package health

import (
	"context"
	"os"
	"testing"
)

func TestScriptNotifierPassesAlarmInEnv(t *testing.T) {
	if os.Getenv("MONITOR_SCRIPT_CHILD") == "1" {
		if os.Getenv("MONITOR_ALARM") != "disk_full" || os.Getenv("MONITOR_STATUS") != "CRITICAL" {
			os.Exit(2)
		}
		os.Exit(0)
	}
	n := &ScriptNotifier{Path: os.Args[0], Args: []string{"-test.run=TestScriptNotifierPassesAlarmInEnv"}}
	t.Setenv("MONITOR_SCRIPT_CHILD", "1")
	err := n.Notify(context.Background(), LogEntry{Name: "disk_full", Status: StatusCritical, Chart: "disk.space", Hostname: "h"})
	if err != nil {
		t.Fatal(err)
	}
}
