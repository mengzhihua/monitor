package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
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
	sched := collect.NewScheduler(reg, opt.Logger, collect.Options{Names: []string{"none"}})
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
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("API must not advertise wildcard CORS")
	}

	if resp := getJSON(t, ts.URL+"/", nil); resp.StatusCode != 200 {
		t.Fatalf("ui status = %d", resp.StatusCode)
	}
}

func TestPrometheusMetadataOncePerFamily(t *testing.T) {
	ts, reg := newTestServer(t, Options{})
	for i := 0; i < 3; i++ {
		id := fmt.Sprintf("cpu.cpu%d", i)
		reg.AddChart(&registry.Chart{ID: id, Context: "cpu.cpu", Family: "utilization", Title: "Core", Units: "%",
			Dimensions: []*registry.Dimension{{ID: "user"}}})
		_ = reg.Collect(id, time.Now(), map[string]float64{"user": float64(i)})
	}
	resp, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if n := strings.Count(sb.String(), "# TYPE monitor_cpu_cpu gauge"); n != 1 {
		t.Fatalf("TYPE emitted %d times:\n%s", n, sb.String())
	}
	if n := strings.Count(sb.String(), "monitor_cpu_cpu{"); n != 3 {
		t.Fatalf("samples = %d:\n%s", n, sb.String())
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
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/info", nil)
	req.Header.Set("Authorization", "Bearer secret")
	if resp, err := http.DefaultClient.Do(req); err != nil || resp.StatusCode != 200 {
		t.Fatalf("bearer status = %v %v", resp, err)
	}

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/v1/live"
	if _, resp, err := websocket.DefaultDialer.Dial(wsURL, nil); err == nil || resp == nil || resp.StatusCode != 401 {
		t.Fatalf("ws without token should be 401, got %v %v", resp, err)
	}
	d := websocket.Dialer{Subprotocols: []string{"monitor", "bearer." + base64.RawURLEncoding.EncodeToString([]byte("secret"))}}
	ws, _, err := d.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("ws with subprotocol token: %v", err)
	}
	if ws.Subprotocol() != "monitor" {
		t.Fatalf("selected subprotocol = %q", ws.Subprotocol())
	}
	ws.Close()

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

	// browsers on other sites must not be able to open the feed
	hdr := http.Header{"Origin": {"http://evil.example"}}
	if _, resp, err := websocket.DefaultDialer.Dial(url, hdr); err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin ws should be rejected, got %v %v", resp, err)
	}
	same := http.Header{"Origin": {ts.URL}}
	ws2, _, err := websocket.DefaultDialer.Dial(url, same)
	if err != nil {
		t.Fatalf("same-origin ws: %v", err)
	}
	ws2.Close()
}

func TestAlarmsAPIAndLivePush(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Context: "system.ram", Family: "ram", Units: "MiB",
		Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`
alarms:
  - name: ram_in_use
    on: system.ram
    lookup: average -10s percentage of used
    units: '%'
    every: 1s
    warn: '$this > 80'
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{Rules: rules, Hostname: "test", LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{Health: eng, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	eng.SetOnEvent(srv.PublishAlarm)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http")+"/api/v1/live?charts=none", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	time.Sleep(50 * time.Millisecond)

	now := time.Now()
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 90, "free": 10})
	eng.Tick(now.Add(time.Second))

	_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	var push struct {
		Alarm health.LogEntry `json:"alarm"`
	}
	if err := ws.ReadJSON(&push); err != nil {
		t.Fatal(err)
	}
	if push.Alarm.Name != "ram_in_use" || push.Alarm.Status != health.StatusWarning {
		t.Fatalf("ws alarm = %+v", push.Alarm)
	}

	var al struct {
		Summary health.Summary          `json:"summary"`
		Alarms  map[string]health.Alarm `json:"alarms"`
	}
	getJSON(t, ts.URL+"/api/v1/alarms", &al)
	if al.Summary.Warning != 1 || al.Alarms["system.ram.ram_in_use"].Status != health.StatusWarning {
		t.Fatalf("alarms = %+v", al)
	}
	var log []health.LogEntry
	getJSON(t, ts.URL+"/api/v1/alarm_log", &log)
	if len(log) != 1 || log[0].Status != health.StatusWarning {
		t.Fatalf("alarm_log = %+v", log)
	}
	getJSON(t, ts.URL+"/api/v1/alarm_log?after="+fmt.Sprint(log[0].UniqueID), &log)
	if len(log) != 0 {
		t.Fatalf("alarm_log after = %+v", log)
	}
	var info struct {
		Alarms health.Summary `json:"alarms"`
	}
	getJSON(t, ts.URL+"/api/v1/info", &info)
	if info.Alarms.Warning != 1 {
		t.Fatalf("info.alarms = %+v", info.Alarms)
	}
	var rs []map[string]any
	getJSON(t, ts.URL+"/api/v1/alarm_rules", &rs)
	if len(rs) != 1 {
		t.Fatalf("alarm_rules = %v", rs)
	}
}

func TestDataTierSelection(t *testing.T) {
	ts, reg := newTestServer(t, Options{Version: "test"})
	now := time.Now().Truncate(time.Hour)
	// 3 hours of per-second samples, constant 10 → every tier averages to 10
	for i := 0; i < 3*3600; i += 5 {
		_ = reg.Collect("system.ram", now.Add(time.Duration(i-3*3600)*time.Second), map[string]float64{"used": 10, "free": 90})
	}
	var data struct {
		Tier   int `json:"tier"`
		Points int `json:"points"`
		Result struct {
			Data [][]*float64 `json:"data"`
		} `json:"result"`
	}
	after, before := now.Unix()-3*3600, now.Unix()
	getJSON(t, ts.URL+fmt.Sprintf("/api/v1/data?chart=system.ram&after=%d&before=%d&points=3", after, before), &data)
	if data.Tier != 2 || data.Points < 3 {
		t.Fatalf("auto tier = %+v", data)
	}
	for _, row := range data.Result.Data {
		if row[1] == nil || *row[1] != 10 {
			t.Fatalf("tier2 row = %v", row)
		}
	}
	getJSON(t, ts.URL+fmt.Sprintf("/api/v1/data?chart=system.ram&after=%d&before=%d&points=180", after, before), &data)
	if data.Tier != 1 {
		t.Fatalf("auto tier for 1m step = %d", data.Tier)
	}
	getJSON(t, ts.URL+fmt.Sprintf("/api/v1/data?chart=system.ram&after=%d&before=%d&points=3&tier=0", after, before), &data)
	if data.Tier != 0 {
		t.Fatalf("forced tier = %d", data.Tier)
	}
	if resp := getJSON(t, ts.URL+"/api/v1/data?chart=system.ram&tier=9", nil); resp.StatusCode != 400 {
		t.Fatalf("bad tier status = %d", resp.StatusCode)
	}
	var info struct {
		DB struct {
			Tiers []tsdb.TierInfo `json:"tiers"`
		} `json:"db"`
	}
	getJSON(t, ts.URL+"/api/v1/info", &info)
	if len(info.DB.Tiers) != 3 || info.DB.Tiers[1].Every != 60 || info.DB.Tiers[2].Every != 3600 {
		t.Fatalf("info tiers = %+v", info.DB.Tiers)
	}
}

type fnCollector struct{}

func (fnCollector) Name() string                                                 { return "fn" }
func (fnCollector) Init(*registry.Registry) error                                { return nil }
func (fnCollector) Collect(context.Context, *registry.Registry, time.Time) error { return nil }
func (fnCollector) Functions() []collect.Function {
	return []collect.Function{{Name: "echo", Help: "echo args", Run: func(_ context.Context, args map[string]string) (any, error) {
		return args, nil
	}}}
}

func TestFunctionsAPI(t *testing.T) {
	collect.Register("fn", func() collect.Collector { return fnCollector{} })
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "test", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"fn"}})
	srv, err := New(reg, db, sched, Options{StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	var list []map[string]any
	getJSON(t, ts.URL+"/api/v1/functions", &list)
	if len(list) != 1 || list[0]["name"] != "echo" {
		t.Fatalf("functions = %v", list)
	}
	var res struct {
		Function string            `json:"function"`
		Result   map[string]string `json:"result"`
	}
	getJSON(t, ts.URL+"/api/v1/function?function=echo&sort=rss&token=x", &res)
	if res.Function != "echo" || res.Result["sort"] != "rss" || res.Result["token"] != "" {
		t.Fatalf("function result = %+v", res)
	}
	if resp := getJSON(t, ts.URL+"/api/v1/function?function=nope", nil); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown function status = %d", resp.StatusCode)
	}
}
