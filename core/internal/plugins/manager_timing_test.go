package plugins

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"
)

func TestManagerRejectsOverflowingTiming(t *testing.T) {
	if strconv.IntSize < 64 {
		t.Skip("duration overflow needs 64-bit configuration integers")
	}
	maxSeconds := int64(math.MaxInt64) / int64(time.Second)
	for _, tc := range []struct {
		name string
		spec Spec
		host int
	}{
		{"explicit timeout", Spec{Timeout: int(maxSeconds + 1)}, 1},
		{"default timeout", Spec{UpdateEvery: int(maxSeconds/10 + 1)}, 1},
		{"interval integer overflow", Spec{UpdateEvery: int(^uint(0) >> 1)}, 1},
		{"inherited interval", Spec{}, int(maxSeconds/10 + 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Name, tc.spec.Command = "invalid", filepath.Join(t.TempDir(), "missing")
			reg := newReg(&memSink{})
			reg.Host.UpdateEvery = tc.host
			m := New(reg, []Spec{tc.spec}, Options{})
			s := statusNamed(m, "invalid")
			if s.State != StateFailed || s.Enabled || !s.Locked || s.Error == "" {
				t.Fatalf("unsafe timing not isolated: %+v", s)
			}
			runTestManager(t, m)
			before := m.Catalog().Revision
			for _, action := range []string{"enable", "restart"} {
				if _, err := m.Apply(context.Background(), ControlRequest{Name: "invalid", Action: action, IfRevision: before}); !errors.Is(err, ErrPolicy) {
					t.Fatalf("%s invalid timing = %v, want policy refusal", action, err)
				}
			}
			if m.Catalog().Revision != before {
				t.Fatal("refused action changed revision")
			}
		})
	}
}

func TestManagerTimingDefaultsAndSafeBoundaries(t *testing.T) {
	maxSeconds := int64(math.MaxInt64) / int64(time.Second)
	maxSeconds = min(maxSeconds, int64(^uint(0)>>1))
	for _, tc := range []struct{ every, timeout, wantEvery, wantTimeout int }{
		{0, 0, 1, 60}, {-1, -1, 1, 60}, {12, 0, 12, 120},
		{86401, 86401, 86401, 86401},
		{int(maxSeconds), int(maxSeconds), int(maxSeconds), int(maxSeconds)},
		{int(maxSeconds / 10), 0, int(maxSeconds / 10), int(maxSeconds/10) * 10},
	} {
		m := New(newReg(&memSink{}), []Spec{{Name: "safe", UpdateEvery: tc.every, Timeout: tc.timeout}}, Options{})
		sp := m.list[0].spec
		if sp.UpdateEvery != tc.wantEvery || sp.Timeout != tc.wantTimeout || m.list[0].locked {
			t.Fatalf("safe timing changed: input=%+v spec=%+v", tc, sp)
		}
		if timeout := time.Duration(sp.Timeout) * time.Second; timeout <= 0 || timeout/4 <= 0 {
			t.Fatalf("unsafe watchdog duration %v", timeout)
		}
	}
}

func TestManagerInvalidTimingDoesNotStartOrBlockHealthyPlugin(t *testing.T) {
	if runtime.GOOS == "windows" || strconv.IntSize < 64 {
		t.Skip("shell processes and 64-bit configuration integers")
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "unsafe-started")
	bad := writeScript(t, dir, "bad", "touch \"$2\"\nprintf 'DISABLE\\n'\n")
	good := writeScript(t, dir, "good", "printf 'CHART timing.value\\nDIMENSION v\\nBEGIN timing.value\\nSET v = 7\\nEND\\nDISABLE\\n'\n")
	sink := &memSink{}
	m := New(newReg(sink), []Spec{
		{Name: "invalid", Command: bad, Args: []string{marker}, Timeout: int(^uint(0) >> 1)},
		{Name: "healthy", Command: good},
	}, Options{})
	runTestManager(t, m)
	waitFor(t, func() bool { return statusNamed(m, "healthy").State == StateDisabled }, "healthy plugin completion")
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid worker executed: %v", err)
	}
	sink.mu.Lock()
	defer sink.mu.Unlock()
	if rows := sink.rows["timing.value|v"]; len(rows) != 1 || rows[0] != 7 {
		t.Fatalf("healthy worker lost samples: %v", rows)
	}
}
