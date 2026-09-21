package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// bindConfig is collectors.modules.bind (statistics-channel JSON).
type bindConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type bindCollector struct {
	cfg    bindConfig
	client *http.Client
	url    string
}

func init() {
	Register("bind", func() Collector { return &bindCollector{} })
}

func (b *bindCollector) Name() string { return "bind" }

func (b *bindCollector) Configure(decode func(v any) error) error {
	if err := decode(&b.cfg); err != nil {
		return err
	}
	if b.cfg.Timeout <= 0 {
		b.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (b *bindCollector) Init(reg *registry.Registry) error {
	if b.cfg.Timeout <= 0 {
		if err := b.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	b.client = &http.Client{Timeout: b.cfg.Timeout}
	urls := []string{b.cfg.URL}
	if b.cfg.URL == "" {
		urls = []string{
			"http://127.0.0.1:8053/json/v1/server",
			"http://127.0.0.1:8053/json/v1",
		}
	}
	u, body, err := httpGetTry(context.Background(), b.client, urls)
	if err != nil {
		return err
	}
	if _, err := parseBindJSON(body); err != nil {
		return err
	}
	b.url = u
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "bind.in_opcodes", Title: "Incoming Requests by OpCode", Units: "requests/s", Type: registry.Stacked, Priority: 51600,
			Dimensions: []*registry.Dimension{{ID: "QUERY", Algorithm: inc}, {ID: "UPDATE", Algorithm: inc}}},
		{ID: "bind.nsstats", Title: "Global Server Statistics", Units: "operations/s", Type: registry.Stacked, Priority: 51610,
			Dimensions: []*registry.Dimension{
				{ID: "success", Algorithm: inc}, {ID: "nxdomain", Algorithm: inc},
				{ID: "servfail", Algorithm: inc}, {ID: "nxrrset", Algorithm: inc}, {ID: "recursion", Algorithm: inc}}},
		{ID: "bind.in_qtypes", Title: "Incoming Requests by Query Type", Units: "requests/s", Type: registry.Stacked, Priority: 51620,
			Dimensions: []*registry.Dimension{
				{ID: "A", Algorithm: inc}, {ID: "AAAA", Algorithm: inc}, {ID: "PTR", Algorithm: inc},
				{ID: "MX", Algorithm: inc}, {ID: "SOA", Algorithm: inc}, {ID: "other", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "bind", "bind", "bind"
		reg.AddChart(c)
	}
	return nil
}

func (b *bindCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	body, err := httpGet(ctx, b.client, b.url)
	if err != nil {
		return err
	}
	st, err := parseBindJSON(body)
	if err != nil {
		return err
	}
	_ = reg.Collect("bind.in_opcodes", now, map[string]float64{"QUERY": st.query, "UPDATE": st.update})
	_ = reg.Collect("bind.nsstats", now, map[string]float64{
		"success": st.success, "nxdomain": st.nxdomain, "servfail": st.servfail, "nxrrset": st.nxrrset, "recursion": st.recursion})
	_ = reg.Collect("bind.in_qtypes", now, map[string]float64{
		"A": st.a, "AAAA": st.aaaa, "PTR": st.ptr, "MX": st.mx, "SOA": st.soa, "other": st.other})
	return nil
}

type bindStats struct {
	query, update, success, nxdomain, servfail, nxrrset, recursion float64
	a, aaaa, ptr, mx, soa, other                                   float64
}

func parseBindJSON(b []byte) (bindStats, error) {
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return bindStats{}, err
	}
	server := raw
	if s, ok := raw["server"].(map[string]any); ok {
		server = s
	}
	opcodes := asMap(server["opcodes"])
	if opcodes == nil {
		opcodes = asMap(raw["opcodes"])
	}
	ns := asMap(server["nsstats"])
	if ns == nil {
		ns = asMap(raw["nsstats"])
	}
	qtypes := asMap(server["qtypes"])
	if qtypes == nil {
		qtypes = asMap(raw["qtypes"])
	}
	if opcodes == nil && ns == nil {
		return bindStats{}, fmt.Errorf("bind: missing opcodes/nsstats")
	}
	st := bindStats{
		query: mapNum(opcodes, "QUERY"), update: mapNum(opcodes, "UPDATE"),
		success: mapNum(ns, "QrySuccess"), nxdomain: mapNum(ns, "QryNXDOMAIN"),
		servfail: mapNum(ns, "QrySERVFAIL"), nxrrset: mapNum(ns, "QryNxrrset"),
		recursion: mapNum(ns, "QryRecursion"),
		a:         mapNum(qtypes, "A"), aaaa: mapNum(qtypes, "AAAA"), ptr: mapNum(qtypes, "PTR"),
		mx: mapNum(qtypes, "MX"), soa: mapNum(qtypes, "SOA"),
	}
	for k, v := range qtypes {
		switch k {
		case "A", "AAAA", "PTR", "MX", "SOA":
		default:
			st.other += jsonNum(v)
		}
	}
	return st, nil
}

func asMap(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func mapNum(m map[string]any, k string) float64 {
	if m == nil {
		return 0
	}
	return jsonNum(m[k])
}
