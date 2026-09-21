package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// wireguardConfig is collectors.modules.wireguard (`wg show all dump`).
type wireguardConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type wireguardCollector struct {
	cfg     wireguardConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	devices map[string]bool
	peers   map[string]bool
}

func init() {
	Register("wireguard", func() Collector { return &wireguardCollector{} })
}

func (w *wireguardCollector) Name() string { return "wireguard" }

func (w *wireguardCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	if w.cfg.Command == "" {
		w.cfg.Command = "wg"
	}
	if w.cfg.Timeout <= 0 {
		w.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (w *wireguardCollector) Init(reg *registry.Registry) error {
	if w.cfg.Command == "" {
		if err := w.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	w.devices, w.peers = map[string]bool{}, map[string]bool{}
	devs, err := w.dump(context.Background())
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return fmt.Errorf("wireguard: no devices")
	}
	return nil
}

func (w *wireguardCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	devs, err := w.dump(ctx)
	if err != nil {
		return err
	}
	if len(devs) == 0 {
		return fmt.Errorf("wireguard: no devices")
	}
	for _, d := range devs {
		w.ensureDevice(reg, d.name)
		_ = reg.Collect("wireguard.device_network_io."+d.name, now, map[string]float64{
			"receive": d.rx, "transmit": d.tx})
		_ = reg.Collect("wireguard.device_peers."+d.name, now, map[string]float64{"peers": float64(len(d.peers))})
		for _, p := range d.peers {
			id := sanitizeID(d.name + "_" + p.pub)
			w.ensurePeer(reg, id, d.name, p.pub)
			_ = reg.Collect("wireguard.peer_network_io."+id, now, map[string]float64{
				"receive": p.rx, "transmit": p.tx})
			_ = reg.Collect("wireguard.peer_latest_handshake_ago."+id, now, map[string]float64{"time": p.ago})
		}
	}
	return nil
}

func (w *wireguardCollector) ensureDevice(reg *registry.Registry, name string) {
	if w.devices[name] {
		return
	}
	w.devices[name] = true
	inc := registry.Incremental
	io := &registry.Chart{ID: "wireguard.device_network_io." + name, Context: "wireguard.device_network_io",
		Title: "Device traffic", Units: "B/s", Type: registry.Area, Priority: 52700,
		Labels: map[string]string{"device": name},
		Dimensions: []*registry.Dimension{
			{ID: "receive", Algorithm: inc}, {ID: "transmit", Algorithm: inc, Multiplier: -1}}}
	peers := &registry.Chart{ID: "wireguard.device_peers." + name, Context: "wireguard.device_peers",
		Title: "Device peers", Units: "peers", Priority: 52710,
		Labels: map[string]string{"device": name}, Dimensions: []*registry.Dimension{{ID: "peers"}}}
	for _, c := range []*registry.Chart{io, peers} {
		c.Family, c.Plugin, c.Module = "wireguard", "wireguard", "wireguard"
		reg.AddChart(c)
	}
}

func (w *wireguardCollector) ensurePeer(reg *registry.Registry, id, device, pub string) {
	if w.peers[id] {
		return
	}
	w.peers[id] = true
	inc := registry.Incremental
	io := &registry.Chart{ID: "wireguard.peer_network_io." + id, Context: "wireguard.peer_network_io",
		Title: "Peer traffic", Units: "B/s", Type: registry.Area, Priority: 52720,
		Labels: map[string]string{"device": device, "public_key": pub},
		Dimensions: []*registry.Dimension{
			{ID: "receive", Algorithm: inc}, {ID: "transmit", Algorithm: inc, Multiplier: -1}}}
	hs := &registry.Chart{ID: "wireguard.peer_latest_handshake_ago." + id, Context: "wireguard.peer_latest_handshake_ago",
		Title: "Peer time elapsed since the latest handshake", Units: "seconds", Priority: 52730,
		Labels:     map[string]string{"device": device, "public_key": pub},
		Dimensions: []*registry.Dimension{{ID: "time"}}}
	for _, c := range []*registry.Chart{io, hs} {
		c.Family, c.Plugin, c.Module = "wireguard", "wireguard", "wireguard"
		reg.AddChart(c)
	}
}

type wgPeer struct {
	pub    string
	rx, tx float64
	ago    float64
}

type wgDevice struct {
	name   string
	rx, tx float64
	peers  []wgPeer
}

func (w *wireguardCollector) dump(ctx context.Context) ([]wgDevice, error) {
	run := w.run
	if run == nil {
		run = execRun(w.cfg.Timeout)
	}
	out, err := run(ctx, w.cfg.Command, "show", "all", "dump")
	if err != nil {
		return nil, fmt.Errorf("wg: %w", err)
	}
	devs := parseWGDump(string(out), time.Now())
	if len(devs) == 0 {
		return nil, fmt.Errorf("wireguard: no devices")
	}
	return devs, nil
}

func parseWGDump(s string, now time.Time) []wgDevice {
	order := []string{}
	byName := map[string]*wgDevice{}
	sc := bufio.NewScanner(bytes.NewReader([]byte(s)))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		name := fields[0]
		if len(fields) == 5 {
			if _, ok := byName[name]; !ok {
				byName[name] = &wgDevice{name: name}
				order = append(order, name)
			}
			continue
		}
		if len(fields) < 8 {
			continue
		}
		d := byName[name]
		if d == nil {
			d = &wgDevice{name: name}
			byName[name] = d
			order = append(order, name)
		}
		pub := fields[1]
		hs, _ := strconv.ParseInt(fields[5], 10, 64)
		rx := firstFloat(fields[6])
		tx := firstFloat(fields[7])
		ago := 0.0
		if hs > 0 {
			ago = now.Sub(time.Unix(hs, 0)).Seconds()
			if ago < 0 {
				ago = 0
			}
		}
		d.rx += rx
		d.tx += tx
		d.peers = append(d.peers, wgPeer{pub: pub, rx: rx, tx: tx, ago: ago})
	}
	out := make([]wgDevice, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out
}
