package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// apConfig is collectors.modules.ap (`iw` AP station dump).
type apConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type apIface struct {
	name, ssid                              string
	clients                                 float64
	rxB, txB                                float64
	rxP, txP                                float64
	retries, failed, signal, rxRate, txRate float64
}

type apCollector struct {
	cfg  apConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("ap", func() Collector { return &apCollector{} })
}

func (a *apCollector) Name() string { return "ap" }

func (a *apCollector) Configure(decode func(v any) error) error {
	if err := decode(&a.cfg); err != nil {
		return err
	}
	if a.cfg.Command == "" {
		a.cfg.Command = "iw"
	}
	if a.cfg.Timeout <= 0 {
		a.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (a *apCollector) Init(reg *registry.Registry) error {
	if a.cfg.Command == "" {
		if err := a.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if a.run == nil {
		a.run = execRun(a.cfg.Timeout)
	}
	a.seen = map[string]bool{}
	ifaces, err := a.ifaces(context.Background())
	if err != nil {
		return err
	}
	if len(ifaces) == 0 {
		return fmt.Errorf("ap: no AP interfaces")
	}
	return nil
}

func (a *apCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	ifaces, err := a.ifaces(ctx)
	if err != nil {
		return err
	}
	inc := registry.Incremental
	for _, iface := range ifaces {
		id := sanitizeID(iface.name + "_" + iface.ssid)
		if !a.seen[id] {
			a.seen[id] = true
			for _, ch := range []*registry.Chart{
				{ID: "ap.clients." + id, Context: "ap.clients", Title: "Connected clients", Units: "clients", Priority: 60800,
					Dimensions: []*registry.Dimension{{ID: "clients"}}},
				{ID: "ap.net." + id, Context: "ap.net", Title: "Bandwidth", Units: "kilobits/s", Type: registry.Area, Priority: 60810,
					Dimensions: []*registry.Dimension{
						{ID: "received", Algorithm: inc, Multiplier: 8, Divisor: 1000},
						{ID: "sent", Algorithm: inc, Multiplier: -8, Divisor: 1000},
					}},
				{ID: "ap.packets." + id, Context: "ap.packets", Title: "Packets", Units: "packets/s", Priority: 60820,
					Dimensions: []*registry.Dimension{{ID: "received", Algorithm: inc}, {ID: "sent", Algorithm: inc, Multiplier: -1}}},
				{ID: "ap.issues." + id, Context: "ap.issues", Title: "Transmit issues", Units: "issues/s", Priority: 60830,
					Dimensions: []*registry.Dimension{{ID: "retries", Algorithm: inc}, {ID: "failures", Algorithm: inc}}},
				{ID: "ap.signal." + id, Context: "ap.signal", Title: "Average Signal", Units: "dBm", Priority: 60840,
					Dimensions: []*registry.Dimension{{ID: "average_signal"}}},
				{ID: "ap.bitrate." + id, Context: "ap.bitrate", Title: "Bitrate", Units: "Mbps", Priority: 60850,
					Dimensions: []*registry.Dimension{{ID: "receive"}, {ID: "transmit", Multiplier: -1}}},
			} {
				ch.Family, ch.Plugin, ch.Module = "ap", "ap", "ap"
				reg.AddChart(ch)
			}
		}
		_ = reg.Collect("ap.clients."+id, now, map[string]float64{"clients": iface.clients})
		_ = reg.Collect("ap.net."+id, now, map[string]float64{"received": iface.rxB, "sent": iface.txB})
		_ = reg.Collect("ap.packets."+id, now, map[string]float64{"received": iface.rxP, "sent": iface.txP})
		_ = reg.Collect("ap.issues."+id, now, map[string]float64{"retries": iface.retries, "failures": iface.failed})
		_ = reg.Collect("ap.signal."+id, now, map[string]float64{"average_signal": iface.signal})
		_ = reg.Collect("ap.bitrate."+id, now, map[string]float64{"receive": iface.rxRate, "transmit": iface.txRate})
	}
	return nil
}

func (a *apCollector) ifaces(ctx context.Context) ([]apIface, error) {
	b, err := a.run(ctx, a.cfg.Command, "dev")
	if err != nil {
		return nil, fmt.Errorf("ap: %w", err)
	}
	type tmp struct{ name, ssid, typ string }
	var cur tmp
	var list []tmp
	flush := func() {
		if cur.name != "" && strings.EqualFold(cur.typ, "AP") {
			list = append(list, cur)
		}
		cur = tmp{}
	}
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Interface "):
			flush()
			cur.name = strings.TrimSpace(strings.TrimPrefix(t, "Interface "))
		case strings.HasPrefix(t, "ssid "):
			cur.ssid = strings.TrimSpace(strings.TrimPrefix(t, "ssid "))
		case strings.HasPrefix(t, "type "):
			cur.typ = strings.TrimSpace(strings.TrimPrefix(t, "type "))
		}
	}
	flush()
	var out []apIface
	for _, t := range list {
		st, err := a.station(ctx, t.name)
		if err != nil {
			return nil, fmt.Errorf("ap: %w", err)
		}
		st.name = t.name
		st.ssid = t.ssid
		if st.ssid == "" {
			st.ssid = t.name
		}
		out = append(out, st)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ap: no AP interfaces")
	}
	return out, nil
}

func (a *apCollector) station(ctx context.Context, iface string) (apIface, error) {
	b, err := a.run(ctx, a.cfg.Command, iface, "station", "dump")
	if err != nil {
		return apIface{}, err
	}
	st := apIface{}
	var n, sig, rxR, txR float64
	for _, line := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "Station "):
			n++
		case strings.HasPrefix(t, "rx bytes:"):
			st.rxB += firstFloat(t)
		case strings.HasPrefix(t, "tx bytes:"):
			st.txB += firstFloat(t)
		case strings.HasPrefix(t, "rx packets:"):
			st.rxP += firstFloat(t)
		case strings.HasPrefix(t, "tx packets:"):
			st.txP += firstFloat(t)
		case strings.HasPrefix(t, "tx retries:"):
			st.retries += firstFloat(t)
		case strings.HasPrefix(t, "tx failed:"):
			st.failed += firstFloat(t)
		case strings.HasPrefix(t, "signal:"):
			sig += firstFloat(t)
		case strings.HasPrefix(t, "rx bitrate:"):
			rxR += firstFloat(t)
		case strings.HasPrefix(t, "tx bitrate:"):
			txR += firstFloat(t)
		}
	}
	st.clients = n
	if n > 0 {
		st.signal = sig / n
		st.rxRate = rxR / n
		st.txRate = txR / n
	}
	return st, nil
}
