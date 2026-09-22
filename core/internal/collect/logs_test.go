package collect

import "testing"

func TestParseLogcat(t *testing.T) {
	raw := `--------- beginning of main
01-02 15:04:05.123  1234  5678 I Monitor: agent started
01-02 15:04:06.000   101   101 E zygote  : failed to fork
1750000000.500  W/System  (  42): low memory
I/okhttp  (  99): hello
`
	rows := parseLogcat(raw, LogQuery{Limit: 50})
	if len(rows) < 3 {
		t.Fatalf("rows = %+v", rows)
	}
	found := map[string]bool{}
	for _, r := range rows {
		found[r.Unit] = true
		if r.Message == "" {
			t.Fatalf("empty message %+v", r)
		}
	}
	if !found["Monitor"] || !found["zygote"] {
		t.Fatalf("units = %v", found)
	}
	filtered := parseLogcat(raw, LogQuery{Limit: 50, Query: "fork"})
	if len(filtered) != 1 || filtered[0].Priority != "err" {
		t.Fatalf("filter = %+v", filtered)
	}
}

func TestParseEventLogText(t *testing.T) {
	raw := "Event[0]:\n  Log: Application\n  Source: Monitor\n  Level: Warning\n  Date: 2024-01-02T03:04:05.000\n  Description: disk full\n\nEvent[1]:\n  Log: System\n  Level: Information\n  Description: started\n"
	rows := parseEventLogText(raw, LogQuery{Limit: 10})
	if len(rows) != 2 || rows[0].Unit != "Application" || rows[0].Priority != "warning" || rows[0].Message != "disk full" {
		t.Fatalf("%+v", rows)
	}
	filtered := parseEventLogText(raw, LogQuery{Limit: 10, Query: "disk"})
	if len(filtered) != 1 {
		t.Fatalf("filter = %+v", filtered)
	}
}
