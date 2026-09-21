package hub

import (
	"net/http"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestNodeID(t *testing.T) {
	if id := nodeID(&registry.Host{ID: "srv-01.example_com"}); id != "srv-01.example_com" {
		t.Fatalf("safe id rewritten: %s", id)
	}
	if id := nodeID(&registry.Host{Hostname: "fallback"}); id != "fallback" {
		t.Fatalf("hostname fallback: %s", id)
	}
	unsafe := nodeID(&registry.Host{ID: "has space/and:colon"})
	if len(unsafe) != 16 || unsafe == "has space/and:colon" {
		t.Fatalf("unsafe id should be hashed: %s", unsafe)
	}
	if nodeID(&registry.Host{ID: "has space/and:colon"}) != unsafe {
		t.Fatal("hash must be stable")
	}
}

func TestAuthorized(t *testing.T) {
	db, _ := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	defer db.Close()
	n, err := Open(db, t.TempDir(), Options{Keys: []string{"k1", "k2"}})
	if err != nil {
		t.Fatal(err)
	}
	mk := func(hdr, q string) *http.Request {
		r, _ := http.NewRequest("GET", "http://hub"+stream.Path+q, nil)
		if hdr != "" {
			r.Header.Set("Authorization", hdr)
		}
		return r
	}
	if !n.Authorized(mk("Bearer k2", "")) || !n.Authorized(mk("", "?api_key=k1")) {
		t.Fatal("valid keys rejected")
	}
	if n.Authorized(mk("Bearer k3", "")) || n.Authorized(mk("", "")) || n.Authorized(mk("Basic k1", "")) {
		t.Fatal("invalid credentials accepted")
	}
	empty, _ := Open(db, t.TempDir(), Options{})
	if empty.IngestEnabled() || empty.Authorized(mk("Bearer k1", "")) {
		t.Fatal("no keys configured must disable ingestion")
	}
}

func TestPersistenceAndIsolation(t *testing.T) {
	db, _ := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	defer db.Close()
	dir := t.TempDir()
	n, err := Open(db, dir, Options{Keys: []string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	def := &stream.ChartDef{ID: "system.cpu", Context: "system.cpu", Family: "cpu", Title: "CPU", Units: "%", Type: "stacked", Priority: 100, UpdateEvery: 1,
		Dimensions: []stream.DimDef{{ID: "user"}, {ID: "system"}}}
	a := n.newNode("a", registry.Host{ID: "a", Hostname: "a", UpdateEvery: 1})
	b := n.newNode("b", registry.Host{ID: "b", Hostname: "b", UpdateEvery: 1})
	n.mu.Lock()
	n.nodes["a"], n.nodes["b"] = a, b
	n.dirty = true
	n.mu.Unlock()
	a.reg.AddChart(def.ToChart())
	b.reg.AddChart(def.ToChart())
	a.functions = []stream.FunctionInfo{{Name: "processes", Timeout: 10}}
	if err := a.reg.Ingest("system.cpu", 1000, map[string]float64{"user": 10, "system": 5}); err != nil {
		t.Fatal(err)
	}
	if err := b.reg.Ingest("system.cpu", 1000, map[string]float64{"user": 20, "system": 6}); err != nil {
		t.Fatal(err)
	}
	a.recordAlarm(health.LogEntry{UniqueID: 1, AlarmID: 1, Name: "cpu", Chart: "system.cpu", Status: health.StatusCritical, When: 1000})

	// same series id, different values → prefixed storage keeps them apart
	pa, _ := a.DB().QueryTier(registry.SeriesID("system.cpu", "user"), 0, 999, 1001)
	pb, _ := b.DB().QueryTier(registry.SeriesID("system.cpu", "user"), 0, 999, 1001)
	if len(pa) != 1 || len(pb) != 1 || pa[0].Sum != 10 || pb[0].Sum != 20 {
		t.Fatalf("isolation broken: a=%+v b=%+v", pa, pb)
	}
	if _, _, ok := db.Bounds(registry.SeriesID("system.cpu", "user")); ok {
		t.Fatal("remote samples leaked into the un-prefixed local namespace")
	}
	if len(a.Alarms()) != 1 || len(b.Alarms()) != 0 {
		t.Fatal("alarms not isolated")
	}
	if err := n.Save(); err != nil {
		t.Fatal(err)
	}

	// reopen: metadata + chart definitions + functions survive, status offline
	n2, err := Open(db, dir, Options{Keys: []string{"k"}})
	if err != nil {
		t.Fatal(err)
	}
	a2, ok := n2.Get("a")
	if !ok {
		t.Fatal("node a not restored")
	}
	if _, ok := a2.Registry().Chart("system.cpu"); !ok {
		t.Fatal("chart definition not restored")
	}
	if len(a2.Functions()) != 1 || a2.Functions()[0].Name != "processes" {
		t.Fatalf("functions = %+v", a2.Functions())
	}
	if a2.Status(time.Now()) != StatusOffline {
		t.Fatalf("restored node status = %s", a2.Status(time.Now()))
	}
	info := a2.Info(time.Now())
	if info.ID != "a" || info.ChartsCount != 1 || info.Status != StatusOffline {
		t.Fatalf("info = %+v", info)
	}
	if len(n2.List()) != 2 {
		t.Fatalf("list = %d", len(n2.List()))
	}
	if err := n2.Forget("a"); err != nil {
		t.Fatal(err)
	}
	if _, ok := n2.Get("a"); ok {
		t.Fatal("forget did not remove node")
	}
	if err := n2.Forget("missing"); err == nil {
		t.Fatal("forgetting unknown node should fail")
	}
}
