package collect

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// statsdConfig is collectors.modules.statsd. The agent listens for the
// classic StatsD line protocol (counter/gauge/timer) and turns each metric
// into a chart, matching Netdata's statsd.plugin.
type statsdConfig struct {
	Listen string `yaml:"listen"` // default 127.0.0.1:8125
}

type statsdCollector struct {
	cfg  statsdConfig
	conn *net.UDPConn

	mu     sync.Mutex
	gauges map[string]float64
	counts map[string]float64
	times  map[string][]float64 // flush as average each tick
}

func init() {
	Register("statsd", func() Collector { return &statsdCollector{} })
}

func (s *statsdCollector) Name() string { return "statsd" }

func (s *statsdCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Listen == "" {
		s.cfg.Listen = "127.0.0.1:8125"
	}
	return nil
}

func (s *statsdCollector) Init(reg *registry.Registry) error {
	if s.cfg.Listen == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	addr, err := net.ResolveUDPAddr("udp", s.cfg.Listen)
	if err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return err
	}
	s.conn = conn
	s.gauges = map[string]float64{}
	s.counts = map[string]float64{}
	s.times = map[string][]float64{}
	go s.readLoop()
	_ = reg
	return nil
}

func (s *statsdCollector) Stop() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
}

func (s *statsdCollector) readLoop() {
	buf := make([]byte, 65535)
	for {
		n, _, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		s.ingest(buf[:n])
	}
}

func (s *statsdCollector) ingest(b []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, line := range bytes.Split(b, []byte{'\n'}) {
		parseStatsDLine(s.gauges, s.counts, s.times, strings.TrimSpace(string(line)))
	}
}

// parseStatsDLine implements `name:value|type[|@rate]`. Returns silently on junk.
func parseStatsDLine(gauges, counts map[string]float64, times map[string][]float64, line string) {
	if line == "" {
		return
	}
	name, rest, ok := strings.Cut(line, ":")
	if !ok || name == "" {
		return
	}
	valStr, kind, ok := strings.Cut(rest, "|")
	if !ok {
		return
	}
	kind, rateStr, _ := strings.Cut(kind, "|@")
	v, err := strconv.ParseFloat(valStr, 64)
	if err != nil {
		return
	}
	rate := 1.0
	if rateStr != "" {
		if r, err := strconv.ParseFloat(rateStr, 64); err == nil && r > 0 && r <= 1 {
			rate = r
		}
	}
	switch strings.ToLower(kind) {
	case "c":
		counts[name] += v / rate
	case "g":
		if strings.HasPrefix(valStr, "+") || strings.HasPrefix(valStr, "-") {
			gauges[name] += v
		} else {
			gauges[name] = v
		}
	case "ms", "h":
		times[name] = append(times[name], v)
	}
}

func (s *statsdCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	s.mu.Lock()
	gauges := s.gauges
	counts := s.counts
	times := s.times
	s.gauges = map[string]float64{}
	s.counts = map[string]float64{}
	s.times = map[string][]float64{}
	// gauges persist (StatsD gauges are last-value); restore them
	for k, v := range gauges {
		s.gauges[k] = v
	}
	s.mu.Unlock()

	flush := func(prefix, title, units string, algo registry.Algorithm, values map[string]float64) {
		if len(values) == 0 {
			return
		}
		id := "statsd." + prefix
		ch, ok := reg.Chart(id)
		if !ok {
			ch = reg.AddChart(&registry.Chart{ID: id, Family: "statsd", Title: title, Units: units,
				Priority: 70000, Plugin: "statsd", Module: "statsd"})
		}
		for name := range values {
			did := sanitizeID(name)
			if ch.Dimension(did) == nil {
				ch.AddDimension(&registry.Dimension{ID: did, Name: name, Algorithm: algo})
			}
		}
		out := map[string]float64{}
		for name, v := range values {
			out[sanitizeID(name)] = v
		}
		_ = reg.Collect(id, now, out)
	}
	flush("gauge", "StatsD gauges", "value", registry.Absolute, gauges)
	// counts are per-tick sums; store as absolute (already a rate over the interval)
	flush("counter", "StatsD counters", "events/s", registry.Absolute, counts)
	avg := map[string]float64{}
	for k, vs := range times {
		if len(vs) == 0 {
			continue
		}
		var sum float64
		for _, v := range vs {
			sum += v
		}
		avg[k] = sum / float64(len(vs))
	}
	flush("timer", "StatsD timers", "ms", registry.Absolute, avg)
	return nil
}
