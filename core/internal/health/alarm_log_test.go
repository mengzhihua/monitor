package health

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAlarmLogLoadDoesNotPinHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alarm-log.jsonl")
	var body strings.Builder
	for i := 1; i <= 300; i++ {
		fmt.Fprintf(&body, "{\"unique_id\":%d,\"alarm_id\":1,\"when\":%d,\"name\":\"n\",\"chart\":\"c\",\"info\":\"%s\"}\n", i, i, strings.Repeat("x", 32))
	}
	if err := os.WriteFile(path, []byte(body.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	e := &Engine{opt: Options{LogKeep: 5}}
	if err := e.loadLog(path); err != nil {
		t.Fatal(err)
	}
	if len(e.entries) != 5 || cap(e.entries) != 5 {
		t.Fatalf("len=%d cap=%d", len(e.entries), cap(e.entries))
	}
	if e.entries[0].When != 296 || e.entries[4].When != 300 || e.entries[0].Info == "" {
		t.Fatalf("window %+v", e.entries)
	}
}

func TestAlarmLogRecordStaysInsideKeep(t *testing.T) {
	e := &Engine{opt: Options{LogKeep: 4}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for i := 1; i <= 20; i++ {
		e.record(LogEntry{When: int64(i), Name: "n", Chart: "c", Info: strings.Repeat("y", 40), Status: StatusWarning}, false)
	}
	if len(e.entries) != 4 || cap(e.entries) != 4 {
		t.Fatalf("len=%d cap=%d", len(e.entries), cap(e.entries))
	}
	if e.entries[0].When != 17 || e.entries[3].When != 20 {
		t.Fatalf("window %d..%d", e.entries[0].When, e.entries[3].When)
	}
}
