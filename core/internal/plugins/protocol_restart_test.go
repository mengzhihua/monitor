package plugins

import (
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParserRestartPrimesIncrementalCounters(t *testing.T) {
	for _, algorithm := range []string{"incremental", "percentage-of-incremental-row"} {
		t.Run(algorithm, func(t *testing.T) {
			sink := &memSink{}
			reg := newReg(sink)
			now := time.Unix(1_700_000_000, 0)
			clock := func() time.Time { now = now.Add(time.Second); return now }
			definition := fmt.Sprintf("CHART plugin.counter.load\nDIMENSION a '' %s\nDIMENSION b '' %s\nDIMENSION gauge\n", algorithm, algorithm)
			spec := Spec{Name: "counter", Manifest: &Manifest{ID: "counter"}}
			first := newPluginParser(spec, reg, PackageBudgets{})
			first.Now = clock
			if err := first.Run(strings.NewReader(definition + "BEGIN plugin.counter.load\nSET a = 100\nSET b = 200\nSET gauge = 1\nEND\nBEGIN plugin.counter.load\nSET a = 110\nSET b = 220\nSET gauge = 2\nEND\n")); err != nil {
				t.Fatal(err)
			}
			chart, _ := reg.Chart("plugin.counter.load")
			// The new process has unrelated counters. A larger first value must
			// prime its baseline, just as a smaller first value would.
			second := newPluginParser(spec, reg, PackageBudgets{})
			second.Now = clock
			if err := second.Run(strings.NewReader(definition + "BEGIN plugin.counter.load\nSET a = 150\nSET b = 280\nSET gauge = 3\nEND\nBEGIN plugin.counter.load\nSET a = 160\nSET b = 300\nSET gauge = 4\nEND\n")); err != nil {
				t.Fatal(err)
			}
			if current, _ := reg.Chart(chart.ID); current != chart {
				t.Fatal("restart replaced the chart instead of preserving its history")
			}
			wantA, wantB := 10.0, 20.0
			if algorithm == "percentage-of-incremental-row" {
				// The absolute gauge participates in the registry's row sum.
				wantA, wantB = 10*100.0/32, 20*100.0/32
			}
			a, b := sink.rows[chart.ID+"|a"], sink.rows[chart.ID+"|b"]
			if len(a) != 2 || len(b) != 2 || math.Abs(a[0]-wantA) > 1e-9 || math.Abs(b[0]-wantB) > 1e-9 {
				t.Fatalf("restart published counters from unrelated process generations: a=%v b=%v", a, b)
			}
			if algorithm == "percentage-of-incremental-row" {
				wantA, wantB = 10*100.0/34, 20*100.0/34
			}
			if math.Abs(a[1]-wantA) > 1e-9 || math.Abs(b[1]-wantB) > 1e-9 {
				t.Fatalf("new generation lost its second counter sample: a=%v b=%v", a, b)
			}
			if gauge := sink.rows[chart.ID+"|gauge"]; len(gauge) != 4 || gauge[2] != 3 || gauge[3] != 4 {
				t.Fatalf("restart dropped absolute samples: %v", gauge)
			}
		})
	}
}

func TestParserRedeclarationRetainsSameProcessBaseline(t *testing.T) {
	sink := &memSink{}
	now := time.Unix(1_700_000_000, 0)
	p := &Parser{Plugin: "counter", Reg: newReg(sink), Now: func() time.Time { now = now.Add(time.Second); return now }}
	for _, value := range []int{100, 110} {
		stream := fmt.Sprintf("CHART counter.load\nDIMENSION a '' incremental\nBEGIN counter.load\nSET a = %d\nEND\n", value)
		if err := p.Run(strings.NewReader(stream)); err != nil {
			t.Fatal(err)
		}
	}
	if samples := sink.rows["counter.load|a"]; len(samples) != 1 || samples[0] != 10 {
		t.Fatalf("same-process redeclaration reset a continuous counter: %v", samples)
	}
}

func TestParserRestartOnlyResetsOwnedDeclaredCounters(t *testing.T) {
	for _, test := range []struct {
		name, plugin, owner string
		reset               bool
	}{
		{name: "own chart", plugin: "counter", owner: "counter", reset: true},
		{name: "own module", plugin: "counter", owner: "counter/module", reset: true},
		{name: "another collector", plugin: "counter", owner: "counter-other"},
		{name: "unknown owner", plugin: "", owner: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			sink := &memSink{}
			reg := newReg(sink)
			reg.AddChart(&registry.Chart{ID: "counter.load", Plugin: test.owner, Dimensions: []*registry.Dimension{
				{ID: "declared", Algorithm: registry.Incremental}, {ID: "retained", Algorithm: registry.Incremental},
			}})
			at := time.Unix(1_700_000_000, 0)
			if err := reg.Collect("counter.load", at, map[string]float64{"declared": 100, "retained": 100}); err != nil {
				t.Fatal(err)
			}
			p := &Parser{Plugin: test.plugin, Reg: reg, Now: func() time.Time { return at.Add(time.Second) }}
			if err := p.Run(strings.NewReader("CHART counter.load\nDIMENSION declared '' incremental\nBEGIN counter.load\nSET declared = 150\nSET retained = 150\nEND\n")); err != nil {
				t.Fatal(err)
			}
			if got := sink.rows["counter.load|declared"]; test.reset && len(got) != 0 || !test.reset && (len(got) != 1 || got[0] != 50) {
				t.Fatalf("declared counter reset=%v samples=%v", test.reset, got)
			}
			if got := sink.rows["counter.load|retained"]; len(got) != 1 || got[0] != 50 {
				t.Fatalf("undeclared counter was reset: %v", got)
			}
		})
	}
}

func TestManagerRestartDoesNotPublishPreviousProcessDelta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process fixture")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "counter.plugin", `
if [ -f "$GENERATION_FILE" ]; then
  first=150; second=160; ending=DISABLE
else
  : > "$GENERATION_FILE"
  first=100; second=110; ending=EXIT
fi
printf "CHART counter.load\nDIMENSION value '' incremental\nBEGIN counter.load\nSET value = %s\nEND\nBEGIN counter.load\nSET value = %s\nEND\n%s\n" "$first" "$second" "$ending"
`)
	sink := &memSink{}
	m := New(newReg(sink), []Spec{{Name: "counter", Command: script, Env: []string{"GENERATION_FILE=" + filepath.Join(dir, "generation")}}},
		Options{RestartMin: time.Millisecond, RestartMax: time.Millisecond})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "second process completes")
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if samples := sink.rows["counter.load|value"]; len(samples) != 2 || samples[0] != 10 || samples[1] != 10 {
		t.Fatalf("process restart wrote a cross-generation counter delta: %v", samples)
	}
	if restarts := m.Status()[0].Restarts; restarts != 1 {
		t.Fatalf("expected one real restart, got %d", restarts)
	}
}
