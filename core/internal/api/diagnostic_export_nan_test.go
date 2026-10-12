package api

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"testing"
	"time"
)

func TestDiagnosticExportAlarmLogOmitsNaN(t *testing.T) {
	s, ts := diagnosticTestServer(t)
	entries := s.opt.Health.Log(0)
	if len(entries) == 0 {
		t.Fatal("expected a transition in the engine log")
	}
	if !math.IsNaN(entries[0].OldValue) {
		t.Fatalf("fixture should start from UNINITIALIZED with NaN old value, got %v", entries[0].OldValue)
	}
	until := time.Now().Unix() + 3600
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/v1/operations/diagnostic/export?include=alarms&from=1&until=%d", ts.URL, until), nil)
	req.Header.Set("Authorization", "Bearer operator")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
	if len(raw) == 0 {
		t.Fatal("empty body: NaN old_value likely broke JSON encoding")
	}
	var bundle diagnosticExport
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.AlarmLog) == 0 {
		t.Fatal("expected alarm_log entries inside the export window")
	}
	for _, e := range bundle.AlarmLog {
		if e.Value == nil {
			t.Fatalf("expected finite value, got nil for %+v", e)
		}
		if e.OldValue != nil {
			t.Fatalf("UNINITIALIZED old_value should be null, got %v", *e.OldValue)
		}
	}
	if !json.Valid(raw) {
		t.Fatal("invalid json")
	}
}
