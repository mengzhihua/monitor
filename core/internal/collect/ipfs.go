package collect

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// ipfsConfig is collectors.modules.ipfs (Kubo HTTP API :5001).
type ipfsConfig struct {
	URL     string        `yaml:"url"`
	Timeout time.Duration `yaml:"timeout"`
}

type ipfsCollector struct {
	cfg    ipfsConfig
	client *http.Client
	url    string
}

func init() {
	Register("ipfs", func() Collector { return &ipfsCollector{} })
}

func (i *ipfsCollector) Name() string { return "ipfs" }

func (i *ipfsCollector) Configure(decode func(v any) error) error {
	if err := decode(&i.cfg); err != nil {
		return err
	}
	if i.cfg.Timeout <= 0 {
		i.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (i *ipfsCollector) Init(reg *registry.Registry) error {
	if i.cfg.Timeout <= 0 {
		if err := i.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	i.client = &http.Client{Timeout: i.cfg.Timeout}
	base := strings.TrimRight(i.cfg.URL, "/")
	if base == "" {
		base = "http://127.0.0.1:5001"
	}
	i.url = base
	if _, err := i.bw(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "ipfs.bandwidth", Title: "IPFS Bandwidth", Units: "bytes/s", Type: registry.Area, Priority: 57900,
			Dimensions: []*registry.Dimension{{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}},
		{ID: "ipfs.peers", Title: "IPFS Peers", Units: "peers", Priority: 57910,
			Dimensions: []*registry.Dimension{{ID: "peers"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "ipfs", "ipfs", "ipfs"
		reg.AddChart(ch)
	}
	return nil
}

func (i *ipfsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	bw, err := i.bw(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("ipfs.bandwidth", now, map[string]float64{"in": nestFloat(bw, "TotalIn"), "out": nestFloat(bw, "TotalOut")})
	peers := 0.0
	if pb, err := httpPost(ctx, i.client, i.url+"/api/v0/swarm/peers"); err == nil {
		if m, e := jsonMap(pb); e == nil {
			peers = float64(len(nestSlice(m, "Peers")))
		}
	}
	_ = reg.Collect("ipfs.peers", now, map[string]float64{"peers": peers})
	return nil
}

func (i *ipfsCollector) bw(ctx context.Context) (map[string]any, error) {
	b, err := httpPost(ctx, i.client, i.url+"/api/v0/stats/bw")
	if err != nil {
		b, err = httpGet(ctx, i.client, i.url+"/api/v0/stats/bw")
		if err != nil {
			return nil, fmt.Errorf("ipfs: %w", err)
		}
	}
	m, err := jsonMap(b)
	if err != nil {
		return nil, fmt.Errorf("ipfs: %w", err)
	}
	if _, ok := m["TotalIn"]; !ok {
		return nil, fmt.Errorf("ipfs: no bandwidth")
	}
	return m, nil
}
