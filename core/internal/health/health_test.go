package health

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestExpr(t *testing.T) {
	vars := func(n string) (float64, bool) {
		switch n {
		case "this":
			return 90, true
		case "status":
			return float64(StatusWarning), true
		case "WARNING":
			return float64(StatusWarning), true
		case "CRITICAL":
			return float64(StatusCritical), true
		case "used":
			return 3, true
		case "free":
			return 1, true
		}
		return 0, false
	}
	cases := map[string]float64{
		"1 + 2 * 3":   7,
		"(1 + 2) * 3": 9,
		"-$this":      -90,
		"$this > (($status >= $WARNING) ? (75) : (85))":  1,
		"$this > (($status == $CRITICAL) ? (85) : (95))": 0,
		"$used * 100 / ($used + $free)":                  75,
		"$missing":                                       math.NaN(),
		"isnan($missing) ? 5 : 6":                        5,
		"abs(-3) + min(4, 2, 9) + max(1, 7)":             12,
		"$this > 50 && $this < 100":                      1,
		"$this > 50 AND NOT ($this < 100)":               0,
		"10 % 3":                                         1,
		"1 / 0":                                          math.NaN(),
		"${this} == 90":                                  1,
		"$this > 80 ? ($this > 85 ? 2 : 1) : 0":          2,
		"$this != nan":                                   1,
	}
	for src, want := range cases {
		e, err := ParseExpr(src)
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		got := e.Eval(vars)
		if math.IsNaN(want) != math.IsNaN(got) || (!math.IsNaN(want) && got != want) {
			t.Errorf("%q = %v, want %v", src, got, want)
		}
	}
	for _, bad := range []string{"1 +", "$", "foo(1)", "(1", "1 ? 2", "abs()", "3 4"} {
		if _, err := ParseExpr(bad); err == nil {
			t.Errorf("%q should not parse", bad)
		}
	}
	if e, err := ParseExpr("  "); err != nil || e != nil {
		t.Fatalf("empty expr = %v %v", e, err)
	}
}

func TestParseLookupDelayRepeat(t *testing.T) {
	l, err := ParseLookup("average -10m unaligned of user,system, nice")
	if err != nil {
		t.Fatal(err)
	}
	if l.Method != tsdb.GroupAverage || l.After != 10*time.Minute || len(l.Dimensions) != 3 || l.Dimensions[2] != "nice" {
		t.Fatalf("lookup = %+v", l)
	}
	l, err = ParseLookup("sum -30m absolute percentage of out")
	if err != nil || !l.AbsValue || !l.Percentage || l.Method != tsdb.GroupSum {
		t.Fatalf("lookup = %+v %v", l, err)
	}
	if _, err := ParseLookup("bogus -1m"); err == nil {
		t.Fatal("bogus method accepted")
	}
	d, err := ParseDelay("up 30s down 15m multiplier 1.5 max 1h")
	if err != nil || d.Up != 30*time.Second || d.Down != 15*time.Minute || d.Multiplier != 1.5 || d.Max != time.Hour {
		t.Fatalf("delay = %+v %v", d, err)
	}
	r, err := ParseRepeat("warning 30m critical 10m")
	if err != nil || r.Warning != 30*time.Minute || r.Critical != 10*time.Minute {
		t.Fatalf("repeat = %+v %v", r, err)
	}
	if d, _ := parseDuration("2d"); d != 48*time.Hour {
		t.Fatalf("2d = %v", d)
	}
	if d, _ := parseDuration("90"); d != 90*time.Second {
		t.Fatalf("90 = %v", d)
	}
}

func TestDefaultRulesCompile(t *testing.T) {
	rules, err := DefaultRules()
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) < 10 {
		t.Fatalf("only %d builtin rules", len(rules))
	}
	override, _ := ParseRules([]byte(`
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -1m percentage of used
    warn: '$this > 50'
`), "test")
	merged := Merge(rules, override)
	if len(merged) != len(rules) {
		t.Fatalf("merge changed count: %d vs %d", len(merged), len(rules))
	}
	for _, r := range merged {
		if r.Spec.Name == "ram_in_use" && r.Spec.Warn != "$this > 50" {
			t.Fatalf("override not applied: %+v", r.Spec)
		}
	}
}

type memNotifier struct {
	mu   sync.Mutex
	seen []LogEntry
}

func (m *memNotifier) Name() string { return "mem" }
func (m *memNotifier) Notify(_ context.Context, e LogEntry) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, e)
	return nil
}
func (m *memNotifier) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}

func newTestEngine(t *testing.T, rulesYAML string, notifiers ...Notifier) (*Engine, *registry.Registry) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Family: "ram", Units: "MiB",
		Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := ParseRules([]byte(rulesYAML), "test")
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(reg, db, Options{Rules: rules, Hostname: "h", LogDir: t.TempDir(), Notifiers: notifiers,
		Roles: map[string][]string{"sysadmin": {"mem"}}})
	if err != nil {
		t.Fatal(err)
	}
	return e, reg
}

const ramRule = `
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > (($status >= $WARNING) ? (70) : (80))'
    crit: '$this > 95'
    to: sysadmin
`

func TestEngineTransitionsAndHysteresis(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, ramRule, n)
	go func() {
		for range e.notifyCh {
		}
	}()
	now := time.Unix(1_700_000_000, 0)
	feed := func(used, free float64) {
		for i := 0; i < 10; i++ {
			_ = reg.Collect("system.ram", now, map[string]float64{"used": used, "free": free})
			now = now.Add(time.Second)
		}
		e.Tick(now)
	}
	feed(10, 90) // 10% → CLEAR
	al := e.Alarms()
	if len(al) != 1 || al[0].Status != StatusClear || math.Abs(al[0].Value-10) > 0.01 {
		t.Fatalf("alarms = %+v", al)
	}
	feed(85, 15) // 85% → WARNING
	if s := e.Alarms()[0].Status; s != StatusWarning {
		t.Fatalf("status = %v, want WARNING", s)
	}
	feed(75, 25) // 75%: above the lowered 70 threshold → stays WARNING (hysteresis)
	if s := e.Alarms()[0].Status; s != StatusWarning {
		t.Fatalf("status = %v, want WARNING (hysteresis)", s)
	}
	feed(60, 40) // → CLEAR
	if s := e.Alarms()[0].Status; s != StatusClear {
		t.Fatalf("status = %v, want CLEAR", s)
	}
	feed(99, 1) // → CRITICAL
	if s := e.Alarms()[0].Status; s != StatusCritical {
		t.Fatalf("status = %v, want CRITICAL", s)
	}
	log := e.Log(0)
	// UNINIT→CLEAR, CLEAR→WARNING, WARNING→CLEAR, CLEAR→CRITICAL
	if len(log) != 4 || log[1].Status != StatusWarning || log[1].OldStatus != StatusClear || log[3].Status != StatusCritical {
		for _, l := range log {
			t.Logf("%d %s→%s %.1f", l.When, l.OldStatus, l.Status, l.Value)
		}
		t.Fatalf("log has %d entries", len(log))
	}
	if log[0].Notified {
		t.Fatal("initial CLEAR must not notify")
	}
	for _, l := range log[1:] {
		if !l.Notified {
			t.Fatalf("entry %s→%s not notified", l.OldStatus, l.Status)
		}
	}
	sum := e.Summary()
	if sum.Critical != 1 || sum.Normal != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if got := e.Log(log[2].UniqueID); len(got) != 1 || got[0].UniqueID != log[3].UniqueID {
		t.Fatalf("Log(after) = %+v", got)
	}
}

func TestEngineDelaySuppressesFlap(t *testing.T) {
	rule := ramRule + "    delay: down 30s\n"
	e, reg := newTestEngine(t, rule)
	sent := 0
	e.opt.Notifiers = []Notifier{&memNotifier{}}
	go func() {
		for range e.notifyCh {
			sent++
		}
	}()
	now := time.Unix(1_700_000_000, 0)
	feed := func(used float64, secs int) {
		for i := 0; i < secs; i++ {
			_ = reg.Collect("system.ram", now, map[string]float64{"used": used, "free": 100 - used})
			now = now.Add(time.Second)
			e.Tick(now)
		}
	}
	feed(10, 12) // CLEAR
	feed(90, 12) // WARNING (up delay 0 → notified immediately)
	feed(10, 12) // CLEAR, down-delay 30s → pending
	if a := e.Alarms()[0]; a.DelayUpTo == 0 || a.Status != StatusClear {
		t.Fatalf("expected pending recovery, got %+v", a)
	}
	feed(90, 12) // back to WARNING before the delay expired → recovery dropped
	feed(90, 30)
	if a := e.Alarms()[0]; a.DelayUpTo != 0 {
		t.Fatalf("pending should be cleared: %+v", a)
	}
	entries := e.Log(0)
	notified := 0
	for _, l := range entries {
		if l.Notified {
			notified++
		}
	}
	// notified: first WARNING, and the re-raise (CLEAR→WARNING). The CLEAR
	// in between must not have produced a notification.
	if notified != 2 {
		for _, l := range entries {
			t.Logf("%s→%s notified=%v delay=%d", l.OldStatus, l.Status, l.Notified, l.Delay)
		}
		t.Fatalf("notified = %d, want 2", notified)
	}
}

func TestEngineChartVariablesAndOtherAlarms(t *testing.T) {
	rules := `
alarms:
  - name: free_mib
    on: system.ram
    calc: '$free'
    every: 1s
    warn: '$this < 20'
  - name: used_ratio
    on: system.ram
    calc: '$used / ($used + $free_mib)'
    every: 1s
    warn: '$this > 0.5'
`
	e, reg := newTestEngine(t, rules)
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 70, "free": 30})
	e.Tick(now.Add(time.Second))
	al := e.Alarms()
	if len(al) != 2 || al[0].Value != 30 || al[0].Status != StatusClear || math.Abs(al[1].Value-0.7) > 1e-9 || al[1].Status != StatusWarning {
		t.Fatalf("alarms = %+v", al)
	}
}

func TestEnginePersistsLog(t *testing.T) {
	dir := t.TempDir()
	db, _ := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	defer db.Close()
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, _ := ParseRules([]byte(ramRule), "t")
	e, err := New(reg, db, Options{Rules: rules, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	e.Tick(now.Add(time.Second))
	e.logFile.Close()
	e2, err := New(reg, db, Options{Rules: rules, LogDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if got := e2.Log(0); len(got) != 1 || got[0].Status != StatusWarning {
		t.Fatalf("reloaded log = %+v", got)
	}
	if e2.nextLog != 2 {
		t.Fatalf("nextLog = %d", e2.nextLog)
	}
}

func TestWebhookAndSlackNotifiers(t *testing.T) {
	var got []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		got = append(got, m)
		if r.URL.Path == "/fail" {
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	entry := LogEntry{Name: "ram_in_use", Chart: "system.ram", Status: StatusWarning, OldStatus: StatusClear, Value: 91.5, Units: "%", Hostname: "h"}
	w := &WebhookNotifier{URL: srv.URL + "/hook", Headers: map[string]string{"X-Key": "k"}}
	if err := w.Notify(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if got[0]["name"] != "ram_in_use" || got[0]["status"] != "WARNING" {
		t.Fatalf("webhook payload = %v", got[0])
	}
	s := &SlackNotifier{WebhookURL: srv.URL + "/slack", Channel: "#ops"}
	if err := s.Notify(context.Background(), entry); err != nil {
		t.Fatal(err)
	}
	if got[1]["channel"] != "#ops" || got[1]["attachments"] == nil {
		t.Fatalf("slack payload = %v", got[1])
	}
	if err := (&WebhookNotifier{URL: srv.URL + "/fail"}).Notify(context.Background(), entry); err == nil {
		t.Fatal("HTTP 500 should be an error")
	}
	if s := Summarize(entry); !strings.Contains(s, "ram_in_use") || !strings.Contains(s, "WARNING") || !strings.Contains(s, "91.5 %") {
		t.Fatalf("summary = %q", s)
	}
}

func TestEngineNotifiesWhenStartingRaised(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, ramRule, n)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	now := time.Unix(1_700_000_000, 0)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	e.Tick(now.Add(time.Second))
	deadline := time.Now().Add(2 * time.Second)
	for n.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done
	if n.count() != 1 || n.seen[0].Status != StatusWarning || n.seen[0].OldStatus != StatusUninitialized {
		t.Fatalf("notifications = %+v", n.seen)
	}
	if e.Notified() != 1 {
		t.Fatalf("Notified() = %d", e.Notified())
	}
}
