package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// redfishConfig is collectors.modules.redfish (BMC Redfish Systems).
type redfishConfig struct {
	TLS      CollectorTLS  `yaml:"tls"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
}

type redfishCollector struct {
	cfg    redfishConfig
	client *http.Client
	url    string
	seen   map[string]bool
}

func init() {
	Register("redfish", func() Collector { return &redfishCollector{} })
}

func (r *redfishCollector) Name() string { return "redfish" }

func (r *redfishCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (r *redfishCollector) Init(reg *registry.Registry) error {
	if r.cfg.Timeout <= 0 {
		if err := r.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	client, tlsErr := collectorHTTPClient(r.cfg.Timeout, r.cfg.TLS)
	if tlsErr != nil {
		return tlsErr
	}
	r.client = client
	r.seen = map[string]bool{}
	base := strings.TrimRight(r.cfg.URL, "/")
	if base == "" {
		base = "https://127.0.0.1/redfish/v1"
	}
	r.url = base
	sys, err := r.systems(context.Background())
	if err != nil {
		return err
	}
	if len(sys) == 0 {
		return fmt.Errorf("redfish: no systems")
	}
	return nil
}

func (r *redfishCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	sys, err := r.systems(ctx)
	if err != nil {
		return err
	}
	for _, s := range sys {
		id := sanitizeID(s.id)
		if !r.seen[id] {
			r.seen[id] = true
			ch := &registry.Chart{ID: "redfish.system_health_state." + id, Context: "redfish.system_health_state",
				Title: "System health", Units: "state", Priority: 57500,
				Dimensions: []*registry.Dimension{{ID: "ok"}, {ID: "warning"}, {ID: "critical"}}}
			ch.Family, ch.Plugin, ch.Module = "redfish", "redfish", "redfish"
			reg.AddChart(ch)
		}
		h := strings.ToLower(s.health)
		_ = reg.Collect("redfish.system_health_state."+id, now, map[string]float64{
			"ok": bool01(h == "ok"), "warning": bool01(h == "warning"), "critical": bool01(h == "critical"),
		})
	}
	return nil
}

type redfishSys struct{ id, health string }

func (r *redfishCollector) systems(ctx context.Context) ([]redfishSys, error) {
	b, err := httpGetAuth(ctx, r.client, r.url+"/Systems", r.cfg.User, r.cfg.Password)
	if err != nil {
		b, err = httpGetAuth(ctx, r.client, r.url+"/Systems/1", r.cfg.User, r.cfg.Password)
		if err != nil {
			return nil, fmt.Errorf("redfish: %w", err)
		}
		return parseRedfishSystems(b, true)
	}
	return parseRedfishSystems(b, false)
}

func parseRedfishSystems(b []byte, single bool) ([]redfishSys, error) {
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("redfish: %w", err)
	}
	if single || nestString(m, "Id") != "" || nestString(m, "Name") != "" {
		id := nestString(m, "Id")
		if id == "" {
			id = nestString(m, "Name")
		}
		if id == "" {
			id = "1"
		}
		h := nestString(m, "Status", "Health")
		if h == "" {
			h = "OK"
		}
		return []redfishSys{{id: id, health: h}}, nil
	}
	var out []redfishSys
	for _, mem := range nestSlice(m, "Members") {
		mm, _ := mem.(map[string]any)
		odata, _ := mm["@odata.id"].(string)
		id := nestString(mm, "Id")
		if id == "" {
			id = odata
		}
		h := nestString(mm, "Status", "Health")
		if h == "" {
			h = "OK"
		}
		if id != "" {
			out = append(out, redfishSys{id: id, health: h})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("redfish: no systems")
	}
	return out, nil
}
