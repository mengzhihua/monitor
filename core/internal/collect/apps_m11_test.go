package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM11CollectorsRegistered(t *testing.T) {
	for _, name := range []string{"cgroup", "k8s_kubelet", "k8s_kubeproxy", "k8s_state", "k8s_apiserver"} {
		found := false
		for _, n := range Available() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func writeFakeCgroup(t *testing.T, root, name string, usec, mem, rbytes int) {
	t.Helper()
	dir := filepath.Join(root, "system.slice", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec " + strconv.Itoa(usec) + "\nuser_usec " + strconv.Itoa(usec/2) + "\nsystem_usec " + strconv.Itoa(usec/2) + "\n",
		"memory.current": strconv.Itoa(mem) + "\n",
		"memory.stat":    "inactive_file 0\n",
		"io.stat":        "253:0 rbytes=" + strconv.Itoa(rbytes) + " wbytes=0 rios=1 wios=0\n",
		"pids.current":   "3\n",
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCgroupCollectorFakeTree(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu io memory pids\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeCgroup(t, root, "docker-abcdef012345.scope", 1_000_000, 40<<20, 2048)
	writeFakeCgroup(t, root, "sshd.service", 1_000_000, 10<<20, 0)

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	c := &cgroupCollector{}
	if err := c.Configure(func(v any) error {
		cfg := v.(*cgroupConfig)
		cfg.CgroupRoot = root
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("cgroup_abcdef012345.cpu"); !ok {
		t.Fatal("missing docker cgroup chart")
	}
	if _, ok := reg.Chart("cgroup_sshd.cpu"); ok {
		t.Fatal("systemd service should be skipped")
	}
}

func TestK8sKubeletMetrics(t *testing.T) {
	body := `# TYPE kubelet_running_pods gauge
kubelet_running_pods 4
# TYPE kubelet_running_containers gauge
kubelet_running_containers 7
# TYPE kubelet_runtime_operations_total counter
kubelet_runtime_operations_total{operation_type="create"} 10
kubelet_runtime_operations_total{operation_type="stop"} 2
# TYPE kubelet_runtime_operations_errors_total counter
kubelet_runtime_operations_errors_total{operation_type="create"} 1
# TYPE kubelet_node_config_error gauge
kubelet_node_config_error 0
# TYPE rest_client_requests_total counter
rest_client_requests_total{code="200",method="GET"} 30
rest_client_requests_total{code="500",method="POST"} 2
# TYPE token_count counter
token_count 5
token_fail_count 1
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	k := &k8sKubeletCollector{cfg: k8sKubeletConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := k.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := k.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("k8s_kubelet.kubelet_pods_running")
	if !ok {
		t.Fatal("missing pods chart")
	}
	_, v := ch.LastValues()
	if v["total"] != 4 {
		t.Fatalf("pods=%v", v)
	}
}

func TestK8sKubeproxyMetrics(t *testing.T) {
	body := `# TYPE kubeproxy_sync_proxy_rules_duration_seconds_count counter
kubeproxy_sync_proxy_rules_duration_seconds_count 9
# TYPE rest_client_requests_total counter
rest_client_requests_total{code="200",method="GET"} 4
# TYPE rest_client_request_duration_seconds summary
rest_client_request_duration_seconds{quantile="0.5"} 0.001
rest_client_request_duration_seconds{quantile="0.9"} 0.002
rest_client_request_duration_seconds{quantile="0.99"} 0.003
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	k := &k8sKubeproxyCollector{cfg: k8sKubeproxyConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := k.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := k.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestK8sApiserverMetrics(t *testing.T) {
	body := `# TYPE apiserver_request_total counter
apiserver_request_total{code="200",verb="GET"} 10
apiserver_request_total{code="500",verb="POST"} 2
# TYPE apiserver_dropped_requests_total counter
apiserver_dropped_requests_total 1
# TYPE apiserver_current_inflight_requests gauge
apiserver_current_inflight_requests{requestKind="mutating"} 3
apiserver_current_inflight_requests{requestKind="readOnly"} 5
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	k := &k8sApiserverCollector{cfg: k8sApiserverConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := k.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := k.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("k8s_apiserver.inflight_requests")
	if !ok {
		t.Fatal("missing inflight")
	}
	_, v := ch.LastValues()
	if v["mutating"] != 3 || v["read_only"] != 5 {
		t.Fatalf("%v", v)
	}
}

func TestK8sStateAPI(t *testing.T) {
	nodes := `{"items":[{"metadata":{"name":"n1","creationTimestamp":"2026-01-01T00:00:00Z"},"spec":{"unschedulable":false},"status":{"conditions":[{"type":"Ready","status":"True"}]}}]}`
	pods := `{"items":[{"metadata":{"name":"web","namespace":"default"},"spec":{"nodeName":"n1"},"status":{"phase":"Running","containerStatuses":[{"name":"app","ready":true,"restartCount":2,"state":{"running":{}}}]}}]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/api/v1/nodes"):
			_, _ = w.Write([]byte(nodes))
		case strings.HasSuffix(r.URL.Path, "/api/v1/pods"):
			_, _ = w.Write([]byte(pods))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	k := &k8sStateCollector{cfg: k8sStateConfig{URL: srv.URL, Timeout: time.Second, MaxPods: 50}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := k.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := k.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("k8s_state.node_condition.n1"); !ok {
		t.Fatal("missing node chart")
	}
	if _, ok := reg.Chart("k8s_state.pod_container_restarts.default_web_app"); !ok {
		t.Fatal("missing container restarts")
	}
}

func TestM11AutoDisable(t *testing.T) {
	k := &k8sKubeletCollector{cfg: k8sKubeletConfig{URL: "http://127.0.0.1:1/metrics", Timeout: 50 * time.Millisecond}}
	if err := k.Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected kubelet disable")
	}
	c := &cgroupCollector{cfg: cgroupConfig{CgroupRoot: "/no/such/cgroup", Max: 10}}
	if err := c.Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected cgroup disable")
	}
}
