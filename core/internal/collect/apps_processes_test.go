package collect

import "testing"

func TestAppsUserLookupFallbackAndCache(t *testing.T) {
	a := &appsCollector{userCache: map[string]string{}}
	const unknown = "invalid-uid"
	if got := a.lookupUserID(unknown, ""); got != "" {
		t.Fatalf("unresolved portable user must remain unavailable: %q", got)
	}
	if _, cached := a.userCache[unknown]; cached {
		t.Fatal("transient unavailable username must not poison later PID lookups")
	}
	a.userCache[unknown] = "resolved_user"
	if got := a.lookupUserID(unknown, ""); got != "resolved_user" {
		t.Fatal("resolved username cache was not reused")
	}
	delete(a.userCache, unknown)
	if got := a.lookupUserID(unknown, "12345"); got != "12345" {
		t.Fatal("native numeric UID fallback changed")
	}
}

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
