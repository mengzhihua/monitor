// Package registry holds the metric metadata model (Host → Chart → Dimension)
// and turns raw collected values into samples according to each dimension's
// algorithm, multiplier and divisor before handing them to the TSDB.
package registry

import (
	"fmt"
	"math"
	"sort"
	"sync"
	"time"
)

type Algorithm string

const (
	Absolute                   Algorithm = "absolute"
	Incremental                Algorithm = "incremental"
	PercentageOfAbsoluteRow    Algorithm = "percentage-of-absolute-row"
	PercentageOfIncrementalRow Algorithm = "percentage-of-incremental-row"
)

type ChartType string

const (
	Line    ChartType = "line"
	Area    ChartType = "area"
	Stacked ChartType = "stacked"
)

type Host struct {
	ID          string            `json:"id"`
	Hostname    string            `json:"hostname"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Labels      map[string]string `json:"labels"`
	UpdateEvery int               `json:"update_every"`
}

type Dimension struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Algorithm  Algorithm `json:"algorithm"`
	Multiplier int64     `json:"multiplier"`
	Divisor    int64     `json:"divisor"`
	Hidden     bool      `json:"hidden,omitempty"`

	lastRaw float64
	lastTS  int64
	hasLast bool
}

// SeriesID is the TSDB key of a dimension: "<chart id>|<dimension id>".
func SeriesID(chartID, dimID string) string { return chartID + "|" + dimID }

type Chart struct {
	ID          string            `json:"id"`
	Context     string            `json:"context"`
	Family      string            `json:"family"`
	Title       string            `json:"title"`
	Units       string            `json:"units"`
	Type        ChartType         `json:"chart_type"`
	Priority    int               `json:"priority"`
	UpdateEvery int               `json:"update_every"`
	Plugin      string            `json:"plugin"`
	Module      string            `json:"module"`
	Labels      map[string]string `json:"labels,omitempty"`
	Dimensions  []*Dimension      `json:"dimensions"`

	mu    sync.Mutex
	dimIx map[string]*Dimension
	last  map[string]float64
	lastT int64
}

func (c *Chart) Dimension(id string) *Dimension {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dimIx[id]
}

// Dims returns a snapshot of the chart's dimensions in registration order.
func (c *Chart) Dims() []*Dimension {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]*Dimension(nil), c.Dimensions...)
}

// AddDimension registers a dimension; multiplier/divisor default to 1.
func (c *Chart) AddDimension(d *Dimension) *Dimension {
	if d.Multiplier == 0 {
		d.Multiplier = 1
	}
	if d.Divisor == 0 {
		d.Divisor = 1
	}
	if d.Name == "" {
		d.Name = d.ID
	}
	if d.Algorithm == "" {
		d.Algorithm = Absolute
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if old, ok := c.dimIx[d.ID]; ok {
		return old
	}
	c.dimIx[d.ID] = d
	c.Dimensions = append(c.Dimensions, d)
	return d
}

// Sink receives finished samples (one per dimension per collection).
type Sink interface {
	Append(seriesID string, ts int64, value float64)
}

type Registry struct {
	Host *Host

	mu     sync.RWMutex
	charts map[string]*Chart
	sink   Sink
	subs   []func(chartID string, ts int64, values map[string]float64)
}

func New(host *Host, sink Sink) *Registry {
	return &Registry{Host: host, charts: map[string]*Chart{}, sink: sink}
}

// Subscribe registers a callback that is invoked for every completed chart
// collection with the final (post-algorithm) values. Used by the live WS feed.
func (r *Registry) Subscribe(fn func(chartID string, ts int64, values map[string]float64)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subs = append(r.subs, fn)
}

// AddChart registers a chart (idempotent by ID) and returns the stored instance.
func (r *Registry) AddChart(c *Chart) *Chart {
	if c.Type == "" {
		c.Type = Line
	}
	if c.Context == "" {
		c.Context = c.ID
	}
	if c.UpdateEvery == 0 {
		c.UpdateEvery = r.Host.UpdateEvery
	}
	if c.Priority == 0 {
		c.Priority = 100000
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.charts[c.ID]; ok {
		return old
	}
	c.dimIx = map[string]*Dimension{}
	c.last = map[string]float64{}
	dims := c.Dimensions
	c.Dimensions = nil
	for _, d := range dims {
		c.AddDimension(d)
	}
	r.charts[c.ID] = c
	return c
}

// ReplaceChart installs c as the definition of chart c.ID, carrying over the
// last values of dimensions that survive. Unlike AddChart it updates
// metadata (title, units, dimension names/hidden) and drops dimensions
// absent from c; readers always go through the registry, so swapping the
// instance is safe without touching the old chart.
func (r *Registry) ReplaceChart(c *Chart) *Chart {
	r.mu.Lock()
	old, ok := r.charts[c.ID]
	r.mu.Unlock()
	if !ok {
		return r.AddChart(c)
	}
	lastT, last := old.LastValues()
	if c.Type == "" {
		c.Type = Line
	}
	if c.Context == "" {
		c.Context = c.ID
	}
	if c.UpdateEvery == 0 {
		c.UpdateEvery = r.Host.UpdateEvery
	}
	if c.Priority == 0 {
		c.Priority = 100000
	}
	c.dimIx = map[string]*Dimension{}
	c.last = map[string]float64{}
	c.lastT = lastT
	dims := c.Dimensions
	c.Dimensions = nil
	for _, d := range dims {
		c.AddDimension(d)
		if v, ok := last[d.ID]; ok {
			c.last[d.ID] = v
		}
	}
	r.mu.Lock()
	r.charts[c.ID] = c
	r.mu.Unlock()
	return c
}

// RemoveChart forgets a chart (e.g. a container that went away). Stored
// samples stay in the database until retention drops them.
func (r *Registry) RemoveChart(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.charts[id]; !ok {
		return false
	}
	delete(r.charts, id)
	return true
}

func (r *Registry) Chart(id string) (*Chart, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.charts[id]
	return c, ok
}

// Charts returns all charts sorted by priority then id.
func (r *Registry) Charts() []*Chart {
	r.mu.RLock()
	out := make([]*Chart, 0, len(r.charts))
	for _, c := range r.charts {
		out = append(out, c)
	}
	r.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority < out[j].Priority
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// LastValues returns the most recent post-algorithm values of a chart.
func (c *Chart) LastValues() (int64, map[string]float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]float64, len(c.last))
	for k, v := range c.last {
		out[k] = v
	}
	return c.lastT, out
}

// Ingest stores already-computed (post-algorithm) values for a chart, as
// received from a remote agent, without applying dimension algorithms.
func (r *Registry) Ingest(chartID string, sec int64, values map[string]float64) error {
	c, ok := r.Chart(chartID)
	if !ok {
		return fmt.Errorf("registry: unknown chart %q", chartID)
	}
	vals := make(map[string]float64, len(values))
	c.mu.Lock()
	for id, v := range values {
		if c.dimIx[id] == nil || math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		vals[id] = v
		c.last[id] = v
	}
	if sec >= c.lastT {
		c.lastT = sec
	}
	c.mu.Unlock()
	if r.sink != nil {
		for id, v := range vals {
			r.sink.Append(SeriesID(chartID, id), sec, v)
		}
	}
	r.mu.RLock()
	subs := r.subs
	r.mu.RUnlock()
	for _, fn := range subs {
		fn(chartID, sec, vals)
	}
	return nil
}

// Collect feeds one collection round of raw values for a chart. ts is the
// collection time; raw values are keyed by dimension id. Dimensions missing from
// raw produce no sample (a gap). Returns an error if the chart is unknown.
func (r *Registry) Collect(chartID string, ts time.Time, raw map[string]float64) error {
	c, ok := r.Chart(chartID)
	if !ok {
		return fmt.Errorf("registry: unknown chart %q", chartID)
	}
	sec := ts.Unix()
	c.mu.Lock()
	// First pass: per-dimension absolute / incremental value.
	vals := make(map[string]float64, len(raw))
	var rowSum float64
	for id, v := range raw {
		d := c.dimIx[id]
		if d == nil {
			continue
		}
		var out float64
		switch d.Algorithm {
		case Incremental, PercentageOfIncrementalRow:
			if !d.hasLast {
				d.lastRaw, d.lastTS, d.hasLast = v, sec, true
				continue
			}
			dt := float64(sec - d.lastTS)
			if dt <= 0 {
				dt = float64(c.UpdateEvery)
			}
			delta := v - d.lastRaw
			d.lastRaw, d.lastTS = v, sec
			if delta < 0 { // counter reset / overflow
				continue
			}
			out = delta / dt
		default:
			out = v
		}
		out = out * float64(d.Multiplier) / float64(d.Divisor)
		vals[id] = out
		rowSum += out
	}
	// Second pass: percentage-of-row algorithms.
	for id := range vals {
		d := c.dimIx[id]
		if d.Algorithm == PercentageOfAbsoluteRow || d.Algorithm == PercentageOfIncrementalRow {
			if rowSum == 0 {
				vals[id] = 0
			} else {
				vals[id] = vals[id] * 100 / rowSum
			}
		}
	}
	for id, v := range vals {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			delete(vals, id)
			continue
		}
		c.last[id] = v
	}
	c.lastT = sec
	c.mu.Unlock()

	if r.sink != nil {
		for id, v := range vals {
			r.sink.Append(SeriesID(chartID, id), sec, v)
		}
	}
	r.mu.RLock()
	subs := r.subs
	r.mu.RUnlock()
	for _, fn := range subs {
		fn(chartID, sec, vals)
	}
	return nil
}
