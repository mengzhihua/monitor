package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestDataQueryParameterCompatibility(t *testing.T) {
	const now = int64(1700001000)
	for _, tc := range []struct {
		name, raw     string
		after, before int64
		points        int
		group         tsdb.GroupFunc
	}{
		{"defaults", "", now - 600, now, 600, tsdb.GroupAverage},
		{"relative range", "before=-60&after=-120&points=12&group=max", now - 180, now - 60, 12, tsdb.GroupMax},
		{"absolute range", "after=1700000000&before=1700000060&points=0&group=mean", 1700000000, 1700000060, 60, tsdb.GroupAverage},
		{"invalid values", "before=bad&after=bad&points=bad&group=bad", now - 600, now, 600, tsdb.GroupAverage},
		{"inverted range", "before=1700000060&after=1700000070", 1699999460, 1700000060, 600, tsdb.GroupAverage},
		{"point limit", "after=-20000&points=20001", now - 20000, now, 10000, tsdb.GroupAverage},
		{"implicit point limit", "after=-20000", now - 20000, now, 10000, tsdb.GroupAverage},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := url.ParseQuery(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			got := parseDataQuery(q, now)
			if got.after != tc.after || got.before != tc.before || got.points != tc.points || got.group != tc.group {
				t.Fatalf("range=%d..%d points=%d group=%s; want %d..%d points=%d group=%s", got.after, got.before, got.points, got.group, tc.after, tc.before, tc.points, tc.group)
			}
		})
	}
}

// The HTTP aliases must share the full query pipeline, including gaps and
// trailing-bucket trimming, rather than accidentally serving the SPA fallback.
func TestDataAliasesPreserveSparseHistory(t *testing.T) {
	ts, reg := newTestServer(t, Options{})
	const start = int64(1700000000)
	for _, sample := range []struct {
		offset int64
		value  float64
	}{{1, 1.23456}, {3, 2}} {
		if err := reg.Collect("system.ram", time.Unix(start+sample.offset, 0), map[string]float64{"used": sample.value, "free": 5}); err != nil {
			t.Fatal(err)
		}
	}
	var baseline map[string]any
	for _, endpoint := range []struct {
		path string
		api  int
	}{{"/api/v1/data", 1}, {"/api/v2/data", 2}, {"/api/v2/q", 2}, {"/api/v3/data", 3}, {"/api/v3/q", 3}} {
		t.Run(endpoint.path, func(t *testing.T) {
			requestURL := fmt.Sprintf("%s%s?chart=system.ram&after=%d&before=%d&points=6&tier=0&dimensions=used", ts.URL, endpoint.path, start, start+6)
			resp, err := http.Get(requestURL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("status=%d content-type=%s", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["api"] != float64(endpoint.api) || body["points"] != float64(3) || body["min"] != 1.235 || body["max"] != float64(2) {
				t.Fatalf("metadata = %+v", body)
			}
			want := []any{[]any{float64(start + 1), 1.235}, []any{float64(start + 2), nil}, []any{float64(start + 3), float64(2)}}
			if got := body["result"].(map[string]any)["data"]; !reflect.DeepEqual(got, want) {
				t.Fatalf("rows=%v, want %v", got, want)
			}
			delete(body, "api")
			if baseline == nil {
				baseline = body
			} else if !reflect.DeepEqual(body, baseline) {
				t.Fatalf("alias changed response: %+v", body)
			}
		})
	}
}

type coverageReader struct {
	tsdb.Reader
	coverageChecks int
}

func (*coverageReader) PlanTier(int64, int64, int) int   { return 2 }
func (*coverageReader) TierEvery(tier int) (int64, bool) { return 60, tier >= 0 && tier <= 2 }
func (r *coverageReader) TierCovers(id string, tier int, after int64) bool {
	r.coverageChecks++
	return tier < 2 || id == "cpu.0|user"
}

func TestDataPlanChecksEverySeriesBeforeChoosingTier(t *testing.T) {
	dims := []dataDimension{{id: "user", charts: []*registry.Chart{{ID: "cpu.0"}, {ID: "cpu.1"}}}}
	reader := &coverageReader{}
	query := dataQuery{after: 1700000000, before: 1700003600, points: 10}
	tier, err := planDataTier(reader, query, dims)
	if err != nil || tier != 1 || reader.coverageChecks != 4 {
		t.Fatalf("auto tier=%d checks=%d err=%v", tier, reader.coverageChecks, err)
	}
	query.tier = "2"
	reader.coverageChecks = 0
	tier, err = planDataTier(reader, query, dims)
	if err != nil || tier != 2 || reader.coverageChecks != 0 {
		t.Fatalf("explicit tier=%d checks=%d err=%v", tier, reader.coverageChecks, err)
	}
	for _, invalid := range []string{"bad", "3", "-1"} {
		query.tier = invalid
		if _, err := planDataTier(reader, query, dims); err == nil || err.Error() != "unknown tier" {
			t.Fatalf("tier %q: %v", invalid, err)
		}
	}
}
