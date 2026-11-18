package api

import (
	"github.com/mengzhihua/monitor/core/internal/sla"
	"testing"
)

func TestAvailabilityCSVPreservesUnknownAndEscapesFormulas(t *testing.T) {
	row := availabilityCSVRow(sla.Availability{Name: "=cmd()", Chart: "+chart", Window: 100, Known: 30, Unknown: 70})
	if len(row) != 7 || row[0] != "'=cmd()" || row[1] != "'+chart" || row[2] != "" || row[5] != "30" || row[6] != "70" {
		t.Fatalf("%v", row)
	}
	zero := 0.0
	if row := availabilityCSVRow(sla.Availability{Uptime: &zero}); row[2] != "0.00" {
		t.Fatalf("zero lost: %v", row)
	}
}
