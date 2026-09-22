package collect

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseJournalFilters(t *testing.T) {
	raw := `{"PRIORITY":"3","_SYSTEMD_UNIT":"sshd.service","_PID":"12","MESSAGE":"failed login","__CURSOR":"c1","_BOOT_ID":"bootA","__REALTIME_TIMESTAMP":"1700000000000000"}
{"PRIORITY":"6","_SYSTEMD_UNIT":"cron.service","_PID":"9","MESSAGE":"ok","__CURSOR":"c2","_BOOT_ID":"bootB","__REALTIME_TIMESTAMP":"1700000001000000"}
`
	rows := parseJournalJSON(raw, LogQuery{Limit: 10, Unit: "sshd", Priority: "err"})
	if len(rows) != 1 || rows[0].Cursor != "c1" || rows[0].Boot != "bootA" || rows[0].PID != "12" {
		t.Fatalf("%+v", rows)
	}
	if got := parseJournalJSON(raw, LogQuery{Limit: 10, Boot: "bootB"}); len(got) != 1 || got[0].Unit != "cron.service" {
		t.Fatalf("boot filter %+v", got)
	}
	args := journalArgs(LogQuery{Limit: 5, Unit: "sshd.service", Priority: "warning", Boot: "-1", Cursor: "abc", After: 10, Query: "disk"})
	joined := strings.Join(args, " ")
	for _, want := range []string{"-u sshd.service", "-p warning", "-b -1", "--after-cursor abc", "-g disk"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("args %q missing %q", joined, want)
		}
	}
}

func TestEventXPathAndRecord(t *testing.T) {
	xp := eventXPath(LogQuery{Cursor: "100", After: 1_700_000_000})
	if !strings.Contains(xp, "EventRecordID>100") || !strings.Contains(xp, "TimeCreated") {
		t.Fatalf("xpath %s", xp)
	}
	if eventXPath(LogQuery{XPath: "*[System]"}) != "*[System]" {
		t.Fatal("explicit xpath")
	}
	raw := "Event[0]:\n  Log: System\n  Level: Error\n  Date: 2024-01-02T03:04:05.000\n  Record Id: 10\n  Description: boom\n\nEvent[1]:\n  Log: System\n  Level: Information\n  Record Id: 11\n  Description: later\n"
	rows := parseEventLogText(raw, LogQuery{Limit: 10, Cursor: "10"})
	if len(rows) != 1 || rows[0].Cursor != "11" || rows[0].Message != "later" {
		t.Fatalf("%+v", rows)
	}
}

func TestParseLogShow(t *testing.T) {
	raw := `[{"timestamp":"2024-01-02 03:04:05.000000-0800","eventMessage":"disk full","processID":42,"subsystem":"com.example.app","messageType":"Error"}]`
	rows := parseLogShow(raw, LogQuery{Limit: 10, Unit: "example", Priority: "err"})
	if len(rows) != 1 || rows[0].PID != "42" || rows[0].Priority != "err" || rows[0].Time == 0 {
		t.Fatalf("%+v", rows)
	}
	if got := parseLogShow(raw, LogQuery{Query: "nope"}); len(got) != 0 {
		t.Fatalf("filter %+v", got)
	}
}

func TestJournalFollowBuffer(t *testing.T) {
	l := &logsCollector{followOn: true, cfg: logsConfig{Top: 10}}
	l.buf = []LogRow{{Time: 1, Priority: "info", Unit: "a", Message: "one", Cursor: "s:1"}}
	rows := l.journalRows(LogQuery{Limit: 10})
	if len(rows) != 1 || rows[0].Message != "one" {
		t.Fatalf("%+v", rows)
	}
	if got := l.journalRows(LogQuery{Limit: 10, Unit: "missing"}); got != nil && len(got) != 0 {
		// unit filter falls through to journalctl; on hosts without it the result is nil
		for _, r := range got {
			if r.Unit == "a" {
				t.Fatalf("unfiltered row leaked %+v", got)
			}
		}
	}
}

func TestProcNetInode(t *testing.T) {
	tcp := "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n" +
		"   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12345 1 0000000000000000 100 0 0 10 0\n" +
		"   1: bad\n"
	inodes := procNetInodes(func(name string) ([]byte, error) {
		if name == "tcp" {
			return []byte(tcp), nil
		}
		return nil, osErr
	})
	if inodes["127.0.0.1:8080|0.0.0.0:0"] != "12345" {
		t.Fatalf("%v", inodes)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

var osErr = errString("missing")

func TestMacosCollectorFixture(t *testing.T) {
	m := &macosCollector{
		cfg: macosConfig{Command: "sysctl", Timeout: time.Second},
		run: func(_ context.Context, name string, _ ...string) ([]byte, error) {
			switch name {
			case "sysctl":
				return []byte("kern.memorystatus_vm_pressure_level: 1\nvm.swapusage: total = 1024.00M  used = 256.00M  free = 768.00M\nmachdep.xcpm.cpu_thermal_level: 3\n"), nil
			case "memory_pressure":
				return []byte("System-wide memory free percentage: 40%\n"), nil
			case "pmset":
				return []byte(" -InternalBattery-0 (id=1)\t81%; charged; 0:00 remaining present: true\n"), nil
			default:
				return nil, osErr
			}
		},
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("macos.memory_pressure")
	if !ok {
		t.Fatal("missing pressure")
	}
	_, vals := ch.LastValues()
	if vals["pressure"] != 60 {
		t.Fatalf("pressure %v", vals)
	}
	swap, _ := reg.Chart("macos.swap")
	_, sv := swap.LastValues()
	if sv["used"] != 256 || sv["free"] != 768 {
		t.Fatalf("swap %v", sv)
	}
	th, _ := reg.Chart("macos.thermal_level")
	_, tv := th.LastValues()
	if tv["level"] != 3 {
		t.Fatalf("thermal %v", tv)
	}
	bat, _ := reg.Chart("macos.battery")
	_, bv := bat.LastValues()
	if bv["charge"] != 81 {
		t.Fatalf("battery %v", bv)
	}
}

func TestSystemdUnitStatesFixture(t *testing.T) {
	root := t.TempDir()
	s := &systemdCollector{
		cfg: systemdConfig{CgroupRoot: root, Command: "systemctl", Max: 50},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Id=sshd.service\nActiveState=active\nSubState=running\nResult=success\nNRestarts=2\nUnitFileState=enabled\n\nId=broken.service\nActiveState=failed\nSubState=failed\nResult=exit-code\nNRestarts=4\nUnitFileState=enabled\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("systemd.service_units")
	if !ok {
		t.Fatal("missing service_units")
	}
	_, vals := ch.LastValues()
	if vals["active"] != 1 || vals["failed"] != 1 {
		t.Fatalf("%v", vals)
	}
	re, _ := reg.Chart("systemd.service_restarts")
	_, rv := re.LastValues()
	if rv["restarts"] != 6 {
		t.Fatalf("restarts %v", rv)
	}
	tab := s.services(map[string]string{"sort": "restarts"})
	if tab.Total != 2 || tab.Columns[1] != "active_state" {
		t.Fatalf("%+v", tab)
	}
	if _, err := os.Stat(filepath.Join(root, "missing")); err == nil {
		t.Fatal("unexpected file")
	}
}
