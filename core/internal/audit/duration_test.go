package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDurationZeroIsWritten(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	log.Append(Entry{TS: 1, User: "system", Action: "action:true", Command: "true", DurationMs: 0})
	log.Close()
	raw, err := os.ReadFile(filepath.Join(dir, "audit-log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"duration_ms":0`) {
		t.Fatalf("zero duration omitted: %s", raw)
	}
}
