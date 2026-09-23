package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// k8sStateConfig is collectors.modules.k8s_state (API nodes/pods).
type k8sStateConfig struct {
	TLS     CollectorTLS  `yaml:"tls"`
	URL     string        `yaml:"url"`
	Token   string        `yaml:"token"`
	Timeout time.Duration `yaml:"timeout"`
	MaxPods int           `yaml:"max_pods"`
}

type k8sStateCollector struct {
	cfg    k8sStateConfig
	client *http.Client
	base   string
	token  string
	nodes  map[string]bool
	pods   map[string]bool
}

func init() {
	Register("k8s_state", func() Collector { return &k8sStateCollector{} })
}

func (k *k8sStateCollector) Name() string { return "k8s_state" }

func (k *k8sStateCollector) Configure(decode func(v any) error) error {
	if err := decode(&k.cfg); err != nil {
		return err
	}
	if k.cfg.Timeout <= 0 {
		k.cfg.Timeout = 3 * time.Second
	}
	if k.cfg.MaxPods <= 0 {
		k.cfg.MaxPods = 100
	}
	return nil
}

func (k *k8sStateCollector) Init(reg *registry.Registry) error {
	if k.cfg.Timeout <= 0 {
		if err := k.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	client, tlsErr := collectorHTTPClient(k.cfg.Timeout, k.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	k.client = client
	k.token = k8sBearer(k.cfg.Token)
	k.nodes, k.pods = map[string]bool{}, map[string]bool{}
	bases := []string{strings.TrimRight(k.cfg.URL, "/")}
	if k.cfg.URL == "" {
		bases = k8sDefaultURLs("")
	}
	var last error
	for _, b := range bases {
		if b == "" {
			continue
		}
		if _, err := k.fetch(context.Background(), b+"/api/v1/nodes"); err != nil {
			last = err
			continue
		}
		k.base = b
		break
	}
	if k.base == "" {
		if last == nil {
			last = fmt.Errorf("k8s_state: no kubernetes API")
		}
		return last
	}
	return nil
}

func (k *k8sStateCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	nb, err := k.fetch(ctx, k.base+"/api/v1/nodes")
	if err != nil {
		return err
	}
	nodes, err := parseK8sNodes(nb)
	if err != nil {
		return err
	}
	pb, err := k.fetch(ctx, k.base+"/api/v1/pods")
	if err != nil {
		return err
	}
	pods, err := parseK8sPods(pb)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return fmt.Errorf("k8s_state: no nodes")
	}
	byNode := map[string][]k8sPod{}
	for _, p := range pods {
		byNode[p.node] = append(byNode[p.node], p)
	}
	for _, n := range nodes {
		k.ensureNode(reg, n)
		id := sanitizeID(n.name)
		cond := map[string]float64{}
		for _, c := range n.conditions {
			dim := sanitizeID(c.typ)
			if dim == "" {
				continue
			}
			ensureDim(reg, "k8s_state.node_condition."+id, dim, &registry.Dimension{ID: dim, Name: c.typ})
			if strings.EqualFold(c.status, "True") {
				cond[dim] = 1
			} else {
				cond[dim] = 0
			}
		}
		_ = reg.Collect("k8s_state.node_condition."+id, now, cond)
		sched := 1.0
		unsched := 0.0
		if n.unschedulable {
			sched, unsched = 0, 1
		}
		_ = reg.Collect("k8s_state.node_schedulability."+id, now, map[string]float64{
			"schedulable": sched, "unschedulable": unsched})
		phase := map[string]float64{"running": 0, "failed": 0, "pending": 0, "succeeded": 0, "unknown": 0}
		ctr := map[string]float64{"running": 0, "waiting": 0, "terminated": 0}
		ncont, ninit := 0.0, 0.0
		for _, p := range byNode[n.name] {
			key := strings.ToLower(p.phase)
			if _, ok := phase[key]; !ok {
				key = "unknown"
			}
			phase[key]++
			ncont += float64(len(p.containers))
			for _, cs := range p.containers {
				ctr[cs.state]++
			}
		}
		_ = reg.Collect("k8s_state.node_pods_phase."+id, now, phase)
		_ = reg.Collect("k8s_state.node_containers."+id, now, map[string]float64{"containers": ncont, "init_containers": ninit})
		_ = reg.Collect("k8s_state.node_containers_state."+id, now, ctr)
		age := now.Sub(n.created).Seconds()
		if age < 0 {
			age = 0
		}
		_ = reg.Collect("k8s_state.node_age."+id, now, map[string]float64{"age": age})
	}
	if len(pods) > k.cfg.MaxPods {
		pods = pods[:k.cfg.MaxPods]
	}
	for _, p := range pods {
		k.ensurePod(reg, p)
		id := sanitizeID(p.namespace + "_" + p.name)
		ph := map[string]float64{"running": 0, "failed": 0, "pending": 0, "succeeded": 0, "unknown": 0}
		key := strings.ToLower(p.phase)
		if _, ok := ph[key]; !ok {
			key = "unknown"
		}
		ph[key] = 1
		_ = reg.Collect("k8s_state.pod_phase."+id, now, ph)
		st := map[string]float64{"running": 0, "waiting": 0, "terminated": 0}
		for _, cs := range p.containers {
			st[cs.state]++
			cid := sanitizeID(p.namespace + "_" + p.name + "_" + cs.name)
			k.ensureContainer(reg, p, cs)
			ready := 0.0
			if cs.ready {
				ready = 1
			}
			_ = reg.Collect("k8s_state.pod_container_readiness_state."+cid, now, map[string]float64{"ready": ready})
			_ = reg.Collect("k8s_state.pod_container_restarts."+cid, now, map[string]float64{"restarts": cs.restarts})
			cst := map[string]float64{"running": 0, "waiting": 0, "terminated": 0}
			cst[cs.state] = 1
			_ = reg.Collect("k8s_state.pod_container_state."+cid, now, cst)
		}
		_ = reg.Collect("k8s_state.pod_containers_state."+id, now, st)
	}
	return nil
}

func (k *k8sStateCollector) ensureNode(reg *registry.Registry, n k8sNode) {
	if k.nodes[n.name] {
		return
	}
	k.nodes[n.name] = true
	id := sanitizeID(n.name)
	labels := map[string]string{"node": n.name}
	charts := []*registry.Chart{
		{ID: "k8s_state.node_condition." + id, Context: "k8s_state.node_condition", Title: "Node Conditions", Units: "state", Priority: 54300, Labels: labels},
		{ID: "k8s_state.node_schedulability." + id, Context: "k8s_state.node_schedulability", Title: "Node Schedulability", Units: "state", Priority: 54310, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "schedulable"}, {ID: "unschedulable"}}},
		{ID: "k8s_state.node_pods_phase." + id, Context: "k8s_state.node_pods_phase", Title: "Node Pods Phase", Units: "pods", Type: registry.Stacked, Priority: 54320, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "failed"}, {ID: "pending"}, {ID: "succeeded"}, {ID: "unknown"}}},
		{ID: "k8s_state.node_containers." + id, Context: "k8s_state.node_containers", Title: "Node Containers", Units: "containers", Priority: 54330, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "containers"}, {ID: "init_containers"}}},
		{ID: "k8s_state.node_containers_state." + id, Context: "k8s_state.node_containers_state", Title: "Node Containers State", Units: "containers", Type: registry.Stacked, Priority: 54340, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "waiting"}, {ID: "terminated"}}},
		{ID: "k8s_state.node_age." + id, Context: "k8s_state.node_age", Title: "Node Age", Units: "seconds", Priority: 54350, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "age"}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "k8s_state", "k8s_state", "k8s_state"
		reg.AddChart(c)
	}
}

func (k *k8sStateCollector) ensurePod(reg *registry.Registry, p k8sPod) {
	key := p.namespace + "/" + p.name
	if k.pods[key] {
		return
	}
	k.pods[key] = true
	id := sanitizeID(p.namespace + "_" + p.name)
	labels := map[string]string{"namespace": p.namespace, "pod": p.name, "node": p.node}
	charts := []*registry.Chart{
		{ID: "k8s_state.pod_phase." + id, Context: "k8s_state.pod_phase", Title: "Pod Phase", Units: "state", Type: registry.Stacked, Priority: 54360, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "failed"}, {ID: "pending"}, {ID: "succeeded"}, {ID: "unknown"}}},
		{ID: "k8s_state.pod_containers_state." + id, Context: "k8s_state.pod_containers_state", Title: "Pod Containers State", Units: "containers", Type: registry.Stacked, Priority: 54370, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "waiting"}, {ID: "terminated"}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "k8s_state", "k8s_state", "k8s_state"
		reg.AddChart(c)
	}
}

func (k *k8sStateCollector) ensureContainer(reg *registry.Registry, p k8sPod, cs k8sContainer) {
	cid := sanitizeID(p.namespace + "_" + p.name + "_" + cs.name)
	if k.pods["ctr:"+cid] {
		return
	}
	k.pods["ctr:"+cid] = true
	labels := map[string]string{"namespace": p.namespace, "pod": p.name, "container": cs.name}
	charts := []*registry.Chart{
		{ID: "k8s_state.pod_container_readiness_state." + cid, Context: "k8s_state.pod_container_readiness_state", Title: "Pod Container Readiness", Units: "state", Priority: 54380, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "ready"}}},
		{ID: "k8s_state.pod_container_restarts." + cid, Context: "k8s_state.pod_container_restarts", Title: "Pod Container Restarts", Units: "restarts", Priority: 54390, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "restarts"}}},
		{ID: "k8s_state.pod_container_state." + cid, Context: "k8s_state.pod_container_state", Title: "Pod Container State", Units: "state", Type: registry.Stacked, Priority: 54400, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "waiting"}, {ID: "terminated"}}},
	}
	for _, c := range charts {
		c.Family, c.Plugin, c.Module = "k8s_state", "k8s_state", "k8s_state"
		reg.AddChart(c)
	}
}

func (k *k8sStateCollector) fetch(ctx context.Context, url string) ([]byte, error) {
	return httpGetToken(ctx, k.client, url, k.token)
}

type k8sNode struct {
	name          string
	created       time.Time
	unschedulable bool
	conditions    []k8sCond
}

type k8sCond struct{ typ, status string }

type k8sPod struct {
	name, namespace, node, phase string
	containers                   []k8sContainer
}

type k8sContainer struct {
	name, state string
	ready       bool
	restarts    float64
}

func parseK8sNodes(b []byte) ([]k8sNode, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name              string `json:"name"`
				CreationTimestamp string `json:"creationTimestamp"`
			} `json:"metadata"`
			Spec struct {
				Unschedulable bool `json:"unschedulable"`
			} `json:"spec"`
			Status struct {
				Conditions []struct {
					Type   string `json:"type"`
					Status string `json:"status"`
				} `json:"conditions"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("k8s_state nodes: %w", err)
	}
	out := make([]k8sNode, 0, len(raw.Items))
	for _, it := range raw.Items {
		n := k8sNode{name: it.Metadata.Name, unschedulable: it.Spec.Unschedulable}
		n.created, _ = time.Parse(time.RFC3339, it.Metadata.CreationTimestamp)
		for _, c := range it.Status.Conditions {
			n.conditions = append(n.conditions, k8sCond{typ: c.Type, status: c.Status})
		}
		out = append(out, n)
	}
	return out, nil
}

func parseK8sPods(b []byte) ([]k8sPod, error) {
	var raw struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase             string `json:"phase"`
				ContainerStatuses []struct {
					Name         string `json:"name"`
					Ready        bool   `json:"ready"`
					RestartCount int    `json:"restartCount"`
					State        struct {
						Running    *struct{} `json:"running"`
						Waiting    *struct{} `json:"waiting"`
						Terminated *struct{} `json:"terminated"`
					} `json:"state"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("k8s_state pods: %w", err)
	}
	out := make([]k8sPod, 0, len(raw.Items))
	for _, it := range raw.Items {
		p := k8sPod{name: it.Metadata.Name, namespace: it.Metadata.Namespace, node: it.Spec.NodeName, phase: it.Status.Phase}
		for _, cs := range it.Status.ContainerStatuses {
			st := "waiting"
			switch {
			case cs.State.Running != nil:
				st = "running"
			case cs.State.Terminated != nil:
				st = "terminated"
			}
			p.containers = append(p.containers, k8sContainer{name: cs.Name, state: st, ready: cs.Ready, restarts: float64(cs.RestartCount)})
		}
		out = append(out, p)
	}
	return out, nil
}
