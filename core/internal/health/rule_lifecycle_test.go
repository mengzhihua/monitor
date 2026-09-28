package health

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRuleMatchingAndBindingDuringLabelUpdates(t *testing.T) {
	spec := ruleConfigFixture("labels")
	spec.Labels = map[string]string{"region": "prod*"}
	e := ruleConfigEngine(t, "", spec)
	chart, _ := e.reg.Chart("system.ram")
	stop, stopped, started := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		close(started)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				chart.MergeLabels(map[string]string{"region": fmt.Sprintf("prod-%d", i), "generation": fmt.Sprint(i)})
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	<-started
	for i := range 100 {
		if !MatchesRuleChart(spec, chart) {
			t.Fatal("prefix label selector stopped matching")
		}
		spec.Info = fmt.Sprint(i)
		if _, err := e.MutateRules(nil, RuleMutation{Upserts: []RuleSpec{spec}}); err != nil {
			t.Fatal(err)
		}
		e.Tick(time.Unix(1700000000+int64(i), 0))
		alarms := e.Alarms()
		if len(alarms) != 1 || !strings.HasPrefix(alarms[0].Labels["region"], "prod") {
			t.Fatalf("label snapshot missing on new alarm: %+v", alarms)
		}
		// The API serializes these returned labels while collectors merge them.
		if _, err := json.Marshal(alarms[0].Labels); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCloseWaitsForRuleMutationGate(t *testing.T) {
	e := ruleConfigEngine(t, t.TempDir(), ruleConfigFixture("configured"))
	before := e.RulesConfig()
	// This is the same gate held throughout prepare, durable write and publish.
	// Close must not finish in its middle, even when engine.mu is available.
	e.tickMu.Lock()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		close(started)
		e.Close()
		close(done)
	}()
	<-started
	closedEarly := false
	select {
	case <-done:
		closedEarly = true
	case <-time.After(30 * time.Millisecond):
	}
	e.tickMu.Unlock()
	if closedEarly {
		t.Fatal("Close passed an active rule mutation gate")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after the mutation gate was released")
	}
	mutation := RuleMutation{Upserts: []RuleSpec{ruleConfigFixture("after-close")}}
	if _, err := e.MutateRules(&before.Revision, mutation); !errors.Is(err, ErrRuleConflict) {
		t.Fatalf("closed engine accepted a mutation: %v", err)
	}
	if err := e.ValidateRuleMutation(&before.Revision, mutation); !errors.Is(err, ErrRuleConflict) {
		t.Fatalf("closed engine accepted mutation preview: %v", err)
	}
	e.Tick(time.Now())
	if !reflect.DeepEqual(e.RulesConfig(), before) || len(e.Alarms()) != 0 || len(e.Log(0)) != 0 {
		t.Fatal("closed engine published rules or evaluated alarms")
	}
}

func TestCompileRejectsWhitespaceRuleName(t *testing.T) {
	for _, name := range []string{"", " ", "\t\n", "\u3000"} {
		if _, err := Compile(ruleConfigFixture(name), "config.yaml"); err == nil {
			t.Fatalf("Compile accepted blank name %q", name)
		}
	}
}
