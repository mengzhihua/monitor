package api

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func (s *Server) handleAllMetrics(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	switch r.URL.Query().Get("format") {
	case "prometheus":
		s.writePrometheusFor(w, v)
	case "csv":
		s.writeAllMetricsCSV(w, v)
	case "shell":
		s.writeAllMetricsShell(w, v)
	default:
		out := map[string]any{}
		for _, c := range v.reg.Charts() {
			ts, vals := c.LatestValues()
			dims := map[string]any{}
			for _, d := range c.Dims() {
				if v, ok := vals[d.ID]; ok {
					dims[d.ID] = map[string]any{"name": d.Name, "value": round3(v)}
				}
			}
			out[c.ID] = map[string]any{"name": c.ID, "context": c.Context, "family": c.Family, "units": c.Units, "last_updated": ts, "dimensions": dims}
		}
		writeJSON(w, out)
	}
}

func promName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func (s *Server) writeAllMetricsCSV(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	csvWriter := csv.NewWriter(w)
	defer csvWriter.Flush()
	if err := csvWriter.Write([]string{"chart", "dimension", "value", "timestamp"}); err != nil {
		return
	}
	for _, c := range v.reg.Charts() {
		ts, vals := c.LatestValues()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if err := csvWriter.Write([]string{csvText(c.ID), csvText(k), strconv.FormatFloat(vals[k], 'g', -1, 64), strconv.FormatInt(ts, 10)}); err != nil {
				return
			}
		}
	}
}

func (s *Server) writeAllMetricsShell(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "# hostname=%s\n", v.hostname)
	for _, c := range v.reg.Charts() {
		_, vals := c.LatestValues()
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		prefix := "NETDATA_" + promName(c.ID)
		for _, k := range keys {
			fmt.Fprintf(w, "%s_%s=%g\n", prefix, promName(k), vals[k])
		}
	}
}

// writePrometheus exposes the local host and, on a hub, every remote node
// (instance label = hostname, node label = node id).
func (s *Server) writePrometheus(w http.ResponseWriter, u User) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.writeProcessMetrics(w)
	s.writeCollectorTimingMetrics(w)
	seen := map[string]bool{}
	s.writePromCharts(w, s.reg.Host.Hostname, "", s.reg.Charts(), seen)
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			if !s.roomAllowed(u, n.ID) {
				continue
			}
			s.writePromCharts(w, n.Host.Hostname, n.ID, n.Registry().Charts(), seen)
		}
	}
}

func (s *Server) writeProcessMetrics(w http.ResponseWriter) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	series := 0
	if s.db != nil {
		if tiers := s.db.Tiers(); len(tiers) > 0 {
			// Tier 0 is the highest-resolution store used for live charts.
			series = tiers[0].Series
		}
	}
	host := s.reg.Host.Hostname
	fmt.Fprintf(w, "# HELP monitor_process_heap_alloc_bytes Go heap bytes currently allocated.\n# TYPE monitor_process_heap_alloc_bytes gauge\n")
	fmt.Fprintf(w, "monitor_process_heap_alloc_bytes{instance=%q} %d\n", host, ms.HeapAlloc)
	fmt.Fprintf(w, "# HELP monitor_process_heap_sys_bytes Bytes of heap memory obtained from the OS.\n# TYPE monitor_process_heap_sys_bytes gauge\n")
	fmt.Fprintf(w, "monitor_process_heap_sys_bytes{instance=%q} %d\n", host, ms.HeapSys)
	fmt.Fprintf(w, "# HELP monitor_process_goroutines Number of goroutines.\n# TYPE monitor_process_goroutines gauge\n")
	fmt.Fprintf(w, "monitor_process_goroutines{instance=%q} %d\n", host, runtime.NumGoroutine())
	fmt.Fprintf(w, "# HELP monitor_tsdb_series Number of series in the highest-resolution TSDB tier.\n# TYPE monitor_tsdb_series gauge\n")
	fmt.Fprintf(w, "monitor_tsdb_series{instance=%q} %d\n", host, series)
	fmt.Fprintf(w, "# HELP monitor_charts Number of registered charts on this process.\n# TYPE monitor_charts gauge\n")
	fmt.Fprintf(w, "monitor_charts{instance=%q} %d\n", host, len(s.reg.Charts()))
	if s.sched != nil {
		if tb, ok := s.sched.Collector("ml").(collect.TrainBuffer); ok && tb != nil {
			fmt.Fprintf(w, "# HELP monitor_ml_train_buffer_bytes Bytes retained for anomaly-detection training history.\n# TYPE monitor_ml_train_buffer_bytes gauge\n")
			fmt.Fprintf(w, "monitor_ml_train_buffer_bytes{instance=%q} %d\n", host, tb.TrainBufferBytes())
		}
	}
}

// writeCollectorTimingMetrics exposes per-collector Collect durations so
// operators can find slow modules without scraping /api/v1/collectors.
func (s *Server) writeCollectorTimingMetrics(w http.ResponseWriter) {
	if s.sched == nil {
		return
	}
	host := s.reg.Host.Hostname
	fmt.Fprintf(w, "# HELP monitor_collector_last_run_ms Last Collect duration in milliseconds.\n# TYPE monitor_collector_last_run_ms gauge\n")
	fmt.Fprintf(w, "# HELP monitor_collector_max_run_ms Longest Collect duration since process start, milliseconds.\n# TYPE monitor_collector_max_run_ms gauge\n")
	fmt.Fprintf(w, "# HELP monitor_collector_runs Total Collect invocations since process start.\n# TYPE monitor_collector_runs counter\n")
	for _, st := range s.sched.Status() {
		if !st.Enabled && st.Runs == 0 {
			continue
		}
		fmt.Fprintf(w, "monitor_collector_last_run_ms{collector=%q,instance=%q} %d\n", st.Name, host, st.LastRun)
		fmt.Fprintf(w, "monitor_collector_max_run_ms{collector=%q,instance=%q} %d\n", st.Name, host, st.MaxRun)
		fmt.Fprintf(w, "monitor_collector_runs{collector=%q,instance=%q} %d\n", st.Name, host, st.Runs)
	}
}

func (s *Server) writePrometheusFor(w http.ResponseWriter, v *view) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	s.writePromCharts(w, v.hostname, v.id, v.reg.Charts(), map[string]bool{})
}

func (s *Server) writePromCharts(w http.ResponseWriter, host, node string, charts []*registry.Chart, seen map[string]bool) {
	// HELP/TYPE may appear once per metric family, so charts sharing a
	// context (per-core cpu, per-disk io, ...) are grouped by metric name.
	sort.SliceStable(charts, func(i, j int) bool { return charts[i].Context < charts[j].Context })
	for _, c := range charts {
		ts, vals := c.LatestValues()
		if len(vals) == 0 {
			continue
		}
		metric := "monitor_" + promName(c.Context)
		if !seen[metric] {
			seen[metric] = true
			fmt.Fprintf(w, "# HELP %s %s (%s)\n# TYPE %s gauge\n", metric, c.Title, c.Units, metric)
		}
		keys := make([]string, 0, len(vals))
		for k := range vals {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			var lbl strings.Builder
			fmt.Fprintf(&lbl, `chart=%q,family=%q,dimension=%q,instance=%q`, c.ID, c.Family, k, host)
			if node != "" {
				fmt.Fprintf(&lbl, `,node=%q`, node)
			}
			for lk, lv := range c.Labels {
				fmt.Fprintf(&lbl, `,%s=%q`, promName(lk), lv)
			}
			fmt.Fprintf(w, "%s{%s} %g %d\n", metric, lbl.String(), vals[k], ts*1000)
		}
	}
}
