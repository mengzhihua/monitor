package health

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func planSpec() MaintenanceSpec {
	return MaintenanceSpec{Title: "RAM upgrade", Reason: "replace memory", Scope: "alarm", Chart: "system.ram", Alarm: "ram_test", DurationSeconds: 60}
}

func TestMaintenancePlanBoundariesOverlapPersistenceAndCancel(t *testing.T) {
	dir := t.TempDir()
	s, err := openMaintenancePlans(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := planSpec()
	spec.StartsAt = 1100
	p, err := s.Create(0, spec, "operator", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if s.Matches(spec.Chart, spec.Alarm, 1099) || !s.Matches(spec.Chart, spec.Alarm, 1100) || !s.Matches(spec.Chart, spec.Alarm, 1159) || s.Matches(spec.Chart, spec.Alarm, 1160) || s.Matches(spec.Chart, "another", 1110) {
		t.Fatal("start inclusive, end exclusive, exact scope required")
	}
	global := planSpec()
	global.Scope = "all"
	global.Chart = ""
	global.Alarm = ""
	global.StartsAt = 1100
	p2, err := s.Create(1, global, "admin", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(2, p.ID, "specific task canceled", "admin", 1110); err != nil {
		t.Fatal(err)
	}
	if !s.Matches(spec.Chart, spec.Alarm, 1111) {
		t.Fatal("canceling one plan removed overlapping global suppression")
	}
	before := s.Snapshot(1111)
	reopened, err := openMaintenancePlans(dir)
	if err != nil || !reflect.DeepEqual(before, reopened.Snapshot(1111)) {
		t.Fatal("restart mismatch", err)
	}
	if !reopened.Persistent() || !reopened.Matches("anything", "else", 1111) {
		t.Fatal("lost active window")
	}
	if err := reopened.Cancel(3, p2.ID, "completed early", "admin", 1112); err != nil {
		t.Fatal(err)
	}
	if reopened.Matches(spec.Chart, spec.Alarm, 1112) {
		t.Fatal("canceled plans must stop suppressing")
	}
	if err := reopened.Cancel(4, p2.ID, "again", "admin", 1113); !errors.Is(err, ErrPlanConflict) {
		t.Fatal(err)
	}
	before.Plans[0].Title = "changed snapshot"
	if reflect.DeepEqual(before, s.Snapshot(1111)) {
		t.Fatal("snapshot alias")
	}
	if info, err := os.Stat(filepath.Join(dir, "maintenance-plans.json")); err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
}

func TestMaintenancePlanValidationCapacityRetentionAndConcurrency(t *testing.T) {
	for _, modify := range []func(*MaintenanceSpec){
		func(p *MaintenanceSpec) { p.Title = "" }, func(p *MaintenanceSpec) { p.Reason = "" },
		func(p *MaintenanceSpec) { p.Scope = "all" }, func(p *MaintenanceSpec) { p.Alarm = "" },
		func(p *MaintenanceSpec) { p.StartsAt = 999 }, func(p *MaintenanceSpec) { p.StartsAt = 1000 + maxPlanAhead + 1 },
		func(p *MaintenanceSpec) { p.DurationSeconds = 0 }, func(p *MaintenanceSpec) { p.DurationSeconds = maxPlanDuration + 1 },
		func(p *MaintenanceSpec) { p.Title = "a\nb" }, func(p *MaintenanceSpec) { p.Reason = strings.Repeat("x", 2049) },
	} {
		s, _ := openMaintenancePlans("")
		p := planSpec()
		modify(&p)
		if _, err := s.Create(0, p, "admin", 1000); !errors.Is(err, ErrPlanInvalid) {
			t.Fatal(p, err)
		}
	}
	s, _ := openMaintenancePlans("")
	var wg sync.WaitGroup
	var won atomic.Int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Create(0, planSpec(), "admin", 1000)
			if err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrPlanConflict) {
				t.Error(err)
			}
			_ = s.Snapshot(1000)
			_ = s.Matches("c", "a", 1000)
		}()
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal(won.Load())
	}
	for i := 1; i < MaintenancePlanLimit; i++ {
		if _, err := s.Create(uint64(i), planSpec(), "admin", 1000); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Create(50, planSpec(), "admin", 1000); !errors.Is(err, ErrPlanCapacity) {
		t.Fatal(err)
	}
	// Expiry requires no writer/timer. Bounded history never evicts future plans.
	s, _ = openMaintenancePlans("")
	future := planSpec()
	future.StartsAt = 1000 + maxPlanAhead
	protected, _ := s.Create(0, future, "admin", 1000)
	for i := 0; i < 240; i++ {
		p := planSpec()
		p.DurationSeconds = 1
		if _, err := s.Create(uint64(i+1), p, "admin", 1100+int64(i)*2); err != nil {
			t.Fatal(err)
		}
	}
	d := s.Snapshot(2000)
	if len(d.Plans) != 200 || d.Revision != 241 || d.Plans[0].ID != protected.ID || d.Plans[0].State != "scheduled" {
		t.Fatal(len(d.Plans), d.Revision, d.Plans[0])
	}
}

func TestMaintenancePlanStorageFailureAndCorruption(t *testing.T) {
	dir := t.TempDir()
	s, _ := openMaintenancePlans(dir)
	p, err := s.Create(0, planSpec(), "admin", 1000)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(s.path)
	before := s.Snapshot(1000)
	// A rename over a directory fails even when tests run with elevated rights.
	if err := os.Rename(s.path, s.path+".saved"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.Cancel(1, p.ID, "early finish", "admin", 1001); err == nil {
		t.Fatal("expected disk failure")
	}
	if _, err := s.Create(1, planSpec(), "admin", 1001); err == nil {
		t.Fatal("expected disk failure")
	}
	if !reflect.DeepEqual(before, s.Snapshot(1000)) || !s.Matches(p.Chart, p.Alarm, 1001) {
		t.Fatal("failed write changed live suppression")
	}
	_ = os.Remove(s.path)
	if err := os.WriteFile(s.path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := openMaintenancePlans(dir); err == nil {
		t.Fatal("must reject corrupt file")
	}
	if _, err := New(nil, nil, Options{LogDir: dir}); err == nil {
		t.Fatal("engine silently lost persisted maintenance")
	}
	b, _ := os.ReadFile(s.path)
	if string(b) != "broken" {
		t.Fatal("corrupt original overwritten")
	}
	var state maintenancePlanState
	_ = json.Unmarshal(original, &state)
	state.Plans[0].Scope = "all" // must not silently broaden a malformed alarm selector
	b, _ = json.Marshal(state)
	_ = os.WriteFile(s.path, b, 0600)
	if _, err := openMaintenancePlans(dir); err == nil {
		t.Fatal("malformed scope accepted")
	}
	for _, damaged := range []string{"null", "{}", `{"version":1,"revision":1,"plans":null}`} {
		_ = os.WriteFile(s.path, []byte(damaged), 0600)
		if _, err := openMaintenancePlans(dir); err == nil {
			t.Fatal("missing file structure accepted:", damaged)
		}
		b, _ := os.ReadFile(s.path)
		if string(b) != damaged {
			t.Fatal("damaged file overwritten")
		}
	}
}

func TestMaintenancePlansSuppressNotificationsButKeepEvaluating(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, `alarms:
  - name: ram_test
    on: system.ram
    calc: '$used'
    warn: '$this > 50'
    crit: '$this > 90'
    every: 1s
`, n)
	var clock atomic.Int64
	clock.Store(1_700_000_000)
	e.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	p, err := e.plans.Create(0, planSpec(), "admin", clock.Load())
	if err != nil {
		t.Fatal(err)
	}
	feed := func(v float64) {
		now := e.now()
		_ = reg.Collect("system.ram", now, map[string]float64{"used": v, "free": 100 - v})
		e.Tick(now)
	}
	feed(80)
	if a := e.Alarms()[0]; a.Status != StatusWarning || !a.Silenced {
		t.Fatal(a)
	}
	if d := e.NotificationDiagnostics(); d.Suppressed != 1 || d.Recent[0].Reason != "planned_maintenance" || n.count() != 0 {
		t.Fatal(d)
	}
	clock.Store(p.EndsAt)
	if e.Alarms()[0].Silenced {
		t.Fatal("expired window still silences alarm")
	}
	feed(95)
	waitDelivered(t, e, 1)
	if e.Alarms()[0].Status != StatusCritical {
		t.Fatal("maintenance paused evaluation")
	}
	all := true
	e.ApplySilence(&all, "", 0, false)
	global := planSpec()
	global.Scope, global.Chart, global.Alarm = "all", "", ""
	p2, _ := e.plans.Create(1, global, "admin", clock.Load())
	if !e.InMaintenance() {
		t.Fatal("global plan not visible to legacy maintenance read")
	}
	if err := e.plans.Cancel(2, p2.ID, "done", "admin", clock.Load()); err != nil {
		t.Fatal(err)
	}
	if !e.IsSilenced("system.ram", "ram_test") {
		t.Fatal("cancel cleared unrelated global silence")
	}
}

func TestMaintenanceInitialSuppressionDoesNotDisableConfiguredRepeats(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, `alarms:
  - name: ram_test
    on: system.ram
    calc: '$used'
    warn: '$this > 50'
    every: 1s
    repeat: warning 10s
`, n)
	var clock atomic.Int64
	const base = int64(1_700_000_000)
	clock.Store(base)
	e.now = func() time.Time { return time.Unix(clock.Load(), 0) }
	spec := planSpec()
	spec.DurationSeconds = 15
	if _, err := e.plans.Create(0, spec, "admin", base); err != nil {
		t.Fatal(err)
	}
	for sec := int64(0); sec <= 20; sec++ {
		clock.Store(base + sec)
		_ = reg.Collect("system.ram", e.now(), map[string]float64{"used": 80, "free": 20})
		e.Tick(e.now())
		if sec < 20 && e.NotificationDiagnostics().Enqueued != 0 {
			t.Fatal("suppressed events replayed before the next configured repeat")
		}
		if sec == 19 && (e.NotificationDiagnostics().Suppressed != 2 || e.Alarms()[0].LastNotified != 0) {
			t.Fatal("repeat cadence or success bookkeeping incorrect")
		}
	}
	waitDelivered(t, e, 1)
	if d := e.NotificationDiagnostics(); d.Accepted != 1 || !d.Recent[0].Repeat || e.Alarms()[0].LastNotified != base+20 {
		t.Fatal(d)
	}
}

func TestFailedInitialNotificationRepeatsAtConfiguredCadence(t *testing.T) {
	var attempts atomic.Int64
	n := diagnosticNotifier{name: "webhook", call: func() error {
		if attempts.Add(1) == 1 {
			return errors.New("failed")
		}
		return nil
	}}
	e, reg := newTestEngine(t, `alarms:
  - name: ram_test
    on: system.ram
    calc: '$used'
    warn: '$this > 50'
    every: 1s
    repeat: warning 10s
`, n)
	const base = int64(1_700_000_000)
	feed := func(sec int64) {
		at := time.Unix(base+sec, 0)
		_ = reg.Collect("system.ram", at, map[string]float64{"used": 80})
		e.Tick(at)
	}
	feed(0)
	deadline := time.Now().Add(3 * time.Second)
	for e.NotificationDiagnostics().Failed != 1 {
		if time.Now().After(deadline) {
			t.Fatal("failed result missing")
		}
		time.Sleep(time.Millisecond)
	}
	for sec := int64(1); sec < 10; sec++ {
		feed(sec)
	}
	if e.NotificationDiagnostics().Enqueued != 1 || e.Alarms()[0].LastNotified != 0 {
		t.Fatal("failure retried too early or marked successful")
	}
	feed(10)
	waitDelivered(t, e, 1)
	if attempts.Load() != 2 || e.Alarms()[0].LastNotified != base+10 {
		t.Fatal(attempts.Load())
	}
}

func TestDelayedNotificationAnchorsRepeatToDispatchAndDoesNotBypassDelay(t *testing.T) {
	n := &memNotifier{}
	e, reg := newTestEngine(t, `alarms:
  - name: ram_test
    on: system.ram
    calc: '$used'
    warn: '$this > 50'
    crit: '$this > 90'
    every: 1s
    delay: up 5s
    repeat: warning 10s critical 1s
`, n)
	const base = int64(1_700_000_000)
	feed := func(sec int64, used float64) {
		at := time.Unix(base+sec, 0)
		_ = reg.Collect("system.ram", at, map[string]float64{"used": used})
		e.Tick(at)
	}
	for sec := int64(0); sec <= 5; sec++ {
		feed(sec, 80)
	}
	waitDelivered(t, e, 1)
	for sec := int64(6); sec <= 10; sec++ {
		feed(sec, 95)
	}
	if e.NotificationDiagnostics().Enqueued != 1 {
		t.Fatal("repeat bypassed pending severity delay")
	}
	feed(11, 95)
	waitDelivered(t, e, 2)
	feed(12, 95)
	waitDelivered(t, e, 3)
}
