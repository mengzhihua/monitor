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
