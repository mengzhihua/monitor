package plugins

import (
	"context"
	"log/slog"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type stderrCapture struct {
	mu             sync.Mutex
	lines          int
	bytes          int
	afterOversized bool
	secretPrefix   bool
}

func (c *stderrCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *stderrCapture) Handle(_ context.Context, record slog.Record) error {
	if record.Message != "plugin stderr" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines++
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "msg" {
			text := attr.Value.String()
			c.bytes += len(text)
			c.afterOversized = c.afterOversized || strings.Contains(text, "after-oversized-line")
			c.secretPrefix = c.secretPrefix || strings.Contains(text, "PRIVATE-KEY-")
		}
		return true
	})
	return nil
}
func (c *stderrCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *stderrCapture) WithGroup(string) slog.Handler      { return c }

func TestManagerBoundsStderrFlood(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process")
	}
	dir := t.TempDir()
	writeScript(t, dir, "flood.plugin", "i=0; while [ \"$i\" -lt 10000 ]; do printf 'diagnostic line %s\\n' \"$i\" >&2; i=$((i+1)); done\nprintf 'DISABLE\\n'\n")
	capture := &stderrCapture{}
	m := New(newReg(&memSink{}), []Spec{{Name: "flood", Command: filepath.Join(dir, "flood.plugin")}}, Options{Logger: slog.New(capture)})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "stderr flood drained")
	stats := m.Status()[0].Stderr
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if stats.Lines != 10000 || stats.Logged != uint64(capture.lines) || stats.Logged+stats.Suppressed != stats.Lines || stats.Bytes == 0 {
		t.Fatalf("stderr counters = %+v", stats)
	}
	if capture.lines > 100 {
		t.Fatalf("stderr flood emitted %d log records for 10000 lines; expected a bounded diagnostic burst", capture.lines)
	}
}

func TestManagerDrainsAfterOversizedStderrLine(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process")
	}
	dir := t.TempDir()
	writeScript(t, dir, "oversized.plugin", "printf '%070000d\\n' 0 >&2\nprintf 'after-oversized-line\\n' >&2\nprintf 'CHART stderr.v\\nDIMENSION v\\nBEGIN stderr.v\\nSET v = 7\\nEND\\nDISABLE\\n'\n")
	capture := &stderrCapture{}
	sink := &memSink{}
	m := New(newReg(sink), []Spec{{Name: "oversized", Command: filepath.Join(dir, "oversized.plugin")}}, Options{Logger: slog.New(capture)})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "oversized stderr process completes")
	capture.mu.Lock()
	marker, totalBytes := capture.afterOversized, capture.bytes
	capture.mu.Unlock()
	if !marker {
		t.Fatal("stderr drain stopped at the oversized line and discarded the following diagnostic")
	}
	if totalBytes > 8192 {
		t.Fatalf("oversized stderr retained %d diagnostic bytes", totalBytes)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if values := sink.rows["stderr.v|v"]; len(values) != 1 || values[0] != 7 {
		t.Fatalf("stderr interfered with protocol samples: %v", values)
	}
}

func TestManagerOversizedStderrOmitsPartialSecrets(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process")
	}
	dir := t.TempDir()
	script := writeScript(t, dir, "secret.plugin", "printf '%04080d%s\\n' 0 \"$PRIVATE_KEY\" >&2\nprintf 'DISABLE\\n'\n")
	capture := &stderrCapture{}
	m := New(newReg(&memSink{}), []Spec{{Name: "secret", Command: script, Env: []string{"PRIVATE_KEY=PRIVATE-KEY-sensitive-full-value"}}}, Options{Logger: slog.New(capture)})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "oversized private diagnostic")
	capture.mu.Lock()
	leaked := capture.secretPrefix
	capture.mu.Unlock()
	if leaked {
		t.Fatal("oversized diagnostic leaked the prefix of a configured secret")
	}
	if stats := m.Status()[0].Stderr; stats.Truncated != 1 {
		t.Fatalf("truncated diagnostics: %+v", stats)
	}
}

func TestManagerStderrCountersSurviveEnableAndRescan(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell process")
	}
	dir := t.TempDir()
	body := "i=0; while [ \"$i\" -lt 100 ]; do printf 'diagnostic\\n' >&2; i=$((i+1)); done\nprintf 'DISABLE\\n'\n"
	script := writeScript(t, dir, "again.plugin", body)
	capture := &stderrCapture{}
	m := New(newReg(&memSink{}), []Spec{{Name: "again", Command: script}}, Options{Logger: slog.New(capture)})
	runTestManager(t, m)
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "first process output")
	control(t, m, "again", "enable")
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "second process output")
	if stats := m.Status()[0].Stderr; stats.Lines != 200 {
		t.Fatalf("enable reset counters: %+v", stats)
	}
	writeScript(t, dir, "again.plugin", "# new executable generation\n"+body)
	control(t, m, "", "rescan")
	waitFor(t, func() bool { return m.Status()[0].State == StateDisabled }, "replacement process output")
	if stats := m.Status()[0].Stderr; stats.Lines != 300 || stats.Suppressed == 0 {
		t.Fatalf("rescan reset counters: %+v", stats)
	}
}

func TestStderrBudgetPersistsAcrossStreamEOF(t *testing.T) {
	capture := &stderrCapture{}
	m := New(newReg(&memSink{}), []Spec{{Name: "streams", Command: "unused"}}, Options{Logger: slog.New(capture)})
	for range 3 {
		m.drainStderr(m.list[0], strings.NewReader(strings.Repeat("diagnostic\n", 100)))
	}
	stats := m.Status()[0].Stderr
	if stats.Lines != 300 || stats.Logged > 20 || stats.Suppressed < 280 {
		t.Fatalf("EOF reset stderr rate budget: %+v", stats)
	}
}

func TestDiagnosticRedactionDoesNotReprocessMarkers(t *testing.T) {
	spec := Spec{Env: []string{"FIRST=d", "SECOND=d", "THIRD=redacted"}}
	text := redactProcessText(spec, "dddd")
	if text != "[redacted][redacted][redacted][redacted]" {
		t.Fatalf("redaction reprocessed markers: %q", text)
	}
}
