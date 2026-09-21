package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

type memSink struct {
	mu   sync.Mutex
	rows map[string][]float64
}

func (m *memSink) Append(id string, _ int64, v float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rows == nil {
		m.rows = map[string][]float64{}
	}
	m.rows[id] = append(m.rows[id], v)
}

func newReg(sink registry.Sink) *registry.Registry {
	return registry.New(&registry.Host{ID: "h", Hostname: "h", UpdateEvery: 1}, sink)
}

func TestParserProtocol(t *testing.T) {
	sink := &memSink{}
	reg := newReg(sink)
	now := time.Unix(1_700_000_000, 0)
	p := &Parser{Plugin: "example", Reg: reg, Now: func() time.Time { now = now.Add(time.Second); return now }}
	script := `
# comment
CHART example.random '' "Random Numbers" "count" random example.random line 90000 1 '' example rnd
DIMENSION a 'first value' absolute 1 1
DIMENSION b second incremental 1 1 hidden
CLABEL env prod source
CLABEL "data center" 'dc 1'
CLABEL_COMMIT
VARIABLE HOST threshold = 42
BEGIN example.random
SET a = 5
SET b = 100
END
BEGIN example.random 1000000
SET a = 7
SET b = 160
SET nope = 1
END
BEGIN example.missing
SET a = 1
END
BEGIN example.random
SET a =
END
FLUSH
BOGUS command
`
	err := p.Run(strings.NewReader(script))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := reg.Chart("example.random")
	if !ok {
		t.Fatal("chart not registered")
	}
	if c.Title != "Random Numbers" || c.Units != "count" || c.Family != "random" || c.Priority != 90000 || c.Plugin != "example/example" || c.Module != "rnd" {
		t.Fatalf("chart = %+v", c)
	}
	if c.Labels["env"] != "prod" || c.Labels["data center"] != "dc 1" {
		t.Fatalf("labels = %v", c.Labels)
	}
	da, db := c.Dimension("a"), c.Dimension("b")
	if da == nil || db == nil || da.Name != "first value" || db.Algorithm != registry.Incremental || !db.Hidden {
		t.Fatalf("dims = %+v %+v", da, db)
	}
	if got := sink.rows["example.random|a"]; len(got) != 2 || got[0] != 5 || got[1] != 7 {
		t.Fatalf("a samples = %v", got)
	}
	if got := sink.rows["example.random|b"]; len(got) != 1 || got[0] != 60 { // (160-100)/1s
		t.Fatalf("b samples = %v", got)
	}
	st := p.Stats()
	if st.Samples != 4 || st.Charts != 1 || st.Errors != 5 { // unknown dim, unknown chart, SET/END outside BEGIN, BOGUS
		t.Fatalf("stats = %+v", st)
	}
	if !strings.Contains(st.LastError, "unknown command") {
		t.Fatalf("last error = %q", st.LastError)
	}
	if v := p.Variables(); v["threshold"] != 42 {
		t.Fatalf("variables = %v", v)
	}
	if err := p.Run(strings.NewReader("DISABLE\n")); !errors.Is(err, ErrDisabled) {
		t.Fatalf("DISABLE → %v", err)
	}
}

func TestParserRedeclareKeepsChart(t *testing.T) {
	reg := newReg(&memSink{})
	p := &Parser{Plugin: "x", Reg: reg}
	_ = p.Run(strings.NewReader("CHART x.c\nDIMENSION a\nBEGIN x.c\nSET a = 1\nEND\n"))
	first, _ := reg.Chart("x.c")
	p2 := &Parser{Plugin: "x", Reg: reg}
	_ = p2.Run(strings.NewReader("CHART x.c\nDIMENSION a\nDIMENSION b\nBEGIN x.c\nSET b = 2\nEND\n"))
	second, _ := reg.Chart("x.c")
	if first != second || second.Dimension("b") == nil {
		t.Fatal("re-declared chart must reuse the registry instance and gain new dimensions")
	}
}

func TestFields(t *testing.T) {
	got := fields(`CHART a.b '' "two words" x  'it''s'`)
	want := []string{"CHART", "a.b", "", "two words", "x", "its"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fields = %q", got)
	}
}

func writeScript(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestManagerRunRestartDisableFailed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	writeScript(t, dir, "flaky.plugin", `
echo "CHART flaky.v '' 'V' 'n'"
echo "DIMENSION v"
echo "BEGIN flaky.v"
echo "SET v = $1"
echo "END"
exit 0
`)
	writeScript(t, dir, "quit.plugin", "echo DISABLE\nsleep 5\n")
	writeScript(t, dir, "noexec.txt", "echo nope\n")
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeScript(t, filepath.Join(dir, "vendor"), "sub.plugin", "echo DISABLE\n")
	sink := &memSink{}
	reg := newReg(sink)
	m := New(reg, []Spec{
		{Name: "missing", Command: filepath.Join(dir, "nope.plugin")},
		{Name: "off", Command: "flaky.plugin", Disabled: true},
		{Name: "sub", Command: "vendor/sub.plugin"},
	}, Options{Dir: dir, RestartMin: 10 * time.Millisecond, RestartMax: 20 * time.Millisecond, Disabled: []string{"quit"}})
	// "quit" disabled by name, "off" by spec → only flaky + missing run
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()

	byName := func() map[string]Status {
		out := map[string]Status{}
		for _, s := range m.Status() {
			out[s.Name] = s
		}
		return out
	}
	waitFor(t, func() bool {
		st := byName()
		return st["flaky"].Restarts >= 2 && st["missing"].State == StateFailed && st["sub"].State == StateDisabled
	}, "flaky restarted twice, missing failed, sub ran from the plugins dir")
	st := byName()
	if st["sub"].Command != filepath.Join(dir, "vendor", "sub.plugin") {
		t.Fatalf("relative sub-directory command = %q", st["sub"].Command)
	}
	if st["off"].State != StateDisabled || st["quit"].State != StateDisabled || st["flaky"].Command != filepath.Join(dir, "flaky.plugin") {
		t.Fatalf("status = %+v", st)
	}
	if _, ok := st["noexec"]; ok {
		t.Fatal("non-.plugin file must not be discovered")
	}
	sink.mu.Lock()
	n := len(sink.rows["flaky.v|v"])
	v := sink.rows["flaky.v|v"]
	sink.mu.Unlock()
	if n < 2 || v[0] != 1 { // update_every passed as $1
		t.Fatalf("samples = %v", v)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("manager did not stop")
	}
	if s := byName()["flaky"]; s.State != StateStopped {
		t.Fatalf("after stop: %+v", s)
	}

	// DISABLE ends supervision without restart
	m2 := New(reg, []Spec{{Name: "quit", Command: filepath.Join(dir, "quit.plugin")}}, Options{RestartMin: time.Millisecond})
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	start := time.Now()
	m2.Run(ctx2)
	if s := m2.Status()[0]; s.State != StateDisabled || s.Restarts != 0 || time.Since(start) > 4*time.Second {
		t.Fatalf("quit plugin: %+v after %s", s, time.Since(start))
	}
}

func TestManagerWatchdogKillsSilentPlugin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts")
	}
	dir := t.TempDir()
	writeScript(t, dir, "silent.plugin", "sleep 30\n")
	m := New(newReg(&memSink{}), []Spec{{Name: "silent", Command: filepath.Join(dir, "silent.plugin"), Timeout: 1}},
		Options{RestartMin: 10 * time.Millisecond, RestartMax: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	waitFor(t, func() bool {
		s := m.Status()[0]
		return s.Restarts >= 1 // only the watchdog can end a 30s sleep this fast
	}, "watchdog restart")
	cancel()
	<-done
}
