package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/health"
)

func TestAlarmLogCSVNeutralizesFormulas(t *testing.T) {
	rec := httptest.NewRecorder()
	writeAlarmCSV(rec, []health.LogEntry{{
		UniqueID: 7, When: 20, Hostname: "web", Name: "=SUM(1,1)", Chart: "disk.space",
		Status: health.StatusCritical, Value: 4, Units: "%", Info: "full",
	}})
	body := rec.Body.String()
	if rec.Header().Get("Content-Type") != "text/csv" || !strings.Contains(body, "unique_id,when,hostname,name,chart,status,value,units,info") {
		t.Fatalf("header %q body %s", rec.Header().Get("Content-Type"), body)
	}
	if !strings.Contains(body, "'=SUM(1,1)") || strings.Contains(body, "\n=SUM") {
		t.Fatalf("formula was not neutralized: %s", body)
	}
	if !strings.Contains(body, "CRITICAL") || !strings.Contains(body, "disk.space") {
		t.Fatalf("%s", body)
	}
}
