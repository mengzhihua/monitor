package api

import (
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMetricExportsExcludeMissingLatestDimensions(t *testing.T) {
	srv, reg := newTestServer(t, Options{})
	at := time.Now().Truncate(time.Second)
	if err := reg.Collect("system.ram", at.Add(-time.Second), map[string]float64{"used": 75, "free": 25}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Collect("system.ram", at, map[string]float64{"free": 30}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/allmetrics", "/api/v3/allmetrics", "/api/v1/allmetrics?format=csv", "/api/v1/allmetrics?format=shell", "/api/v1/allmetrics?format=prometheus", "/metrics"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK {
				t.Fatalf("status=%d err=%v body=%s", resp.StatusCode, err, body)
			}
			if strings.Contains(string(body), "used") || !strings.Contains(string(body), "free") {
				t.Fatalf("stale dimension exported as fresh: %s", body)
			}
			if !strings.Contains(path, "?") && path != "/metrics" {
				var charts map[string]struct {
					LastUpdated int64                              `json:"last_updated"`
					Dimensions  map[string]struct{ Value float64 } `json:"dimensions"`
				}
				if err := json.Unmarshal(body, &charts); err != nil {
					t.Fatal(err)
				}
				if charts["system.ram"].LastUpdated != at.Unix() || charts["system.ram"].Dimensions["free"].Value != 30 {
					t.Fatalf("latest value lost: %s", body)
				}
			}
		})
	}
}

func TestMetricCSVPreservesQuotedIdentifiers(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "test"}, nil)
	chart, dimension := "custom,chart", "value,\"quoted\"\nline"
	reg.AddChart(&registry.Chart{ID: chart, Dimensions: []*registry.Dimension{{ID: dimension}}})
	if err := reg.Collect(chart, time.Unix(123, 0), map[string]float64{dimension: 1.25}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&Server{}).writeAllMetricsCSV(w, &view{reg: reg})
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || len(rows[1]) != 4 || rows[1][0] != chart || rows[1][1] != dimension || rows[1][2] != "1.25" || rows[1][3] != "123" {
		t.Fatalf("CSV fields corrupted: %#v", rows)
	}
}

func TestMetricCSVNeutralizesIdentifierFormulas(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "test"}, nil)
	chart, dimension := "=1+1", " @SUM(1,2)"
	reg.AddChart(&registry.Chart{ID: chart, Dimensions: []*registry.Dimension{{ID: dimension}}})
	if err := reg.Collect(chart, time.Unix(123, 0), map[string]float64{dimension: -1.25}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	(&Server{}).writeAllMetricsCSV(w, &view{reg: reg})
	rows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || len(rows) != 2 {
		t.Fatalf("invalid CSV: %v, %v", rows, err)
	}
	if rows[1][0] != "'"+chart || rows[1][1] != "'"+dimension || rows[1][2] != "-1.25" {
		t.Fatalf("formula identifiers were not protected, or numeric value changed: %v", rows[1])
	}
}
