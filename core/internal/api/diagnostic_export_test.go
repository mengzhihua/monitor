package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func diagnosticTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	reg := registry.New(&registry.Host{ID: "local", Hostname: "diag-host", OS: "linux", Arch: "amd64", UpdateEvery: 1}, db)
	reg.AddChart(&registry.Chart{ID: "system.ram", Family: "ram", Dimensions: []*registry.Dimension{{ID: "used"}, {ID: "free"}}})
	rules, err := health.ParseRules([]byte(`alarms:
  - name: ram_high
    on: system.ram
    calc: '$used'
    every: 1s
    warn: '$this > 50'
    crit: '$this > 90'
    to: sysadmin
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	eng, err := health.New(reg, db, health.Options{
		Rules: rules, Hostname: "diag-host", LogDir: t.TempDir(),
		Roles:     map[string][]string{"sysadmin": {"webhook"}},
		Notifiers: []health.Notifier{&health.WebhookNotifier{URL: "http://127.0.0.1:9/private-hook"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(eng.Close)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	opt := Options{
		Health: eng, OperationsDir: t.TempDir(), Version: "3.0.0-test", StartedAt: time.Now().Add(-time.Hour),
		Users: []User{
			{Name: "reader", Token: "viewer", Role: RoleViewer},
			{Name: "on-call", Token: "operator", Role: RoleTroubleshooter},
			{Name: "boss", Token: "admin-token", Role: RoleAdmin},
		},
	}
	s, err := New(reg, db, sched, opt)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	now := time.Now().Truncate(time.Second)
	_ = reg.Collect("system.ram", now, map[string]float64{"used": 80, "free": 20})
	eng.Tick(now.Add(time.Second))
	return s, ts
}

func TestDiagnosticExportRBACAndRedaction(t *testing.T) {
	_, ts := diagnosticTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/operations/diagnostic/export", nil)
	req.Header.Set("Authorization", "Bearer viewer")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("viewer status=%d body=%s", resp.StatusCode, body)
	}

	// Tick uses now+1s; widen until so the UNINITIALIZED→WARNING entry is included.
	until := time.Now().Unix() + 3600
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/operations/diagnostic/export?include=ops,alarms,notify,history,ai&from=1&until="+strconv.FormatInt(until, 10), nil)
	req.Header.Set("Authorization", "Bearer operator")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("operator status=%d body=%s", resp.StatusCode, raw)
	}
	if len(raw) == 0 {
		t.Fatal("empty diagnostic body")
	}
	if cd := resp.Header.Get("Content-Disposition"); !strings.Contains(cd, "monitor-diagnostic-diag-host-") || !strings.HasSuffix(cd, `.json"`) {
		t.Fatalf("disposition=%q", cd)
	}
	if resp.Header.Get("Cache-Control") != "no-store" {
		t.Fatal(resp.Header.Get("Cache-Control"))
	}
	var bundle diagnosticExport
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if bundle.Schema != diagnosticSchema || bundle.Exporter.Name != "on-call" || bundle.Exporter.Role != RoleTroubleshooter {
		t.Fatalf("%+v", bundle.Exporter)
	}
	if bundle.Host["hostname"] != "diag-host" || bundle.Host["version"] != "3.0.0-test" {
		t.Fatalf("%v", bundle.Host)
	}
	if len(bundle.Problems) == 0 || bundle.Summary["warning"] < 1 {
		t.Fatalf("problems=%+v summary=%v", bundle.Problems, bundle.Summary)
	}
	if bundle.Notifications == nil || bundle.Notifications.Scope != "local" {
		t.Fatalf("notifications=%+v", bundle.Notifications)
	}
	secret := string(raw)
	for _, bad := range []string{"admin-token", "private-hook", `"token"`} {
		if strings.Contains(secret, bad) {
			t.Fatalf("bundle leaked %q: %s", bad, secret)
		}
	}
	if len(bundle.AlarmLog) == 0 {
		t.Fatal("expected alarm_log after first transition")
	}
	for _, e := range bundle.AlarmLog {
		if e.Name == "" {
			t.Fatal(e)
		}
		if e.OldValue != nil {
			t.Fatalf("expected null old_value for UNINITIALIZED transition, got %v", *e.OldValue)
		}
	}
	// Recipient must not be serialized on diagnostic log entries.
	if strings.Contains(secret, `"recipient"`) {
		t.Fatal("alarm log retained recipient field")
	}
}

func TestDiagnosticExportFiltersAndInvalidRange(t *testing.T) {
	_, ts := diagnosticTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/operations/diagnostic/export?from=10&until=1", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/v1/operations/diagnostic/export?include=ops&node=missing-node", nil)
	req.Header.Set("Authorization", "Bearer admin-token")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var bundle diagnosticExport
	if err := json.NewDecoder(resp.Body).Decode(&bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Nodes) != 0 || len(bundle.Problems) != 0 {
		t.Fatalf("expected empty filtered bundle, got nodes=%d problems=%d", len(bundle.Nodes), len(bundle.Problems))
	}
}
