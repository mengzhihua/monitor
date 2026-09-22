package collect

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const scQueryFixture = `
SERVICE_NAME: EventLog
DISPLAY_NAME: Windows Event Log
        TYPE               : 20  WIN32_SHARE_PROCESS
        STATE              : 4  RUNNING
                                (STOPPABLE, NOT_PAUSABLE, IGNORES_SHUTDOWN)
        WIN32_EXIT_CODE    : 0  (0x0)
        SERVICE_EXIT_CODE  : 0  (0x0)
        CHECKPOINT         : 0x0
        WAIT_HINT          : 0x0

SERVICE_NAME: wuauserv
DISPLAY_NAME: Windows Update
        TYPE               : 20  WIN32_SHARE_PROCESS
        STATE              : 1  STOPPED
        WIN32_EXIT_CODE    : 1077  (0x435)
        SERVICE_EXIT_CODE  : 0  (0x0)
        CHECKPOINT         : 0x0
        WAIT_HINT          : 0x0
`

func TestParseSCQuery(t *testing.T) {
	rows := parseSCQuery([]byte(scQueryFixture))
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[0].Name != "EventLog" || rows[0].State != "running" {
		t.Fatalf("eventlog = %+v", rows[0])
	}
	if rows[1].Name != "wuauserv" || rows[1].State != "stopped" {
		t.Fatalf("wuauserv = %+v", rows[1])
	}
}

func TestWindowsCollectorFixture(t *testing.T) {
	c := &windowsCollector{
		cfg: windowsConfig{Command: "sc", Timeout: time.Second},
		snap: func(context.Context) (windowsSnap, error) {
			return windowsSnap{Running: 80, Blocked: 2, Total: 82, Threads: 400, Handles: 12000, Ctxt: 9000, hasCtxt: true}, nil
		},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte(scQueryFixture), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "win", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"system.processes", "system.threads", "system.ctxt", "system.handles"} {
		if _, ok := reg.Chart(id); !ok {
			t.Fatalf("missing %s", id)
		}
	}
	ch, _ := reg.Chart("system.threads")
	_, v := ch.LastValues()
	if v["threads"] != 400 {
		t.Fatalf("threads = %v", v)
	}
	tab, err := c.Functions()[0].Run(context.Background(), map[string]string{"query": "event"})
	if err != nil {
		t.Fatal(err)
	}
	t0, ok := tab.(Table)
	if !ok || t0.Total != 1 {
		t.Fatalf("services table = %+v", tab)
	}
}

func TestWindowsAutoDisable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("live windows")
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&windowsCollector{}).Init(reg); err == nil {
		t.Fatal("expected disable off windows")
	}
}

func TestWindowsRegistered(t *testing.T) {
	found := false
	for _, n := range Available() {
		if n == "windows" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("windows collector not registered")
	}
}
