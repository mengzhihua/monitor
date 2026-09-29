package health

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func ruleConfigFixture(name string) RuleSpec {
	return RuleSpec{Name: name, On: "system.ram", Calc: "$used", Warn: "$this > 80", Every: "1s", Info: "configured"}
}

func ruleConfigEngine(t *testing.T, dir string, specs ...RuleSpec) *Engine {
	t.Helper()
	rules, err := CompileAll(specs, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{Hostname: "fixture", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.memory", Labels: map[string]string{"region": "prod-east"}, Dimensions: []*registry.Dimension{{ID: "used"}}})
	e, err := New(reg, nil, Options{Rules: rules, LogDir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	return e
}

func rulesByName(rules []*Rule) map[string]*Rule {
	out := make(map[string]*Rule, len(rules))
	for _, rule := range rules {
		out[rule.Spec.Name] = rule
	}
	return out
}

func TestRuleConfigPersistenceRestoreAndRestartEpoch(t *testing.T) {
	dir := t.TempDir()
	a, b := ruleConfigFixture("a"), ruleConfigFixture("b")
	e := ruleConfigEngine(t, dir, a, b)
	initial := e.RulesConfig()
	changed := a
	changed.Warn, changed.Disabled = "$this > 95", true
	saved, err := e.MutateRules(&initial.Revision, RuleMutation{Upserts: []RuleSpec{changed, ruleConfigFixture("custom")}, Delete: []string{"b"}})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Revision == initial.Revision || !saved.Persistent || !reflect.DeepEqual(saved.Removed, []string{"b"}) || !reflect.DeepEqual(saved.Overridden, []string{"a", "custom"}) {
		t.Fatalf("mutation metadata: %+v", saved)
	}
	path := filepath.Join(dir, "alert-rules.json")
	info, err := os.Stat(path)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("private rule file: %v %v", info, err)
	}
	e.Close()
	a.Info = "new configured definition"
	reopened := ruleConfigEngine(t, dir, a, b)
	after := reopened.RulesConfig()
	current := rulesByName(after.Rules)
	if len(current) != 2 || !current["a"].Spec.Disabled || current["a"].Spec.Warn != changed.Warn || current["b"] != nil || after.Revision == saved.Revision {
		t.Fatalf("restart lost overlay/tombstone or reused revision: %+v", after)
	}
	if _, err := reopened.MutateRules(&saved.Revision, RuleMutation{Restore: []string{"a"}}); !errors.Is(err, ErrRuleConflict) {
		t.Fatalf("pre-restart revision accepted: %v", err)
	}
	restored, err := reopened.MutateRules(&after.Revision, RuleMutation{Restore: []string{"a", "b"}, Delete: []string{"custom"}})
	if err != nil {
		t.Fatal(err)
	}
	current = rulesByName(restored.Rules)
	if len(current) != 2 || current["a"].Spec.Info != a.Info || current["a"].Spec.Disabled || current["a"].Source != "config.yaml" || len(restored.Removed)+len(restored.Overridden) != 0 {
		t.Fatalf("restore did not reveal current base: %+v", restored)
	}
	reopened.Close()
	again := ruleConfigEngine(t, dir, a, b).RulesConfig()
	if len(again.Rules) != 2 || len(again.Removed)+len(again.Overridden) != 0 {
		t.Fatalf("restored state did not survive restart: %+v", again)
	}
}

func TestRuleConfigValidationIsAtomicAndReadOnly(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("existing"))
	before := e.RulesConfig()
	good := RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("new")}, CreateOnly: true}
	if err := e.ValidateRuleMutation(&before.Revision, good); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "alert-rules.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("validation wrote a file: %v", err)
	}
	invalid := ruleConfigFixture("invalid")
	invalid.Warn = "$this > ("
	blank := ruleConfigFixture(" \t\n")
	oversized := ruleConfigFixture("oversized")
	oversized.Info = strings.Repeat("x", maxRuleConfigBytes)
	for _, tc := range []struct {
		name     string
		mutation RuleMutation
		want     error
	}{
		{"create conflict after valid entry", RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("new"), ruleConfigFixture("existing")}, CreateOnly: true}, ErrRuleConflict},
		{"invalid after valid entry", RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("new"), invalid}}, ErrRuleInvalid},
		{"blank name", RuleMutation{Upserts: []RuleSpec{blank}}, ErrRuleInvalid},
		{"duplicate batch name", RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("new"), ruleConfigFixture("new")}}, ErrRuleInvalid},
		{"mixed duplicate", RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("existing")}, Delete: []string{"existing"}}, ErrRuleInvalid},
		{"missing delete", RuleMutation{Delete: []string{"existing", "unknown"}}, ErrRuleNotFound},
		{"missing restore", RuleMutation{Restore: []string{"unknown"}}, ErrRuleNotFound},
		{"unmodified base restore", RuleMutation{Restore: []string{"existing"}}, ErrRuleInvalid},
		{"oversized file", RuleMutation{Upserts: []RuleSpec{oversized}}, ErrRuleCapacity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := e.ValidateRuleMutation(&before.Revision, tc.mutation); !errors.Is(err, tc.want) {
				t.Fatalf("preview error=%v want=%v", err, tc.want)
			}
			if _, err := e.MutateRules(&before.Revision, tc.mutation); !errors.Is(err, tc.want) {
				t.Fatalf("commit error=%v want=%v", err, tc.want)
			}
			if got := e.RulesConfig(); !reflect.DeepEqual(got, before) || len(e.Alarms()) != 0 || len(e.Log(0)) != 0 {
				t.Fatal("validation or rejected batch changed engine state")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(dir, "alert-rules.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected batch wrote a file: %v", err)
	}
}

func TestRuleConfigRestoreOrphanedTombstone(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("removed-from-config"))
	if _, err := e.MutateRules(nil, RuleMutation{Delete: []string{"removed-from-config"}}); err != nil {
		t.Fatal(err)
	}
	e.Close()
	reopened := ruleConfigEngine(t, dir)
	before := reopened.RulesConfig()
	if len(before.Base)+len(before.Rules) != 0 || !reflect.DeepEqual(before.Removed, []string{"removed-from-config"}) {
		t.Fatalf("orphaned tombstone was not retained: %+v", before)
	}
	mutation := RuleMutation{Restore: []string{"removed-from-config"}}
	if err := reopened.ValidateRuleMutation(&before.Revision, mutation); err != nil {
		t.Fatalf("orphan restore preview: %v", err)
	}
	if got := reopened.RulesConfig(); !reflect.DeepEqual(got, before) {
		t.Fatal("restore preview changed the tombstone")
	}
	after, err := reopened.MutateRules(&before.Revision, mutation)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision == before.Revision || len(after.Removed)+len(after.Base)+len(after.Rules) != 0 {
		t.Fatalf("orphan restore did not only clear the tombstone: %+v", after)
	}
	if _, err := reopened.MutateRules(&after.Revision, mutation); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("restore of unknown name: %v", err)
	}
	reopened.Close()
	// A later reintroduced configuration definition must not stay suppressed.
	if got := ruleConfigEngine(t, dir, ruleConfigFixture("removed-from-config")).RulesConfig(); len(got.Rules) != 1 || len(got.Removed) != 0 {
		t.Fatalf("orphan cleanup did not survive restart: %+v", got)
	}
}

func TestRuleConfigPersistenceFailureDoesNotPublish(t *testing.T) {
	dir := t.TempDir()
	e := ruleConfigEngine(t, dir, ruleConfigFixture("base"))
	before, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("saved")}})
	if err != nil {
		t.Fatal(err)
	}
	path := e.ruleConfig.path
	disk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(dir, "blocked")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	e.ruleConfig.path = blocked
	if _, err := e.MutateRules(&before.Revision, RuleMutation{Delete: []string{"base"}, Upserts: []RuleSpec{ruleConfigFixture("not-saved")}}); err == nil {
		t.Fatal("expected rename failure")
	}
	e.ruleConfig.path = path
	if !reflect.DeepEqual(e.RulesConfig(), before) {
		t.Fatal("failed write changed live rules or revision")
	}
	if after, err := os.ReadFile(path); err != nil || !bytes.Equal(after, disk) {
		t.Fatalf("failed write changed prior file: %v", err)
	}
	if leftovers, _ := filepath.Glob(filepath.Join(dir, ".alert-rules-*")); len(leftovers) != 0 {
		t.Fatalf("failed write left temporary files: %v", leftovers)
	}
}

func TestRuleConfigConcurrentCASAndSnapshotIsolation(t *testing.T) {
	spec := ruleConfigFixture("base")
	spec.Labels = map[string]string{"region": "prod*"}
	spec.Lookup = "average -1m of used"
	e := ruleConfigEngine(t, "", spec)
	initial := e.RulesConfig()
	initial.Rules[0].Spec.Labels["region"] = "corrupt"
	initial.Rules[0].Lookup.Dimensions[0] = "corrupt"
	initial.Rules[0].Calc.Vars()[0] = "corrupt"
	initial.Base[0].Warn.Vars()[0] = "corrupt"
	initial.Base[0].Spec.Info = "corrupt"
	spec.Labels["region"] = "caller mutation"
	copy := e.RulesConfig()
	if copy.Rules[0].Spec.Labels["region"] != "prod*" || copy.Rules[0].Lookup.Dimensions[0] != "used" || copy.Base[0].Spec.Info != "configured" || copy.Rules[0].Calc.Vars()[0] != "used" || copy.Base[0].Warn.Vars()[0] != "this" {
		t.Fatal("snapshot or caller spec aliases the base rule")
	}
	const writers = 12
	start := make(chan struct{})
	results := make(chan error, writers)
	for i := range writers {
		go func() {
			<-start
			_, err := e.MutateRules(&copy.Revision, RuleMutation{Upserts: []RuleSpec{ruleConfigFixture(fmt.Sprintf("writer-%d", i))}})
			_ = e.RulesConfig()
			results <- err
		}()
	}
	close(start)
	winners := 0
	for range writers {
		if err := <-results; err == nil {
			winners++
		} else if !errors.Is(err, ErrRuleConflict) {
			t.Fatal(err)
		}
	}
	if winners != 1 || len(e.RulesConfig().Rules) != 2 || e.RulesConfig().Persistent {
		t.Fatalf("CAS winners=%d snapshot=%+v", winners, e.RulesConfig())
	}
}

func TestRuleConfigCapacityIncludesTombstones(t *testing.T) {
	e := ruleConfigEngine(t, "", ruleConfigFixture("base"))
	specs := make([]RuleSpec, RuleOverrideLimit)
	for i := range specs {
		specs[i] = ruleConfigFixture(fmt.Sprintf("custom-%d", i))
	}
	saved, err := e.MutateRules(nil, RuleMutation{Upserts: specs})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []RuleMutation{{Upserts: []RuleSpec{ruleConfigFixture("one-more")}}, {Delete: []string{"base"}}} {
		if err := e.ValidateRuleMutation(&saved.Revision, mutation); !errors.Is(err, ErrRuleCapacity) {
			t.Fatalf("capacity preview = %v", err)
		}
		if _, err := e.MutateRules(&saved.Revision, mutation); !errors.Is(err, ErrRuleCapacity) {
			t.Fatalf("capacity mutation = %v", err)
		}
	}
	if _, err := e.MutateRules(&saved.Revision, RuleMutation{Delete: []string{"custom-0"}, Upserts: []RuleSpec{ruleConfigFixture("replacement")}}); err != nil {
		t.Fatalf("replacement at capacity failed: %v", err)
	}
}

type blockedRuleLookup struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*blockedRuleLookup) Rate(string, string) (float64, bool) { return 100, true }
func (b *blockedRuleLookup) RatesBetween(string, string, int64, int64) []float64 {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return []float64{100}
}

func TestRuleMutationWaitsForOldEvaluationAndPreservesOtherAlarms(t *testing.T) {
	other, blocked := ruleConfigFixture("other"), ruleConfigFixture("blocked")
	other.Calc = "100"
	blocked.Calc = ""
	blocked.Lookup = "average -1m anomaly-bit of used"
	e := ruleConfigEngine(t, "", other, blocked)
	e.opt.GroupWait = time.Minute
	lookup := &blockedRuleLookup{started: make(chan struct{}), release: make(chan struct{})}
	e.SetAnomaly(lookup)
	tickDone := make(chan struct{})
	now := time.Unix(1700000000, 0)
	go func() { e.Tick(now); close(tickDone) }()
	<-lookup.started
	before := e.Alarms()
	var otherID uint64
	for _, alarm := range before {
		if alarm.Name == "other" {
			otherID = alarm.ID
		}
	}
	mutationDone := make(chan error, 1)
	go func() { _, err := e.MutateRules(nil, RuleMutation{Delete: []string{"blocked"}}); mutationDone <- err }()
	select {
	case err := <-mutationDone:
		close(lookup.release)
		<-tickDone
		t.Fatalf("mutation completed while old evaluation could still emit: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(lookup.release)
	<-tickDone
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	alarms := e.Alarms()
	if len(alarms) != 1 || alarms[0].Name != "other" || alarms[0].ID != otherID {
		t.Fatalf("mutation reset unaffected alarm: %+v", alarms)
	}
	e.mu.RLock()
	for _, group := range e.groups {
		for _, entry := range group.entries {
			if entry.Name != "other" {
				t.Errorf("retired alarm retained in notification group: %+v", entry)
			}
		}
	}
	e.mu.RUnlock()
	count := len(e.Log(0))
	e.Tick(now.Add(time.Second))
	if len(e.Log(0)) != count {
		t.Fatal("old rule emitted after successful deletion")
	}
}

func TestRuleConfigCorruptFileFailsStartup(t *testing.T) {
	valid := ruleConfigState{Version: 1, Counter: 1, Overrides: []RuleSpec{ruleConfigFixture("a")}, Removed: []string{}}
	encoded, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"{", `{"version":99,"overrides":[],"removed":[]}`, string(encoded) + " garbage", `{"version":1,"counter":1,"overrides":[{"name":"bad","on":"system.ram"}],"removed":[]}`, `{"version":1,"counter":1,"overrides":[{"name":" \t\n","on":"system.ram","calc":"1"}],"removed":[]}`} {
		dir := t.TempDir()
		path := filepath.Join(dir, "alert-rules.json")
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := openRuleConfig(dir, nil, nil); !errors.Is(err, ErrRuleInvalid) {
			t.Fatalf("bad file accepted: %v", err)
		}
		if unchanged, err := os.ReadFile(path); err != nil || string(unchanged) != body {
			t.Fatal("startup silently rewrote damaged rules")
		}
	}
}

func TestMatchesRuleChartSharesBindingSelectors(t *testing.T) {
	spec := ruleConfigFixture("scope")
	spec.On = "system.memory"
	spec.Labels = map[string]string{"region": "prod*"}
	e := ruleConfigEngine(t, "", spec)
	chart, _ := e.reg.Chart("system.ram")
	if !MatchesRuleChart(spec, chart) || MatchesRuleChart(spec, nil) {
		t.Fatal("context/prefix matching changed")
	}
	e.Tick(time.Now())
	if len(e.Alarms()) != 1 {
		t.Fatal("matching preview did not bind")
	}
	spec.Labels["region"] = "elsewhere"
	if MatchesRuleChart(spec, chart) {
		t.Fatal("mismatched label passed preview")
	}
}
