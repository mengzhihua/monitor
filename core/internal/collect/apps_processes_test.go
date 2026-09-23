package collect

import "testing"

func TestAppsPIDReuseDropsPreviousBaselines(t *testing.T) {
	previous := &pidState{
		name: "previous owner", startedAt: 1000,
		group: &appGroup{name: "old group"},
		cpuMs: 5000, hasCPU: true, readB: 100, writeB: 200, ioOK: true,
	}
	a := &appsCollector{pids: map[int32]*pidState{42: previous}}
	if got := a.previousPID(42, 1000); got != previous {
		t.Fatal("same process lost its CPU/IO baselines")
	}
	if got := a.previousPID(42, 2000); got != nil {
		t.Fatal("reused PID kept the previous owner's identity or CPU/IO counters")
	}
	if got := a.previousPID(42, 0); got != previous {
		t.Fatal("platform without a start identity changed existing behavior")
	}
	if got := a.previousPID(99, 3000); got != nil {
		t.Fatal("new process inherited an unrelated state")
	}
}
