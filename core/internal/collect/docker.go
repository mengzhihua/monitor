package collect

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dockerConfig is the `collectors.modules.docker` section.
type dockerConfig struct {
	// Address of the Engine API: unix:///var/run/docker.sock (default on
	// unix), tcp://host:2375 or http(s)://host:port.
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
	// Exclude container names (globs); the default hides Kubernetes pause containers.
	Exclude []string `yaml:"exclude"`
}

type dockerCollector struct {
	cfg    dockerConfig
	client *http.Client
	base   string // http://docker or http://host:port
	seen   map[string]*dockerCont
}

type dockerCont struct {
	id, name, image string
	charts          []string
}

func init() {
	Register("docker", func() Collector { return &dockerCollector{} })
}

func (d *dockerCollector) Name() string { return "docker" }

func (d *dockerCollector) Configure(decode func(v any) error) error {
	if err := decode(&d.cfg); err != nil {
		return err
	}
	if d.cfg.Timeout <= 0 {
		d.cfg.Timeout = 5 * time.Second
	}
	if d.cfg.Address == "" {
		if env := os.Getenv("DOCKER_HOST"); env != "" {
			d.cfg.Address = env
		} else if runtime.GOOS == "windows" {
			d.cfg.Address = "tcp://127.0.0.1:2375"
		} else {
			d.cfg.Address = "unix:///var/run/docker.sock"
		}
	}
	if d.cfg.Exclude == nil {
		d.cfg.Exclude = []string{"k8s_POD.*"}
	}
	return nil
}

func (d *dockerCollector) dial() error {
	u, err := url.Parse(d.cfg.Address)
	if err != nil {
		return fmt.Errorf("docker address: %w", err)
	}
	tr := &http.Transport{MaxIdleConns: 4, IdleConnTimeout: 90 * time.Second}
	switch u.Scheme {
	case "unix":
		path := u.Path
		if path == "" {
			path = u.Host
		}
		tr.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		d.base = "http://docker"
	case "tcp", "http":
		d.base = "http://" + u.Host
	case "https":
		d.base = "https://" + u.Host
	default:
		return fmt.Errorf("docker address: unsupported scheme %q", u.Scheme)
	}
	d.client = &http.Client{Transport: tr, Timeout: d.cfg.Timeout}
	return nil
}

func (d *dockerCollector) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.base+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("docker: %s -> %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (d *dockerCollector) Init(reg *registry.Registry) error {
	if d.cfg.Timeout == 0 {
		if err := d.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if err := d.dial(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), d.cfg.Timeout)
	defer cancel()
	var ping struct {
		Version string `json:"Version"`
	}
	if err := d.get(ctx, "/version", &ping); err != nil {
		return fmt.Errorf("docker engine not reachable at %s: %w", d.cfg.Address, err)
	}
	d.seen = map[string]*dockerCont{}
	reg.AddChart(&registry.Chart{ID: "docker.containers", Family: "docker", Title: "Containers by state", Units: "containers",
		Type: registry.Stacked, Priority: 30000, Plugin: "docker", Module: "docker",
		Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "paused"}, {ID: "exited"}, {ID: "other"}}})
	return nil
}

type dockerListEntry struct {
	ID    string   `json:"Id"`
	Names []string `json:"Names"`
	Image string   `json:"Image"`
	State string   `json:"State"`
}

type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

func dockerContName(e dockerListEntry) string {
	if len(e.Names) > 0 {
		return strings.TrimPrefix(e.Names[0], "/")
	}
	if len(e.ID) > 12 {
		return e.ID[:12]
	}
	return e.ID
}

func (d *dockerCollector) excluded(name string) bool {
	for _, p := range d.cfg.Exclude {
		if globMatch(p, name) {
			return true
		}
	}
	return false
}

func (d *dockerCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var list []dockerListEntry
	if err := d.get(ctx, "/containers/json?all=1", &list); err != nil {
		return err
	}
	states := map[string]float64{"running": 0, "paused": 0, "exited": 0, "other": 0}
	live := map[string]bool{}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error
	for _, e := range list {
		switch e.State {
		case "running", "paused", "exited":
			states[e.State]++
		default:
			states["other"]++
		}
		if e.State != "running" {
			continue
		}
		name := dockerContName(e)
		if d.excluded(name) {
			continue
		}
		live[e.ID] = true
		c := d.seen[e.ID]
		if c == nil {
			c = d.addContainer(reg, e, name)
		}
		wg.Add(1)
		go func(c *dockerCont) {
			defer wg.Done()
			var st dockerStats
			if err := d.get(ctx, "/containers/"+c.id+"/stats?stream=false&one-shot=true", &st); err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				return
			}
			d.feed(reg, c, st, now)
		}(c)
	}
	wg.Wait()
	for id, c := range d.seen {
		if !live[id] {
			for _, ch := range c.charts {
				reg.RemoveChart(ch)
			}
			delete(d.seen, id)
		}
	}
	_ = reg.Collect("docker.containers", now, states)
	return firstErr
}

func (d *dockerCollector) addContainer(reg *registry.Registry, e dockerListEntry, name string) *dockerCont {
	c := &dockerCont{id: e.ID, name: name, image: e.Image}
	labels := map[string]string{"container": name, "image": e.Image}
	id := "docker." + sanitizeID(name)
	add := func(suffix, ctx, title, units string, typ registry.ChartType, prio int, dims ...*registry.Dimension) {
		ch := &registry.Chart{ID: id + "." + suffix, Context: ctx, Family: name, Title: title + " (" + name + ")", Units: units,
			Type: typ, Priority: prio, Plugin: "docker", Module: "docker", Labels: labels, Dimensions: dims}
		reg.AddChart(ch)
		c.charts = append(c.charts, ch.ID)
	}
	add("cpu", "docker.container.cpu", "CPU utilization", "percentage", registry.Line, 30010,
		&registry.Dimension{ID: "cpu", Algorithm: registry.Incremental, Multiplier: 1, Divisor: 10_000_000}) // ns/s → % of one core
	add("mem", "docker.container.mem", "Memory usage", "MiB", registry.Area, 30020,
		&registry.Dimension{ID: "used", Multiplier: 1, Divisor: 1 << 20}, &registry.Dimension{ID: "limit", Multiplier: 1, Divisor: 1 << 20, Hidden: true})
	add("net", "docker.container.net", "Network traffic", "kilobits/s", registry.Area, 30030,
		&registry.Dimension{ID: "received", Algorithm: registry.Incremental, Multiplier: 8, Divisor: 1000},
		&registry.Dimension{ID: "sent", Algorithm: registry.Incremental, Multiplier: -8, Divisor: 1000})
	add("io", "docker.container.io", "Block I/O", "KiB/s", registry.Area, 30040,
		&registry.Dimension{ID: "read", Algorithm: registry.Incremental, Multiplier: 1, Divisor: 1024},
		&registry.Dimension{ID: "write", Algorithm: registry.Incremental, Multiplier: -1, Divisor: 1024})
	d.seen[e.ID] = c
	return c
}

func (d *dockerCollector) feed(reg *registry.Registry, c *dockerCont, st dockerStats, now time.Time) {
	id := "docker." + sanitizeID(c.name)
	_ = reg.Collect(id+".cpu", now, map[string]float64{"cpu": float64(st.CPUStats.CPUUsage.TotalUsage)})
	used := st.MemoryStats.Usage
	if v, ok := st.MemoryStats.Stats["inactive_file"]; ok && v < used { // cgroup v2
		used -= v
	} else if v, ok := st.MemoryStats.Stats["cache"]; ok && v < used { // cgroup v1
		used -= v
	}
	_ = reg.Collect(id+".mem", now, map[string]float64{"used": float64(used), "limit": float64(st.MemoryStats.Limit)})
	var rx, tx uint64
	for _, n := range st.Networks {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	if len(st.Networks) > 0 {
		_ = reg.Collect(id+".net", now, map[string]float64{"received": float64(rx), "sent": float64(tx)})
	}
	var rd, wr uint64
	for _, e := range st.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			rd += e.Value
		case "write":
			wr += e.Value
		}
	}
	if len(st.BlkioStats.IOServiceBytesRecursive) > 0 {
		_ = reg.Collect(id+".io", now, map[string]float64{"read": float64(rd), "write": float64(wr)})
	}
}

// sanitizeID makes a chart-id-safe token out of a container/service name.
// Names that needed rewriting get a short hash suffix so distinct names
// ("a.b" vs "a_b") never collapse into the same ID.
func sanitizeID(s string) string {
	var b strings.Builder
	changed := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
			changed = true
		}
	}
	if changed {
		sum := sha1.Sum([]byte(s))
		b.WriteByte('_')
		b.WriteString(hex.EncodeToString(sum[:3]))
	}
	return b.String()
}
