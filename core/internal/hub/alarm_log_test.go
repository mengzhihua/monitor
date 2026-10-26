package hub

import (
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestMirroredAlarmLogDropsPrefix(t *testing.T) {
	nd := &Node{alarms: map[string]health.LogEntry{}}
	const extra = 50
	for i := 1; i <= alarmLogKeep+extra; i++ {
		nd.recordAlarm(health.LogEntry{
			Chart: "system.ram", Name: "ram", UniqueID: uint64(i), When: int64(i),
			Info: strings.Repeat("i", 24),
		})
	}
	if len(nd.alog) != alarmLogKeep || cap(nd.alog) != alarmLogKeep {
		t.Fatalf("len=%d cap=%d", len(nd.alog), cap(nd.alog))
	}
	if nd.alog[0].When != extra+1 || nd.alog[len(nd.alog)-1].When != int64(alarmLogKeep+extra) {
		t.Fatalf("window %d..%d", nd.alog[0].When, nd.alog[len(nd.alog)-1].When)
	}
}
