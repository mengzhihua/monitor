package api

import (
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestRecordAutoActionWritesHandlingHistory(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	s, err := New(reg, db, sched, Options{
		Token: "secret", StartedAt: time.Now(), OperationsDir: t.TempDir(),
		Users: []User{{Name: "admin", Token: "admin", Role: RoleAdmin}},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := health.ActionCommand{
		Name: "restart-svc", AlarmID: 42, AlarmName: "svc_down", Chart: "svc.up",
		Hostname: "host-a", EpisodeStatus: health.StatusCritical, EpisodeSince: 1234,
	}
	s.RecordAutoAction(cmd, 1, 25, "failed to restart")
	id := problemID("", health.Alarm{
		ID: 42, Name: "svc_down", Chart: "svc.up",
		Status: health.StatusCritical, LastStatusChange: 1234,
	})
	r := s.operations.Get(id)
	if len(r.History) != 1 || r.History[0].Action != "auto_command" || r.History[0].Actor != "system" {
		t.Fatalf("%+v", r)
	}
	if r.Problem.Name != "svc_down" || r.Problem.Severity != "CRITICAL" {
		t.Fatalf("target %+v", r.Problem)
	}
	if r.History[0].Note == "" || r.History[0].Note[:11] != "auto action" {
		t.Fatalf("note %q", r.History[0].Note)
	}
}
