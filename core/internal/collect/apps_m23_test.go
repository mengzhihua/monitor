package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM23CollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"db2", "as400", "mq", "websphere", "pandas", "go_expvar",
		"am2320", "lxc", "ecs", "containerd",
	} {
		found := false
		for _, n := range Available() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func TestDB2CollectorFixture(t *testing.T) {
	d := &db2Collector{cfg: db2Config{DSN: "SAMPLE", Command: "db2", Timeout: time.Second}}
	d.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("total 12\nactive 8\nidle 4\nwaits 2\ntimeouts 0\nescalations 0\ndeadlocks 1\nused 33.5\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("db2.connections")
	if !ok {
		t.Fatal("missing db2.connections")
	}
	_, vals := ch.LastValues()
	if vals["total"] != 12 || vals["active"] != 8 {
		t.Fatalf("%v", vals)
	}
}

func TestAS400CollectorFixture(t *testing.T) {
	a := &as400Collector{cfg: as400Config{DSN: "AS400", Command: "isql", Timeout: time.Second}}
	a.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("utilization 42\nconfigured 4\ntotal 120\nbatch 10\ninteractive 5\nactive 15\nwaiting 3\nused 71\nremote 8\nnet_total 20\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("as400.cpu_utilization")
	if !ok {
		t.Fatal("missing cpu")
	}
	_, vals := ch.LastValues()
	if vals["utilization"] != 42 {
		t.Fatalf("%v", vals)
	}
}

func TestMQParseAndCollect(t *testing.T) {
	qms := parseDspmq([]byte("QMNAME(QM1)                                           STATUS(Running)\nQMNAME(QM2) STATUS(Ended)\n"))
	if len(qms) != 2 || qms[0].Name != "QM1" || qms[1].Status != "Ended" {
		t.Fatalf("%+v", qms)
	}
	qs := parseRunmqscQueues([]byte("AMQ8450I:\n   QUEUE(APP.Q)          TYPE(QLOCAL)\n   CURDEPTH(17)          IPPROCS(1)\nQUEUE(DLQ)\n   CURDEPTH(0)\n"))
	if len(qs) != 2 || qs[0].Name != "APP.Q" || qs[0].Depth != 17 || qs[1].Name != "DLQ" {
		t.Fatalf("%+v", qs)
	}

	m := &mqCollector{cfg: mqConfig{Command: "dspmq", Runmqsc: "runmqsc", Timeout: time.Second}}
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dspmq" {
			return []byte("QMNAME(QM1) STATUS(Running)\n"), nil
		}
		return []byte("QUEUE(APP.Q)\nCURDEPTH(9)\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("mq.queue_managers"); !ok {
		t.Fatal("missing managers")
	}
	found := false
	for _, ch := range reg.Charts() {
		if strings.HasPrefix(ch.ID, "mq.queue_depth.") {
			found = true
			_, vals := ch.LastValues()
			if vals["curdepth"] != 9 {
				t.Fatalf("%s %v", ch.ID, vals)
			}
		}
	}
	if !found {
		t.Fatal("missing queue depth")
	}
}

func TestWebspherePandasExpvarHTTP(t *testing.T) {
	was := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"heap_used": 128, "heap_max": 512, "threads_active": 12, "pool": 50, "sessions": 7, "requests": 1000}`))
	}))
	defer was.Close()
	wcol := &websphereCollector{cfg: websphereConfig{URL: was.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := wcol.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := wcol.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("websphere.jvm_heap")
	if !ok {
		t.Fatal("missing heap")
	}
	_, vals := ch.LastValues()
	if vals["used"] != 128 || vals["free"] != 384 {
		t.Fatalf("%v", vals)
	}

	pd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"cpu": 11.5, "mem": 64}]`))
	}))
	defer pd.Close()
	p := &pandasCollector{cfg: pandasConfig{Timeout: time.Second, Jobs: []pandasJob{{Name: "host", URL: pd.URL, Format: "json"}}}}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_001, 0)); err != nil {
		t.Fatal(err)
	}
	pch, ok := reg.Chart("pandas.host")
	if !ok {
		t.Fatal("missing pandas.host")
	}
	_, pvals := pch.LastValues()
	if pvals["cpu"] != 11.5 || pvals["mem"] != 64 {
		t.Fatalf("%v", pvals)
	}

	ev := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"memstats":{"HeapAlloc":2048,"HeapInuse":1024,"StackInuse":512,"MSpanInuse":64,"MCacheInuse":32,"Sys":4096,"Mallocs":10,"Frees":3,"PauseNs":[100,0,200]}}`))
	}))
	defer ev.Close()
	g := &goExpvarCollector{cfg: goExpvarConfig{URL: ev.URL, Timeout: time.Second}}
	yes := true
	g.cfg.CollectMemstats = &yes
	if err := g.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := g.Collect(context.Background(), reg, time.Unix(1_700_000_002, 0)); err != nil {
		t.Fatal(err)
	}
	hch, ok := reg.Chart("expvar.memstats.heap")
	if !ok {
		t.Fatal("missing expvar heap")
	}
	_, hvals := hch.LastValues()
	if hvals["alloc"] != 2048/1024.0 { // divisor 1024 → 2 KiB displayed? LastValues is post-algorithm
		// LastValues returns post-algorithm values; absolute with divisor 1024 → 2048/1024 = 2
		if hvals["alloc"] != 2 {
			t.Fatalf("alloc=%v want 2", hvals["alloc"])
		}
	}
	lch, ok := reg.Chart("expvar.memstats.live_objects")
	if !ok {
		t.Fatal("missing live")
	}
	_, lvals := lch.LastValues()
	if lvals["live"] != 7 {
		t.Fatalf("live=%v", lvals["live"])
	}
}

func TestAM2320LXCECSContainerd(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "in_temp_input"), []byte("25125\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "in_humidityrelative_input"), []byte("43250\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	am := &am2320Collector{cfg: am2320Config{Path: dir}}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := am.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := am.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("am2320.temperature")
	if !ok {
		t.Fatal("missing temp")
	}
	_, vals := ch.LastValues()
	if vals["temperature"] != 25.125 {
		t.Fatalf("%v", vals)
	}

	lx := &lxcCollector{}
	lx.list = func(context.Context) ([]lxcCont, error) {
		return []lxcCont{{Name: "web", State: "RUNNING"}, {Name: "db", State: "STOPPED"}}, nil
	}
	if err := lx.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := lx.Collect(context.Background(), reg, time.Unix(1_700_000_001, 0)); err != nil {
		t.Fatal(err)
	}
	lch, ok := reg.Chart("lxc.containers")
	if !ok {
		t.Fatal("missing lxc")
	}
	_, lvals := lch.LastValues()
	if lvals["running"] != 1 || lvals["stopped"] != 1 {
		t.Fatalf("%v", lvals)
	}

	ecsSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "stats"):
			_, _ = w.Write([]byte(`{"cpu_stats":{"cpu_usage":{"total_usage":5000}},"memory_stats":{"usage":1048576,"limit":2097152}}`))
		default:
			_, _ = w.Write([]byte(`{"Family":"api","Containers":[{"Name":"app","KnownStatus":"RUNNING","Image":"app:1"}]}`))
		}
	}))
	defer ecsSrv.Close()
	ec := &ecsCollector{cfg: ecsConfig{URL: ecsSrv.URL, Timeout: time.Second}}
	if err := ec.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ec.Collect(context.Background(), reg, time.Unix(1_700_000_002, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("ecs.containers"); !ok {
		t.Fatal("missing ecs")
	}

	cd := &containerdCollector{cfg: containerdConfig{Command: "ctr", Namespace: "k8s.io", Timeout: time.Second}}
	cd.run = func(_ context.Context, _ string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "tasks") {
			return []byte("TASK     PID      STATUS\nc1       1        RUNNING\n"), nil
		}
		return []byte("CONTAINER    IMAGE    RUNTIME\nc1           nginx    io.containerd.runc.v2\n"), nil
	}
	if err := cd.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := cd.Collect(context.Background(), reg, time.Unix(1_700_000_003, 0)); err != nil {
		t.Fatal(err)
	}
	cch, ok := reg.Chart("containerd.containers")
	if !ok {
		t.Fatal("missing containerd")
	}
	_, cvals := cch.LastValues()
	if cvals["running"] != 1 {
		t.Fatalf("%v", cvals)
	}
}

func TestPandasCSVAndLxcLs(t *testing.T) {
	row, err := parsePandasCSV([]byte("cpu,mem\n3.5,9\n"))
	if err != nil || row["cpu"] != 3.5 || row["mem"] != 9 {
		t.Fatalf("%v %v", row, err)
	}
	cs := parseLxcLs([]byte("NAME STATE AUTOSTART\nweb RUNNING 1\ndb STOPPED 0\n"))
	if len(cs) != 2 || cs[0].Name != "web" || cs[1].State != "STOPPED" {
		t.Fatalf("%+v", cs)
	}
	ids := parseCtrContainers([]byte("CONTAINER IMAGE\nabc nginx\n"))
	if len(ids) != 1 || ids[0] != "abc" {
		t.Fatalf("%v", ids)
	}
}

func TestDB2AS400NoDSNDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := (&db2Collector{}).Init(reg); err == nil {
		t.Fatal("db2 should disable without dsn")
	}
	if err := (&as400Collector{}).Init(reg); err == nil {
		t.Fatal("as400 should disable without dsn")
	}
	if err := (&pandasCollector{}).Init(reg); err == nil {
		t.Fatal("pandas should disable without jobs")
	}
	if err := (&ecsCollector{}).Init(reg); err == nil {
		t.Fatal("ecs should disable without metadata url")
	}
}
