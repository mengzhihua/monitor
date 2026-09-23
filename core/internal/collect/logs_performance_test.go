package collect

import (
	"strings"
	"testing"
)

func TestParseUnifiedLogFieldFallbacks(t *testing.T) {
	record := `{"timestamp":"2024-01-02T03:04:05Z","eventMessage":"","message":"fallback message","subsystem":"","processImagePath":"/test/service","senderImagePath":"/ignored","processID":null,"processIdentifier":"123","messageType":"","type":"Error","metadata":{"unused":[1,2,3]}}`
	for _, raw := range []string{"[" + record + "]", "invalid line\n" + record + ",\n"} {
		rows := parseLogShow(raw, LogQuery{Unit: "service", Priority: "err"})
		if len(rows) != 1 || rows[0].Message != "fallback message" || rows[0].PID != "123" || rows[0].Unit != "/test/service" || rows[0].Time != 1704164645 {
			t.Fatalf("unexpected rows: %+v", rows)
		}
		if rows := parseLogShow(raw, LogQuery{After: 1704164646}); len(rows) != 0 {
			t.Fatalf("time filter returned %+v", rows)
		}
	}
	if rows := parseLogShow("["+record+", invalid]", LogQuery{}); len(rows) != 0 {
		t.Fatal("malformed JSON array must not produce a partial result")
	}
}

func BenchmarkParseUnifiedLog1000(b *testing.B) {
	// Unified logging includes structured metadata that the dashboard never uses.
	record := `{"timestamp":"2026-09-23 12:00:00.000000+0800","eventMessage":"sample event","processID":42,"subsystem":"com.example.service","messageType":"Info","senderImagePath":"/usr/lib/example","senderImageUUID":"01234567-89ab-cdef-0123-456789abcdef","processImageUUID":"01234567-89ab-cdef-0123-456789abcdef","traceID":123456,"activityIdentifier":0,"threadID":123,"formatString":"sample %s","metadata":{"unused":[1,2,3,4,5],"source":{"name":"fixture","tags":["one","two"]}}}`
	raw := "[" + strings.Repeat(record+",", 999) + record + "]"
	b.ReportAllocs()
	b.SetBytes(int64(len(raw)))
	for b.Loop() {
		if rows := parseLogShow(raw, LogQuery{}); len(rows) != 1000 {
			b.Fatal(len(rows))
		}
	}
}
