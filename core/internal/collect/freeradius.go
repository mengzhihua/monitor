package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// freeradiusConfig is collectors.modules.freeradius (radclient status-server).
type freeradiusConfig struct {
	Address string        `yaml:"address"`
	Secret  string        `yaml:"secret"`
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type freeradiusCollector struct {
	cfg freeradiusConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("freeradius", func() Collector { return &freeradiusCollector{} })
}

func (f *freeradiusCollector) Name() string { return "freeradius" }

func (f *freeradiusCollector) Configure(decode func(v any) error) error {
	if err := decode(&f.cfg); err != nil {
		return err
	}
	if f.cfg.Address == "" {
		f.cfg.Address = "127.0.0.1:18121"
	}
	if f.cfg.Secret == "" {
		f.cfg.Secret = "adminsecret"
	}
	if f.cfg.Command == "" {
		f.cfg.Command = "radclient"
	}
	if f.cfg.Timeout <= 0 {
		f.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (f *freeradiusCollector) Init(reg *registry.Registry) error {
	if f.cfg.Address == "" {
		if err := f.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if _, err := f.status(context.Background()); err != nil {
		return err
	}
	inc := registry.Incremental
	for _, c := range []*registry.Chart{
		{ID: "freeradius.authentication", Title: "Authentication", Units: "packets/s", Priority: 52900,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}, {ID: "responses", Algorithm: inc}}},
		{ID: "freeradius.authentication_access_responses", Title: "Authentication Responses", Units: "packets/s", Priority: 52910,
			Dimensions: []*registry.Dimension{
				{ID: "accepts", Algorithm: inc}, {ID: "rejects", Algorithm: inc}, {ID: "challenges", Algorithm: inc}}},
		{ID: "freeradius.bad_authentication", Title: "Bad Authentication Requests", Units: "packets/s", Priority: 52920,
			Dimensions: []*registry.Dimension{
				{ID: "dropped", Algorithm: inc}, {ID: "duplicate", Algorithm: inc},
				{ID: "invalid", Algorithm: inc}, {ID: "malformed", Algorithm: inc}}},
		{ID: "freeradius.accounting", Title: "Accounting", Units: "packets/s", Priority: 52930,
			Dimensions: []*registry.Dimension{{ID: "requests", Algorithm: inc}, {ID: "responses", Algorithm: inc}}},
	} {
		c.Family, c.Plugin, c.Module = "freeradius", "freeradius", "freeradius"
		reg.AddChart(c)
	}
	return nil
}

func (f *freeradiusCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := f.status(ctx)
	if err != nil {
		return err
	}
	g := func(k string) float64 { return m[k] }
	_ = reg.Collect("freeradius.authentication", now, map[string]float64{
		"requests": g("access-requests"), "responses": g("auth-responses")})
	_ = reg.Collect("freeradius.authentication_access_responses", now, map[string]float64{
		"accepts": g("access-accepts"), "rejects": g("access-rejects"), "challenges": g("access-challenges")})
	_ = reg.Collect("freeradius.bad_authentication", now, map[string]float64{
		"dropped": g("auth-dropped-requests"), "duplicate": g("auth-duplicate-requests"),
		"invalid": g("auth-invalid-requests"), "malformed": g("auth-malformed-requests")})
	_ = reg.Collect("freeradius.accounting", now, map[string]float64{
		"requests": g("accounting-requests"), "responses": g("accounting-responses")})
	return nil
}

func (f *freeradiusCollector) status(ctx context.Context) (map[string]float64, error) {
	run := f.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			cctx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
			defer cancel()
			cmd := exec.CommandContext(cctx, name, args...)
			cmd.WaitDelay = execWaitDelay
			cmd.Stdin = strings.NewReader("Message-Authenticator = 0x00\n")
			return cmd.Output()
		}
	}
	out, err := run(ctx, f.cfg.Command, "-t", "1", f.cfg.Address, "status", f.cfg.Secret)
	if err != nil {
		return nil, fmt.Errorf("radclient: %w", err)
	}
	m := parseFreeRADIUSStatus(out)
	if len(m) == 0 {
		return nil, fmt.Errorf("freeradius: no status attributes")
	}
	return m, nil
}

func parseFreeRADIUSStatus(b []byte) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(k))
		key = strings.TrimPrefix(key, "freeradius-total-")
		out[key] = firstFloat(v)
	}
	return out
}
