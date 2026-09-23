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

// mlConfig is collectors.modules.ml.
// Detection is Netdata-style k-means (k=2 on lag-window first-differences)
// with a k-sigma fallback until the first model trains.
type mlConfig struct {
	Enabled    *bool         `yaml:"enabled"`
	Window     int           `yaml:"window"` // sample ring, default 120
	Sigma      float64       `yaml:"sigma"`  // k-sigma fallback, default 3
	Lag        int           `yaml:"lag"`    // feature length, default 3
	K          int           `yaml:"k"`      // clusters, default 2
	Models     int           `yaml:"models"` // voting models on different train windows
	MinTrain   int           `yaml:"min_train"`
	TrainEvery time.Duration `yaml:"train_every"`
	MaxTrain   int           `yaml:"max_train_samples"`
	Threshold  float64       `yaml:"threshold"` // percentile of train distances, default 0.99
}

type kmModel struct {
	c0, c1 []float64
	thresh float64
	ok     bool
}

type anomBit struct {
	ts   int64
	rate float64 // 0 or 100
}

type dimML struct {
	values    []float64 // ring of last Window values
	diffRing  []float64 // first-diff ring; grows to MaxTrain, not allocated up front
	diffI     int
	diffN     int
	i, n      int
	prev      float64
	hasPrev   bool
	models    []kmModel
	lastTrain int64
	lastSeen  int64
	anom      bool
	votes     float64 // 0..1 fraction of models calling anomalous
	chart     string
	bits      []anomBit // fixed ring of recent 0/100 flags
	bitI      int
	bitN      int
}

type mlCollector struct {
	cfg         mlConfig
	mu          sync.Mutex
	dims        map[string]*dimML // series id
	unsub       func()
	trainSecond int64 // collection timestamp that owns trainLeft
	trainLeft   int   // k-means jobs still allowed in trainSecond
	gcTick      int64 // last ts/60 that dropped silent series
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
	if m.cfg.Lag <= 0 {
		m.cfg.Lag = 3
	}
	if m.cfg.K <= 0 {
		m.cfg.K = 2
	}
	if m.cfg.Models <= 0 {
		m.cfg.Models = 4
	}
	if m.cfg.MinTrain <= 0 {
		m.cfg.MinTrain = 900 // Netdata default; tests override
	}
	if m.cfg.TrainEvery <= 0 {
		m.cfg.TrainEvery = 3 * time.Hour
	}
	if m.cfg.MaxTrain <= 0 {
		m.cfg.MaxTrain = 14400
	}
	if m.cfg.Threshold <= 0 || m.cfg.Threshold > 1 {
		m.cfg.Threshold = 0.99
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

func (m *mlCollector) onSample(chartID string, ts int64, values map[string]float64) {
	if strings.HasPrefix(chartID, "anomaly_detection.") {
		return
	}
	var trainSt *dimML
	var trainDiffs []float64
	m.mu.Lock()
	if m.trainSecond != ts {
		m.trainSecond = ts
		m.trainLeft = 1 // one k-means fit per collection second, not one per series
	}
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
		st.lastSeen = ts
		st.values[st.i] = v
		st.i = (st.i + 1) % len(st.values)
		if st.n < len(st.values) {
			st.n++
		}
		if st.hasPrev {
			st.pushDiff(v-st.prev, m.cfg.MaxTrain)
		}
		st.prev, st.hasPrev = v, true
		if trainSt == nil && m.trainLeft > 0 && m.due(st, ts) {
			m.trainLeft--
			st.lastTrain = ts
			trainDiffs = st.copyDiffs()
			trainSt = st
		}
		st.anom, st.votes = m.detect(st)
		rate := 0.0
		if st.anom {
			rate = 100
		}
		st.pushBit(anomBit{ts: ts, rate: rate}, m.cfg.Window)
	}
	// Series that disappear (dead units, old IRQs) keep a MaxTrain ring until
	// dropped. Once a minute of collection time is enough; scanning every
	// sample would cost more than it saves.
	if tick := ts / 60; m.gcTick != tick {
		m.gcTick = tick
		cutoff := ts - 15*60
		for id, st := range m.dims {
			if st.lastSeen < cutoff {
				delete(m.dims, id)
			}
		}
	}
	m.mu.Unlock()
	if trainSt == nil {
		return
	}
	models := trainModels(trainDiffs, m.cfg.Lag, m.cfg.Models, m.cfg.Threshold)
	m.mu.Lock()
	trainSt.models = models
	m.mu.Unlock()
}

func (st *dimML) pushDiff(d float64, max int) {
	if max < 1 {
		max = 1
	}
	if len(st.diffRing) == 0 {
		n := 32
		if n > max {
			n = max
		}
		st.diffRing = make([]float64, n)
	} else if st.diffN == len(st.diffRing) && len(st.diffRing) < max {
		n := len(st.diffRing) * 2
		if n > max || n < len(st.diffRing) {
			n = max
		}
		next := make([]float64, n)
		start := st.diffI - st.diffN
		if start < 0 {
			start += len(st.diffRing)
		}
		for i := 0; i < st.diffN; i++ {
			next[i] = st.diffRing[(start+i)%len(st.diffRing)]
		}
		st.diffRing = next
		st.diffI = st.diffN
	}
	st.diffRing[st.diffI] = d
	st.diffI++
	if st.diffI == len(st.diffRing) {
		st.diffI = 0
	}
	if st.diffN < len(st.diffRing) {
		st.diffN++
	}
}

func (st *dimML) pushBit(b anomBit, cap int) {
	if cap < 1 {
		return
	}
	if len(st.bits) != cap {
		st.bits = make([]anomBit, cap)
		st.bitI, st.bitN = 0, 0
	}
	st.bits[st.bitI] = b
	st.bitI++
	if st.bitI == len(st.bits) {
		st.bitI = 0
	}
	if st.bitN < len(st.bits) {
		st.bitN++
	}
}

func (st *dimML) copyDiffs() []float64 {
	if st.diffN == 0 || len(st.diffRing) == 0 {
		return nil
	}
	out := make([]float64, st.diffN)
	start := st.diffI - st.diffN
	if start < 0 {
		start += len(st.diffRing)
	}
	for i := 0; i < st.diffN; i++ {
		out[i] = st.diffRing[(start+i)%len(st.diffRing)]
	}
	return out
}

func (st *dimML) lastDiffs(dst []float64) int {
	n := len(dst)
	if n > st.diffN || len(st.diffRing) == 0 {
		n = st.diffN
	}
	if n == 0 {
		return 0
	}
	start := st.diffI - n
	if start < 0 {
		start += len(st.diffRing)
	}
	for i := 0; i < n; i++ {
		dst[i] = st.diffRing[(start+i)%len(st.diffRing)]
	}
	return n
}

func (m *mlCollector) due(st *dimML, ts int64) bool {
	if st.diffN < m.cfg.MinTrain || st.diffN < m.cfg.Lag+8 {
		return false
	}
	every := int64(m.cfg.TrainEvery.Seconds())
	if every < 1 {
		every = 1
	}
	return st.lastTrain == 0 || ts-st.lastTrain >= every
}

func (m *mlCollector) detect(st *dimML) (bool, float64) {
	var buf [8]float64
	feat := buf[:m.cfg.Lag]
	if m.cfg.Lag > len(buf) {
		feat = make([]float64, m.cfg.Lag)
	}
	if st.lastDiffs(feat) < m.cfg.Lag {
		if len(st.models) > 0 {
			return false, 0
		}
		return m.sigmaAnomalous(st), 0
	}
	var trained, votes int
	for _, md := range st.models {
		if !md.ok {
			continue
		}
		trained++
		if kmAnomalous(md, feat) {
			votes++
		}
	}
	if trained > 0 {
		frac := float64(votes) / float64(trained)
		return votes*2 >= trained, frac // majority
	}
	return m.sigmaAnomalous(st), 0
}

func (m *mlCollector) sigmaAnomalous(st *dimML) bool {
	if st.n < 30 || len(st.values) == 0 {
		return false
	}
	n := st.n
	if n > len(st.values) {
		n = len(st.values)
	}
	start := 0
	if st.n == len(st.values) {
		start = st.i
	}
	var count int
	var sum, sumsq, last float64
	prev := math.NaN()
	for k := 0; k < n; k++ {
		v := st.values[(start+k)%len(st.values)]
		if !math.IsNaN(prev) {
			d := v - prev
			sum += d
			sumsq += d * d
			count++
			last = d
		}
		prev = v
	}
	if count < 20 {
		return false
	}
	sum -= last
	sumsq -= last * last
	c := float64(count - 1)
	mean := sum / c
	variance := sumsq/c - mean*mean
	if variance < 1e-18 {
		return false
	}
	std := math.Sqrt(variance)
	if std < 1e-9 {
		return false
	}
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
	type agg struct {
		total, anom int
		votes       float64
	}
	byChart := map[string]*agg{}
	for _, st := range m.dims {
		c := byChart[st.chart]
		if c == nil {
			c = &agg{}
			byChart[st.chart] = c
		}
		c.total++
		c.votes += st.votes
		if st.anom {
			c.anom++
		}
	}
	out := make([]Weight, 0, len(byChart))
	for chart, c := range byChart {
		rate := 0.0
		if c.total > 0 {
			rate = 100 * float64(c.anom) / float64(c.total)
		}
		score := rate
		if method == "kmeans" {
			if c.total > 0 {
				score = 100 * c.votes / float64(c.total)
			}
			if score == 0 {
				continue
			}
		} else if method == "anomaly-rate" && rate == 0 {
			continue
		}
		out = append(out, Weight{Chart: chart, Context: chart, Score: score, AnomalyRate: rate})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// Rate is the latest 0–100 anomaly bit for one dimension (health.AnomalySource).
func (m *mlCollector) Rate(chart, dim string) (float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.dims[registry.SeriesID(chart, dim)]
	if st == nil {
		return 0, false
	}
	rate := 0.0
	if st.anom {
		rate = 100
	}
	return rate, true
}

// RatesBetween returns stored 0–100 bits whose timestamps fall in [after, before].
func (m *mlCollector) RatesBetween(chart, dim string, after, before int64) []float64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.dims[registry.SeriesID(chart, dim)]
	if st == nil {
		return nil
	}
	if st.bitN == 0 || len(st.bits) == 0 {
		return nil
	}
	start := 0
	if st.bitN == len(st.bits) {
		start = st.bitI
	}
	var out []float64
	for k := 0; k < st.bitN; k++ {
		b := st.bits[(start+k)%len(st.bits)]
		if b.ts >= after && b.ts <= before {
			out = append(out, b.rate)
		}
	}
	return out
}

func (m *mlCollector) DimAnomalies() map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]bool{}
	for id, st := range m.dims {
		if st != nil && st.anom {
			out[id] = true
		}
	}
	return out
}

func lastFeature(diffs []float64, lag int) []float64 {
	if lag <= 0 || len(diffs) < lag {
		return nil
	}
	return append([]float64(nil), diffs[len(diffs)-lag:]...)
}

func trainModels(diffs []float64, lag, nModels int, pct float64) []kmModel {
	if nModels < 1 {
		nModels = 1
	}
	out := make([]kmModel, nModels)
	for i := 0; i < nModels; i++ {
		frac := float64(i+1) / float64(nModels)
		n := int(math.Round(frac * float64(len(diffs))))
		if n < lag+8 {
			continue
		}
		out[i] = trainOne(diffs[len(diffs)-n:], lag, pct)
	}
	return out
}

func trainOne(diffs []float64, lag int, pct float64) kmModel {
	pts := features(diffs, lag)
	if len(pts) < 8 {
		return kmModel{}
	}
	c0, c1 := kmeans2(pts, 16)
	if c0 == nil {
		return kmModel{}
	}
	dists := make([]float64, len(pts))
	for i, p := range pts {
		dists[i] = min(l2(p, c0), l2(p, c1))
	}
	sort.Float64s(dists)
	idx := int(pct * float64(len(dists)-1))
	if idx < 0 {
		idx = 0
	}
	th := dists[idx]
	if th < 1e-12 {
		th = 1e-12
	}
	return kmModel{c0: c0, c1: c1, thresh: th, ok: true}
}

func features(diffs []float64, lag int) [][]float64 {
	if len(diffs) < lag {
		return nil
	}
	out := make([][]float64, 0, len(diffs)-lag+1)
	for i := lag; i <= len(diffs); i++ {
		out = append(out, append([]float64(nil), diffs[i-lag:i]...))
	}
	return out
}

func kmAnomalous(m kmModel, feat []float64) bool {
	if !m.ok || len(feat) != len(m.c0) {
		return false
	}
	return min(l2(feat, m.c0), l2(feat, m.c1)) > m.thresh
}

func kmeans2(points [][]float64, iters int) (c0, c1 []float64) {
	if len(points) < 2 {
		return nil, nil
	}
	// Deterministic init: smallest / largest L2 from origin.
	minI, maxI := 0, 0
	minN, maxN := l2(points[0], nil), l2(points[0], nil)
	for i, p := range points {
		n := l2(p, nil)
		if n < minN {
			minN, minI = n, i
		}
		if n > maxN {
			maxN, maxI = n, i
		}
	}
	if minI == maxI {
		maxI = len(points) - 1
	}
	c0, c1 = append([]float64(nil), points[minI]...), append([]float64(nil), points[maxI]...)
	dim := len(c0)
	assign := make([]int, len(points))
	for it := 0; it < iters; it++ {
		changed := false
		for i, p := range points {
			a := 0
			if l2(p, c1) < l2(p, c0) {
				a = 1
			}
			if assign[i] != a {
				assign[i] = a
				changed = true
			}
		}
		sum0, sum1 := make([]float64, dim), make([]float64, dim)
		n0, n1 := 0, 0
		for i, p := range points {
			if assign[i] == 0 {
				n0++
				for d := 0; d < dim; d++ {
					sum0[d] += p[d]
				}
			} else {
				n1++
				for d := 0; d < dim; d++ {
					sum1[d] += p[d]
				}
			}
		}
		if n0 == 0 || n1 == 0 {
			return c0, c1
		}
		for d := 0; d < dim; d++ {
			c0[d] = sum0[d] / float64(n0)
			c1[d] = sum1[d] / float64(n1)
		}
		if it > 0 && !changed {
			break
		}
	}
	return c0, c1
}

func l2(a, b []float64) float64 {
	var s float64
	for i, x := range a {
		y := 0.0
		if b != nil && i < len(b) {
			y = b[i]
		}
		d := x - y
		s += d * d
	}
	return math.Sqrt(s)
}
