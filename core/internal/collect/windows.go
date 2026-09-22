package collect

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// windowsConfig is collectors.modules.windows (Netdata windows.plugin subset).
type windowsConfig struct {
	Command string        `yaml:"command"` // sc.exe for the services function
	Timeout time.Duration `yaml:"timeout"`
}

// windowsSnap is one sample of Windows process accounting.
type windowsSnap struct {
	Running, Blocked, Total, Threads int64
	Handles                          int64
	Ctxt                             uint64
	hasCtxt                          bool
}

type windowsCollector struct {
	cfg  windowsConfig
	snap func(ctx context.Context) (windowsSnap, error)
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("windows", func() Collector { return &windowsCollector{} })
}

func (w *windowsCollector) Name() string { return "windows" }

func (w *windowsCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	if w.cfg.Command == "" {
		w.cfg.Command = "sc"
	}
	if w.cfg.Timeout <= 0 {
		w.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (w *windowsCollector) Init(reg *registry.Registry) error {
	if w.cfg.Command == "" {
		if err := w.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if w.snap == nil && runtime.GOOS != "windows" {
		return errors.New("windows collector is windows-only")
	}
	s, err := w.sample(context.Background())
	if err != nil {
		return err
	}
	if s.Total == 0 && s.Threads == 0 {
		return fmt.Errorf("windows: no process accounting")
	}
	add := func(ch *registry.Chart) {
		if _, ok := reg.Chart(ch.ID); ok {
			return
		}
		if ch.Plugin == "" {
			ch.Plugin = "windows"
		}
		if ch.Module == "" {
			ch.Module = "system"
		}
		reg.AddChart(ch)
	}
	add(&registry.Chart{ID: "system.processes", Family: "processes", Title: "System processes", Units: "processes",
		Priority: 210, Dimensions: []*registry.Dimension{{ID: "running"}, {ID: "blocked"}, {ID: "total"}}})
	add(&registry.Chart{ID: "system.threads", Family: "processes", Title: "System threads", Units: "threads",
		Priority: 215, Dimensions: []*registry.Dimension{{ID: "threads"}}})
	if s.hasCtxt || s.Ctxt > 0 {
		add(&registry.Chart{ID: "system.ctxt", Family: "processes", Title: "CPU context switches", Units: "switches/s",
			Priority: 211, Dimensions: []*registry.Dimension{incDim("switches")}})
	}
	if s.Handles > 0 {
		add(&registry.Chart{ID: "system.handles", Family: "processes", Title: "System handles", Units: "handles",
			Priority: 216, Dimensions: []*registry.Dimension{{ID: "handles"}}})
	}
	return nil
}

func (w *windowsCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	s, err := w.sample(ctx)
	if err != nil {
		return err
	}
	if _, ok := reg.Chart("system.processes"); ok {
		_ = reg.Collect("system.processes", now, map[string]float64{
			"running": float64(s.Running), "blocked": float64(s.Blocked), "total": float64(s.Total),
		})
	}
	if _, ok := reg.Chart("system.threads"); ok {
		_ = reg.Collect("system.threads", now, map[string]float64{"threads": float64(s.Threads)})
	}
	if _, ok := reg.Chart("system.ctxt"); ok {
		_ = reg.Collect("system.ctxt", now, map[string]float64{"switches": float64(s.Ctxt)})
	}
	if _, ok := reg.Chart("system.handles"); ok {
		_ = reg.Collect("system.handles", now, map[string]float64{"handles": float64(s.Handles)})
	}
	return nil
}

func (w *windowsCollector) sample(ctx context.Context) (windowsSnap, error) {
	if w.snap != nil {
		return w.snap(ctx)
	}
	return liveWindowsSnap(ctx)
}

func liveWindowsFromProcs(ctx context.Context) (windowsSnap, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return windowsSnap{}, err
	}
	var s windowsSnap
	s.Total = int64(len(procs))
	for _, p := range procs {
		if ctx.Err() != nil {
			break
		}
		if n, err := p.NumThreadsWithContext(ctx); err == nil {
			s.Threads += int64(n)
		}
		if st, err := p.StatusWithContext(ctx); err == nil {
			switch {
			case containsFold(st, process.Running):
				s.Running++
			case containsFold(st, process.Blocked), containsFold(st, "disk-sleep"), containsFold(st, "uninterruptible"):
				s.Blocked++
			default:
				s.Running++ // idle/sleep still count as present
			}
		} else {
			s.Running++
		}
		if n, err := p.NumFDsWithContext(ctx); err == nil {
			s.Handles += int64(n)
		}
		if cs, err := p.NumCtxSwitchesWithContext(ctx); err == nil && cs != nil {
			s.Ctxt += uint64(cs.Voluntary + cs.Involuntary)
			s.hasCtxt = true
		}
	}
	if s.Running+s.Blocked == 0 {
		s.Running = s.Total
	}
	return s, nil
}

func containsFold(ss []string, want string) bool {
	want = strings.ToLower(want)
	for _, s := range ss {
		if strings.ToLower(s) == want {
			return true
		}
	}
	return false
}

func (w *windowsCollector) Functions() []Function {
	return []Function{{
		Name:    "windows-services",
		Help:    "Windows Service Control Manager (sc query)",
		Timeout: 10,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			return w.services(ctx, args)
		},
	}}
}

type winServiceRow struct {
	Name    string `json:"name"`
	Display string `json:"display"`
	State   string `json:"state"`
	Type    string `json:"type"`
}

func (w *windowsCollector) services(ctx context.Context, args map[string]string) (Table, error) {
	run := w.run
	if run == nil {
		run = execRun(w.cfg.Timeout)
	}
	out, err := run(ctx, w.cfg.Command, "query", "state=", "all")
	if err != nil {
		return Table{}, fmt.Errorf("sc query: %w", err)
	}
	rows := parseSCQuery(out)
	q := strings.ToLower(args["query"])
	filtered := rows
	if q != "" {
		filtered = nil
		for _, r := range rows {
			if strings.Contains(strings.ToLower(r.Name+" "+r.Display+" "+r.State), q) {
				filtered = append(filtered, r)
			}
		}
	}
	tab := Table{Columns: []string{"name", "display", "state", "type"}, Total: len(filtered), Rows: make([]any, len(filtered))}
	for i := range filtered {
		tab.Rows[i] = filtered[i]
	}
	return tab, nil
}

func parseSCQuery(b []byte) []winServiceRow {
	var rows []winServiceRow
	var cur winServiceRow
	flush := func() {
		if cur.Name == "" {
			return
		}
		rows = append(rows, cur)
		cur = winServiceRow{}
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "SERVICE_NAME:"):
			flush()
			cur.Name = strings.TrimSpace(strings.TrimPrefix(trim, "SERVICE_NAME:"))
		case strings.HasPrefix(trim, "DISPLAY_NAME:"):
			cur.Display = strings.TrimSpace(strings.TrimPrefix(trim, "DISPLAY_NAME:"))
		case strings.HasPrefix(trim, "STATE"):
			// STATE : 4  RUNNING
			parts := strings.Fields(trim)
			if n := len(parts); n > 0 {
				cur.State = strings.ToLower(parts[n-1])
			}
		case strings.HasPrefix(trim, "TYPE"):
			parts := strings.Fields(trim)
			if n := len(parts); n > 0 {
				cur.Type = parts[n-1]
			}
		}
	}
	flush()
	return rows
}
