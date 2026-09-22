// Package export pushes the latest sample of every chart to Graphite,
// InfluxDB line protocol, or a JSON HTTP endpoint (Netdata exporting engine).
package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type Destination struct {
	Type    string            `yaml:"type"` // graphite | influx | json | prometheus
	Address string            `yaml:"address"`
	URL     string            `yaml:"url"`
	Prefix  string            `yaml:"prefix"`
	Headers map[string]string `yaml:"headers"`
	Every   time.Duration     `yaml:"every"`
	// MongoDB only (type: mongodb).
	Database   string `yaml:"database"`
	Collection string `yaml:"collection"`
}

type Engine struct {
	reg  *registry.Registry
	host string
	dest []Destination
	log  *slog.Logger
	http *http.Client
}

func New(reg *registry.Registry, dest []Destination, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	out := dest[:0]
	for _, d := range dest {
		t := strings.ToLower(d.Type)
		if t == "" {
			continue
		}
		d.Type = t
		if d.Every <= 0 {
			d.Every = 10 * time.Second
		}
		if d.Prefix == "" {
			d.Prefix = "monitor"
		}
		out = append(out, d)
	}
	return &Engine{reg: reg, host: reg.Host.Hostname, dest: out, log: log, http: &http.Client{Timeout: 10 * time.Second}}
}

func (e *Engine) Empty() bool { return len(e.dest) == 0 }

func (e *Engine) Run(ctx context.Context) {
	if e.Empty() {
		return
	}
	type sched struct {
		d    Destination
		next time.Time
	}
	jobs := make([]sched, len(e.dest))
	now := time.Now()
	for i, d := range e.dest {
		jobs[i] = sched{d: d, next: now}
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			for i := range jobs {
				if t.Before(jobs[i].next) {
					continue
				}
				if err := e.flush(ctx, jobs[i].d); err != nil {
					e.log.Warn("export failed", "type", jobs[i].d.Type, "err", err)
				}
				jobs[i].next = t.Add(jobs[i].d.Every)
			}
		}
	}
}

func (e *Engine) flush(ctx context.Context, d Destination) error {
	switch d.Type {
	case "graphite":
		return e.flushGraphite(ctx, d)
	case "influx", "influxdb":
		return e.flushInflux(ctx, d)
	case "json":
		return e.flushJSON(ctx, d)
	case "prometheus", "prom", "remote_write", "prometheus-remote-write":
		return e.flushPromRW(ctx, d)
	case "opentsdb", "open_tsdb":
		return e.flushOpenTSDB(ctx, d)
	case "mongodb", "mongo":
		return e.flushMongo(ctx, d)
	case "kinesis", "firehose":
		return e.flushKinesis(ctx, d)
	case "pubsub", "pub/sub", "google-pubsub":
		return e.flushPubSub(ctx, d)
	default:
		return fmt.Errorf("unknown exporter %q", d.Type)
	}
}

type point struct {
	chart, dim string
	ts         int64
	value      float64
}

func (e *Engine) snapshot() []point {
	var out []point
	for _, c := range e.reg.Charts() {
		ts, vals := c.LastValues()
		if ts == 0 {
			continue
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, point{chart: c.ID, dim: k, ts: ts, value: vals[k]})
		}
	}
	return out
}

func (e *Engine) flushGraphite(ctx context.Context, d Destination) error {
	addr := d.Address
	if addr == "" {
		addr = d.URL
	}
	if addr == "" {
		return fmt.Errorf("graphite: address required")
	}
	dialer := net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	var b strings.Builder
	host := graphiteSafe(e.host)
	for _, p := range e.snapshot() {
		fmt.Fprintf(&b, "%s.%s.%s.%s %g %d\n", d.Prefix, host, graphiteSafe(p.chart), graphiteSafe(p.dim), p.value, p.ts)
	}
	_, err = io.WriteString(conn, b.String())
	return err
}

func graphiteSafe(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		case r == '.':
			return '_'
		}
		return '_'
	}, s)
}

func (e *Engine) flushInflux(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("influx: url required")
	}
	var b strings.Builder
	for _, p := range e.snapshot() {
		fmt.Fprintf(&b, "%s,host=%s,chart=%s,dimension=%s value=%g %d\n",
			influxEsc(d.Prefix), influxEsc(e.host), influxEsc(p.chart), influxEsc(p.dim), p.value, p.ts*1e9)
	}
	return e.post(ctx, url, "text/plain; charset=utf-8", []byte(b.String()), d.Headers)
}

func influxEsc(s string) string {
	s = strings.ReplaceAll(s, ",", "\\,")
	s = strings.ReplaceAll(s, " ", "\\ ")
	return strings.ReplaceAll(s, "=", "\\=")
}

func (e *Engine) flushJSON(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("json: url required")
	}
	charts := map[string]any{}
	for _, c := range e.reg.Charts() {
		ts, vals := c.LastValues()
		if ts == 0 {
			continue
		}
		charts[c.ID] = map[string]any{"context": c.Context, "units": c.Units, "last_updated": ts, "dimensions": vals}
	}
	body, err := json.Marshal(map[string]any{"hostname": e.host, "prefix": d.Prefix, "charts": charts})
	if err != nil {
		return err
	}
	return e.post(ctx, url, "application/json", body, d.Headers)
}

func (e *Engine) flushOpenTSDB(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("opentsdb: url required")
	}
	type put struct {
		Metric    string            `json:"metric"`
		Timestamp int64             `json:"timestamp"`
		Value     float64           `json:"value"`
		Tags      map[string]string `json:"tags"`
	}
	var pts []put
	for _, p := range e.snapshot() {
		metric := d.Prefix + "." + graphiteSafe(p.chart) + "." + graphiteSafe(p.dim)
		pts = append(pts, put{Metric: metric, Timestamp: p.ts, Value: p.value,
			Tags: map[string]string{"host": e.host, "chart": p.chart, "dimension": p.dim}})
	}
	body, err := json.Marshal(pts)
	if err != nil {
		return err
	}
	return e.post(ctx, url, "application/json", body, d.Headers)
}

func (e *Engine) post(ctx context.Context, url, ctype string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("User-Agent", "monitord-export/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := e.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}
