package collect

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// mlConfig is collectors.modules.ml. A lightweight edge detector flags
// dimensions whose latest first-difference is more than `sigma` standard
// deviations from the trailing window (Netdata's anomaly bit / rate).
type mlConfig struct {
	Enabled *bool   `yaml:"enabled"`
	Window  int     `yaml:"window"` // samples of history, default 120
	Sigma   float64 `yaml:"sigma"`  // default 3
}

type dimML struct {
	values []float64 // ring of last Window values
	i, n   int
	anom   bool
	chart  string
}

type mlCollector struct {
	cfg   mlConfig
	mu    sync.Mutex
	dims  map[string]*dimML // series id
	unsub func()
}

func init() {
	Register("ml", func() Collector { return &mlCollector{} })
}

func (m *mlCollector) Name() string { return "ml" }

func (m *mlCollector) Configure(decode func(v any) error) error {
	if err := decode(&m.cfg); err != nil {
		return err
	}
	if m.cfg.Window <= 0 {
		m.cfg.Window = 120
	}
	if m.cfg.Sigma <= 0 {
		m.cfg.Sigma = 3
	}
	return nil
}

func (m *mlCollector) Init(reg *registry.Registry) error {
	if m.cfg.Window <= 0 {
		if err := m.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if m.cfg.Enabled != nil && !*m.cfg.Enabled {
		return fmt.Errorf("disabled")
	}
	m.dims = map[string]*dimML{}
	reg.AddChart(&registry.Chart{ID: "anomaly_detection.anomaly_rate", Family: "anomalies",
		Title: "Anomaly rate", Units: "percentage", Type: registry.Area, Priority: 50,
		Plugin: "ml", Module: "ml", Dimensions: []*registry.Dimension{{ID: "anomaly_rate"}}})
	reg.AddChart(&registry.Chart{ID: "anomaly_detection.anomalous_dims", Family: "anomalies",
		Title: "Anomalous dimensions", Units: "dimensions", Priority: 51,
		Plugin: "ml", Module: "ml", Dimensions: []*registry.Dimension{{ID: "anomalous"}, {ID: "normal"}}})
	reg.Subscribe(m.onSample)
	return nil
}

func (m *mlCollector) onSample(chartID string, _ int64, values map[string]float64) {
	if strings.HasPrefix(chartID, "anomaly_detection.") {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for dim, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		id := registry.SeriesID(chartID, dim)
		st := m.dims[id]
		if st == nil {
			st = &dimML{values: make([]float64, m.cfg.Window), chart: chartID}
			m.dims[id] = st
		}
		st.values[st.i] = v
		st.i = (st.i + 1) % len(st.values)
		if st.n < len(st.values) {
			st.n++
		}
		st.anom = m.isAnomalous(st)
	}
}

func (m *mlCollector) isAnomalous(st *dimML) bool {
	if st.n < 30 { // need a little history before flagging
		return false
	}
	// first differences of the occupied window
	n := st.n
	if n > len(st.values) {
		n = len(st.values)
	}
	var diffs []float64
	prev := math.NaN()
	// walk oldest → newest
	start := 0
	if st.n == len(st.values) {
		start = st.i
	}
	for k := 0; k < n; k++ {
		v := st.values[(start+k)%len(st.values)]
		if !math.IsNaN(prev) {
			diffs = append(diffs, v-prev)
		}
		prev = v
	}
	if len(diffs) < 20 {
		return false
	}
	mean, std := meanStd(diffs[:len(diffs)-1]) // train on all but last
	if std < 1e-9 {
		return false
	}
	last := diffs[len(diffs)-1]
	return math.Abs(last-mean) > m.cfg.Sigma*std
}

func meanStd(xs []float64) (mean, std float64) {
	if len(xs) == 0 {
		return 0, 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	mean = s / float64(len(xs))
	var v float64
	for _, x := range xs {
		d := x - mean
		v += d * d
	}
	return mean, math.Sqrt(v / float64(len(xs)))
}

func (m *mlCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	m.mu.Lock()
	var anom, total float64
	for _, st := range m.dims {
		total++
		if st.anom {
			anom++
		}
	}
	m.mu.Unlock()
	rate := 0.0
	if total > 0 {
		rate = 100 * anom / total
	}
	_ = reg.Collect("anomaly_detection.anomaly_rate", now, map[string]float64{"anomaly_rate": rate})
	_ = reg.Collect("anomaly_detection.anomalous_dims", now, map[string]float64{"anomalous": anom, "normal": total - anom})
	return nil
}

func (m *mlCollector) Weights(method string) []Weight {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.dims == nil {
		return nil
	}
	byChart := map[string]struct{ total, anom int }{}
	for _, st := range m.dims {
		c := byChart[st.chart]
		c.total++
		if st.anom {
			c.anom++
		}
		byChart[st.chart] = c
	}
	out := make([]Weight, 0, len(byChart))
	for chart, c := range byChart {
		rate := 0.0
		if c.total > 0 {
			rate = 100 * float64(c.anom) / float64(c.total)
		}
		if method == "anomaly-rate" && rate == 0 {
			continue
		}
		out = append(out, Weight{Chart: chart, Context: chart, Score: rate, AnomalyRate: rate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}
