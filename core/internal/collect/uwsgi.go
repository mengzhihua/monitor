package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// uwsgiConfig is collectors.modules.uwsgi (stats socket JSON).
type uwsgiConfig struct {
	Address string        `yaml:"address"`
	Timeout time.Duration `yaml:"timeout"`
}

type uwsgiCollector struct {
	cfg  uwsgiConfig
	dial func(ctx context.Context, network, address string) (net.Conn, error)
}

func init() {
	Register("uwsgi", func() Collector { return &uwsgiCollector{} })
}

func (u *uwsgiCollector) Name() string { return "uwsgi" }

func (u *uwsgiCollector) Configure(decode func(v any) error) error {
	if err := decode(&u.cfg); err != nil {
		return err
	}
	if u.cfg.Address == "" {
		u.cfg.Address = "127.0.0.1:1717"
	}
	if u.cfg.Timeout <= 0 {
		u.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (u *uwsgiCollector) Init(reg *registry.Registry) error {
	if u.cfg.Address == "" {
		if err := u.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := u.stats(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, ch := range []*registry.Chart{
		{ID: "uwsgi.transmitted_data", Title: "UWSGI Transmitted Data", Units: "bytes/s", Type: registry.Area, Priority: 56900,
			Dimensions: []*registry.Dimension{{ID: "tx", Algorithm: inc}}},
		{ID: "uwsgi.requests", Title: "UWSGI Requests", Units: "requests/s", Priority: 56910,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}}},
		{ID: "uwsgi.harakiris", Title: "UWSGI Dropped Requests", Units: "harakiris/s", Priority: 56920,
			Dimensions: []*registry.Dimension{{ID: "harakiris", Algorithm: inc}}},
		{ID: "uwsgi.exceptions", Title: "UWSGI Raised Exceptions", Units: "exceptions/s", Priority: 56930,
			Dimensions: []*registry.Dimension{{ID: "exceptions", Algorithm: inc}}},
		{ID: "uwsgi.respawns", Title: "UWSGI Respawns", Units: "respawns/s", Priority: 56940,
			Dimensions: []*registry.Dimension{{ID: "respawns", Algorithm: inc}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "uwsgi", "uwsgi", "uwsgi"
		reg.AddChart(ch)
	}
	return nil
}

func (u *uwsgiCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := u.stats(ctx)
	if err != nil {
		return err
	}
	var tx, req, hara, exc, resp float64
	for _, w := range st.Workers {
		tx += float64(w.TX)
		req += float64(w.Requests)
		hara += float64(w.HarakiriCount)
		exc += float64(w.Exceptions)
		resp += float64(w.RespawnCount)
	}
	_ = reg.Collect("uwsgi.transmitted_data", now, map[string]float64{"tx": tx})
	_ = reg.Collect("uwsgi.requests", now, map[string]float64{"requests": req})
	_ = reg.Collect("uwsgi.harakiris", now, map[string]float64{"harakiris": hara})
	_ = reg.Collect("uwsgi.exceptions", now, map[string]float64{"exceptions": exc})
	_ = reg.Collect("uwsgi.respawns", now, map[string]float64{"respawns": resp})
	return nil
}

type uwsgiStats struct {
	Workers []struct {
		ID            int    `json:"id"`
		Requests      int64  `json:"requests"`
		Exceptions    int64  `json:"exceptions"`
		HarakiriCount int64  `json:"harakiri_count"`
		RespawnCount  int64  `json:"respawn_count"`
		TX            int64  `json:"tx"`
		Status        string `json:"status"`
	} `json:"workers"`
}

func (u *uwsgiCollector) stats(ctx context.Context) (uwsgiStats, error) {
	dial := u.dial
	if dial == nil {
		d := net.Dialer{Timeout: u.cfg.Timeout}
		dial = d.DialContext
	}
	conn, err := dial(ctx, "tcp", u.cfg.Address)
	if err != nil {
		return uwsgiStats{}, fmt.Errorf("uwsgi: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(u.cfg.Timeout))
	b, err := io.ReadAll(io.LimitReader(conn, 4<<20))
	if err != nil {
		return uwsgiStats{}, fmt.Errorf("uwsgi: %w", err)
	}
	var st uwsgiStats
	if err := json.Unmarshal(b, &st); err != nil {
		return uwsgiStats{}, fmt.Errorf("uwsgi: %w", err)
	}
	if st.Workers == nil {
		return uwsgiStats{}, fmt.Errorf("uwsgi: no workers")
	}
	return st, nil
}
