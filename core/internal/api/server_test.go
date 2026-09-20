package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newTestServer(t *testing.T, opt Options) (*httptest.Server, *registry.Registry) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", OS: "linux", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Title: "RAM", Units: "MiB",
		Type: registry.Stacked, Priority: 200, Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	sched := collect.NewScheduler(reg, opt.Logger, []string{"none"}, nil)
	if opt.StartedAt.IsZero() {
		opt.StartedAt = time.Now()
	}
	srv, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, reg
}

func getJSON(t *testing.T, url string, out any) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil && resp.StatusCode == 200 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("%s: decode: %v", url, err)
		}
	}
	return resp
}

func TestInfoChartsData(t *testing.T) {
	ts, reg := newTestServer(t, Options{Version: "test"})
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 10; i++ {
		_ = reg.Collect("system.ram", now.Add(time.Duration(i-9)*time.Second), map[string]float64{"used": float64(100 + i), "free": 50})
	}

	var info struct {
		Version     string `json:"version"`
		ChartsCount int    `json:"charts_count"`
		Host        struct{ Hostname string }
	}
	getJSON(t, ts.URL+"/api/v1/info", &info)
	if info.Version != "test" || info.ChartsCount != 1 || info.Host.Hostname != "test" {
		t.Fatalf("info = %+v", info)
	}

	var charts struct {
		Charts map[string]struct {
			Dimensions []struct {
				ID string `json:"id"`
			} `json:"dimensions"`
			FirstEntry int64 `json:"first_entry"`
		} `json:"charts"`
	}
	getJSON(t, ts.URL+"/api/v1/charts", &charts)
	c, ok := charts.Charts["system.ram"]
	if !ok || len(c.Dimensions) != 2 || c.FirstEntry == 0 {
		t.Fatalf("charts = %+v", charts)
	}

	var data struct {
		DimensionIDs []string `json:"dimension_ids"`
		Points       int      `json:"points"`
		Result       struct {
			Labels []string     `json:"labels"`
			Data   [][]*float64 `json:"data"`
		} `json:"result"`
	}
	after, before := now.Unix()-10, now.Unix()
	getJSON(t, ts.URL+fmt.Sprintf("/api/v1/data?chart=system.ram&after=%d&before=%d", after, before), &data)
	if len(data.DimensionIDs) != 2 || data.Points != 10 || len(data.Result.Labels) != 3 {
		t.Fatalf("data = %+v", data)
	}
	last := data.Result.Data[len(data.Result.Data)-1]
	if last[1] == nil || *last[1] != 109 || last[2] == nil || *last[2] != 50 {
		t.Fatalf("last row = %v", last)
	}

	// dimension filter + grouping
	getJSON(t, ts.URL+fmt.Sprintf("/api/v1/data?chart=system.ram&after=%d&before=%d&points=2&group=max&dimensions=used", after, before), &data)
	// grid alignment may add one bucket in front
	if len(data.DimensionIDs) != 1 || data.DimensionIDs[0] != "used" || data.Points < 2 || data.Points > 3 {
		t.Fatalf("filtered data = %+v", data)
	}

	if resp := getJSON(t, ts.URL+"/api/v1/data?chart=missing", nil); resp.StatusCode != 404 {
		t.Fatalf("missing chart status = %d", resp.StatusCode)
	}

	resp, _ := http.Get(ts.URL + "/api/v1/allmetrics?format=prometheus")
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if !strings.Contains(sb.String(), `monitor_system_ram{chart="system.ram",family="ram",dimension="used",instance="test"} 109`) {
		t.Fatalf("prometheus output:\n%s", sb.String())
	}

	if resp := getJSON(t, ts.URL+"/", nil); resp.StatusCode != 200 {
		t.Fatalf("ui status = %d", resp.StatusCode)
	}
}

func TestTokenAndAllowFrom(t *testing.T) {
	ts, _ := newTestServer(t, Options{Token: "secret"})
	if resp := getJSON(t, ts.URL+"/api/v1/info", nil); resp.StatusCode != 401 {
		t.Fatalf("no token status = %d", resp.StatusCode)
	}
	if resp := getJSON(t, ts.URL+"/api/v1/info?token=secret", nil); resp.StatusCode != 200 {
		t.Fatalf("token status = %d", resp.StatusCode)
	}
	if resp := getJSON(t, ts.URL+"/", nil); resp.StatusCode != 200 {
		t.Fatalf("ui should not need a token, status = %d", resp.StatusCode)
	}

	ts2, _ := newTestServer(t, Options{AllowFrom: []string{"10.0.0.0/8"}})
	if resp := getJSON(t, ts2.URL+"/api/v1/info", nil); resp.StatusCode != 403 {
		t.Fatalf("allow_from status = %d", resp.StatusCode)
	}
}

func TestLiveWebSocket(t *testing.T) {
	ts, reg := newTestServer(t, Options{})
	url := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/live?charts=system.ram"
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// give the hub a moment to register the connection
	time.Sleep(50 * time.Millisecond)
	_ = reg.Collect("system.ram", time.Now(), map[string]float64{"used": 1, "free": 2})

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var msg liveMsg
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if msg.Chart != "system.ram" || msg.Values["used"] != 1 || msg.Values["free"] != 2 {
		t.Fatalf("live msg = %+v", msg)
	}
}
