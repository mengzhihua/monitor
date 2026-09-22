package collect

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestAppsCollectorGroupsAndProcessesFunction(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	decode := func(v any) error {
		c := v.(*appsConfig)
		c.Groups = map[string][]string{"tests": {"collect.test", "*.test"}, "system": {"custom-init"}}
		return nil
	}
	if err := a.Configure(decode); err != nil {
		t.Fatal(err)
	}
	if a.groups[0].name != "system" || a.groups[1].name != "tests" || len(a.groups) < 5 {
		t.Fatalf("groups = %v", a.groups)
	}
	if a.match("custom-init", "").name != "system" || a.match("systemd-journald", "").name != "system" || a.match("nothing-known", "").name != "other" {
		t.Fatal("group matching")
	}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := a.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("apps.processes")
	_, vals := ch.LastValues()
	if vals["tests"] < 1 {
		t.Fatalf("test binary not counted in its group: %v", vals)
	}
	tbl := a.processes(map[string]string{"group": "tests"})
	if tbl.Total < 1 || len(tbl.Rows) != tbl.Total {
		t.Fatalf("processes table = %+v", tbl)
	}
	found := false
	for _, r := range tbl.Rows {
		if r.(ProcessRow).PID == int32(os.Getpid()) {
			found = true
		}
	}
	if !found {
		t.Fatal("own pid missing from processes function")
	}
	fns := a.Functions()
	if len(fns) != 1 || fns[0].Name != "processes" {
		t.Fatalf("functions = %+v", fns)
	}
	if _, ok := reg.Chart("apps.processes_user"); !ok {
		t.Fatal("missing apps.processes_user")
	}
	uch, _ := reg.Chart("apps.processes_user")
	if len(uch.Dims()) == 0 {
		t.Log("user dimensions empty (username lookup failed on this host)")
	}
}
