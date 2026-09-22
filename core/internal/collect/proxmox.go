package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// proxmoxConfig is collectors.modules.proxmox (PVE REST /api2/json).
type proxmoxConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Token    string        `yaml:"token"` // PVEAPIToken=USER@REALM!TOKENID=UUID
	Timeout  time.Duration `yaml:"timeout"`
}

type proxmoxCollector struct {
	cfg    proxmoxConfig
	client *http.Client
	base   string
	ticket string
	csrf   string
	seen   map[string]bool
	get    func(ctx context.Context, path string) ([]byte, error)
}

func init() {
	Register("proxmox", func() Collector { return &proxmoxCollector{} })
}

func (p *proxmoxCollector) Name() string { return "proxmox" }

func (p *proxmoxCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 5 * time.Second
	}
	if p.cfg.URL == "" {
		p.cfg.URL = "https://127.0.0.1:8006"
	}
	return nil
}

func (p *proxmoxCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout <= 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	p.base = strings.TrimRight(p.cfg.URL, "/")
	p.seen = map[string]bool{}
	if p.client == nil {
		var err error
		p.client, err = collectorHTTPClient(p.cfg.Timeout, p.cfg.TLS)
		if err != nil {
			return err
		}
	}
	if p.get == nil {
		p.get = p.doGet
	}
	if p.cfg.User == "" && p.cfg.Token == "" {
		return fmt.Errorf("proxmox: no credentials")
	}
	if _, err := p.resources(context.Background()); err != nil {
		return err
	}
	return nil
}

func (p *proxmoxCollector) doGet(ctx context.Context, path string) ([]byte, error) {
	if p.ticket == "" && p.cfg.Token == "" {
		if err := p.login(ctx); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.base+path, nil)
	if err != nil {
		return nil, err
	}
	if p.cfg.Token != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+p.cfg.Token)
	} else {
		req.Header.Set("Cookie", "PVEAuthCookie="+p.ticket)
		if p.csrf != "" {
			req.Header.Set("CSRFPreventionToken", p.csrf)
		}
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("proxmox %s: %s", path, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

func (p *proxmoxCollector) login(ctx context.Context) error {
	form := url.Values{"username": {p.cfg.User}, "password": {p.cfg.Password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.base+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("proxmox login: %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var wrap struct {
		Data struct {
			Ticket string `json:"ticket"`
			CSRF   string `json:"CSRFPreventionToken"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return err
	}
	if wrap.Data.Ticket == "" {
		return fmt.Errorf("proxmox: empty ticket")
	}
	p.ticket, p.csrf = wrap.Data.Ticket, wrap.Data.CSRF
	return nil
}

func (p *proxmoxCollector) resources(ctx context.Context) ([]proxmoxResource, error) {
	b, err := p.get(ctx, "/api2/json/cluster/resources")
	if err != nil {
		return nil, err
	}
	return parseProxmoxResources(b)
}

func (p *proxmoxCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	rs, err := p.resources(ctx)
	if err != nil {
		return err
	}
	for _, r := range rs {
		switch r.Type {
		case "node":
			id := sanitizeID(r.Node)
			p.ensureNode(reg, r.Node, id)
			_ = reg.Collect("proxmox.node_cpu."+id, now, map[string]float64{"used": r.CPU * 100})
			_ = reg.Collect("proxmox.node_mem."+id, now, map[string]float64{"used": r.Mem, "free": r.MaxMem - r.Mem})
			online, off := 0.0, 1.0
			if r.Status == "online" {
				online, off = 1, 0
			}
			_ = reg.Collect("proxmox.node_status."+id, now, map[string]float64{"online": online, "offline": off})
		case "qemu", "lxc":
			name := r.Name
			if name == "" {
				name = fmt.Sprintf("%s-%d", r.Type, r.VMID)
			}
			id := sanitizeID(fmt.Sprintf("%s_%d", r.Type, r.VMID))
			p.ensureVM(reg, name, r.Type, id)
			running, stopped := 0.0, 1.0
			if r.Status == "running" {
				running, stopped = 1, 0
			}
			_ = reg.Collect("proxmox.vm_status."+id, now, map[string]float64{"running": running, "stopped": stopped})
			_ = reg.Collect("proxmox.vm_cpu."+id, now, map[string]float64{"used": r.CPU * 100})
			_ = reg.Collect("proxmox.vm_mem."+id, now, map[string]float64{"used": r.Mem, "free": r.MaxMem - r.Mem})
		}
	}
	return nil
}

func (p *proxmoxCollector) ensureNode(reg *registry.Registry, node, id string) {
	key := "node:" + id
	if p.seen[key] {
		return
	}
	p.seen[key] = true
	lbl := map[string]string{"node": node}
	add := func(c *registry.Chart) {
		c.Family, c.Plugin, c.Module, c.Labels = "proxmox", "proxmox", "proxmox", lbl
		reg.AddChart(c)
	}
	add(&registry.Chart{ID: "proxmox.node_status." + id, Context: "proxmox.node_status", Title: "Proxmox node " + node,
		Units: "status", Priority: 58100, Dimensions: []*registry.Dimension{{ID: "online"}, {ID: "offline"}}})
	add(&registry.Chart{ID: "proxmox.node_cpu." + id, Context: "proxmox.node_cpu", Title: "Proxmox node " + node + " CPU",
		Units: "percentage", Priority: 58110, Dimensions: []*registry.Dimension{{ID: "used"}}})
	add(&registry.Chart{ID: "proxmox.node_mem." + id, Context: "proxmox.node_mem", Title: "Proxmox node " + node + " memory",
		Units: "bytes", Priority: 58120, Type: registry.Stacked, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
}

func (p *proxmoxCollector) ensureVM(reg *registry.Registry, name, kind, id string) {
	if p.seen[id] {
		return
	}
	p.seen[id] = true
	lbl := map[string]string{"name": name, "type": kind}
	add := func(c *registry.Chart) {
		c.Family, c.Plugin, c.Module, c.Labels = "proxmox", "proxmox", "proxmox", lbl
		reg.AddChart(c)
	}
	add(&registry.Chart{ID: "proxmox.vm_status." + id, Context: "proxmox.vm_status", Title: "Proxmox " + kind + " " + name,
		Units: "status", Priority: 58130, Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "stopped"}}})
	add(&registry.Chart{ID: "proxmox.vm_cpu." + id, Context: "proxmox.vm_cpu", Title: "Proxmox " + name + " CPU",
		Units: "percentage", Priority: 58140, Dimensions: []*registry.Dimension{{ID: "used"}}})
	add(&registry.Chart{ID: "proxmox.vm_mem." + id, Context: "proxmox.vm_mem", Title: "Proxmox " + name + " memory",
		Units: "bytes", Priority: 58150, Type: registry.Stacked, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
}

type proxmoxResource struct {
	Type   string  `json:"type"`
	Node   string  `json:"node"`
	Name   string  `json:"name"`
	Status string  `json:"status"`
	VMID   int     `json:"vmid"`
	CPU    float64 `json:"cpu"`
	Mem    float64 `json:"mem"`
	MaxMem float64 `json:"maxmem"`
}

func parseProxmoxResources(b []byte) ([]proxmoxResource, error) {
	var wrap struct {
		Data []proxmoxResource `json:"data"`
	}
	if err := json.Unmarshal(b, &wrap); err != nil {
		return nil, err
	}
	if wrap.Data == nil {
		return nil, fmt.Errorf("proxmox: empty resources")
	}
	return wrap.Data, nil
}
