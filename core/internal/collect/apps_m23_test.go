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
		return []byte("total 12\nactive 8\nidle 4\nwaits 2\ntimeouts 0\nescalations 0\ndeadlocks 1\nused 33.5\nhits 90\nmisses 10\nlog_used 1024\nlog_avail 2048\n"), nil
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
	lch, ok := reg.Chart("db2.log_utilization")
	if !ok {
		t.Fatal("missing db2.log_utilization")
	}
	_, lvals := lch.LastValues()
	if lvals["utilization"] != 33.5 {
		t.Fatalf("utilization=%v", lvals)
	}
	bch, ok := reg.Chart("db2.bufferpool_hit_ratio")
	if !ok {
		t.Fatal("missing bufferpool")
	}
	_, bvals := bch.LastValues()
	if bvals["hits"] != 90 || bvals["misses"] != 10 {
		t.Fatalf("hit ratio %v", bvals)
	}
	sch, ok := reg.Chart("db2.log_space")
	if !ok {
		t.Fatal("missing log_space")
	}
	_, svals := sch.LastValues()
	if svals["used"] != 1024 || svals["available"] != 2048 {
		t.Fatalf("log_space %v", svals)
	}
}

func TestAS400CollectorFixture(t *testing.T) {
	a := &as400Collector{cfg: as400Config{DSN: "AS400", Command: "isql", Timeout: time.Second}}
	a.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("utilization 42\nconfigured 4\ntotal 120\nbatch 10\ninteractive 5\nactive 15\nwaiting 3\nused 71\nremote 8\nnet_total 20\npool_machine 100\npool_base 200\npool_interactive 50\npool_spool 25\ntemp_current 12\ntemp_maximum 64\n"), nil
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
	mch, ok := reg.Chart("as400.memory_pool_usage")
	if !ok {
		t.Fatal("missing memory pool")
	}
	_, mvals := mch.LastValues()
	if mvals["machine"] != 100 || mvals["base"] != 200 || mvals["interactive"] != 50 || mvals["spool"] != 25 {
		t.Fatalf("pools %v", mvals)
	}
	tch, ok := reg.Chart("as400.temporary_storage")
	if !ok {
		t.Fatal("missing temp storage")
	}
	_, tvals := tch.LastValues()
	if tvals["current"] != 12 || tvals["maximum"] != 64 {
		t.Fatalf("temp %v", tvals)
	}
}

func TestParseMemoryPools(t *testing.T) {
	got := parseMemoryPools([]byte("*MACHINE 100\n*BASE 200\n*INTERACT 50\n*SPOOL 25\ninteractive 5\n"))
	if got["pool_machine"] != 100 || got["pool_base"] != 200 || got["pool_interactive"] != 50 || got["pool_spool"] != 25 {
		t.Fatalf("%v", got)
	}
	if strings.Contains(as400SQL, "MACHINE_POOL") || strings.Contains(as400SQL, "BASE_POOL") {
		t.Fatal("SYSTEM_STATUS SQL must not select non-existent pool columns")
	}
	if !strings.Contains(as400PoolSQL, "MEMORY_POOL_INFO") {
		t.Fatal("pool SQL should query MEMORY_POOL_INFO")
	}
}

func TestMQParseAndCollect(t *testing.T) {
	qms := parseDspmq([]byte("QMNAME(QM1)                                           STATUS(Running)\nQMNAME(QM2) STATUS(Ended)\n"))
	if len(qms) != 2 || qms[0].Name != "QM1" || qms[1].Status != "Ended" {
		t.Fatalf("%+v", qms)
	}
	qs := parseRunmqscQueues([]byte("AMQ8450I:\n   QUEUE(APP.Q)          TYPE(QLOCAL)\n   CURDEPTH(17)          IPPROCS(1) OPPROCS(2)\nQUEUE(DLQ)\n   CURDEPTH(0)\nAMQ8409:\n   QUEUE(APP.Q) TYPE(QLOCAL)\n   MAXDEPTH(5000)\nQUEUE(DLQ) MAXDEPTH(1000)\n"))
	if len(qs) != 2 || qs[0].Name != "APP.Q" || qs[0].Depth != 17 || qs[0].MaxDepth != 5000 || qs[0].IPProcs != 1 || qs[0].OPProcs != 2 || qs[1].Name != "DLQ" || qs[1].MaxDepth != 1000 {
		t.Fatalf("%+v", qs)
	}

	m := &mqCollector{cfg: mqConfig{Command: "dspmq", Runmqsc: "runmqsc", Timeout: time.Second}}
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dspmq" {
			return []byte("QMNAME(QM1) STATUS(Running)\n"), nil
		}
		return []byte("QUEUE(APP.Q)\nCURDEPTH(9)\nMAXDEPTH(100)\nIPPROCS(2)\nOPPROCS(1)\nMSGIN(5)\nMSGOUT(3)\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	qch, ok := reg.Chart("mq.qmgr.status")
	if !ok {
		t.Fatal("missing mq.qmgr.status")
	}
	_, qvals := qch.LastValues()
	if qvals["status"] != 1 {
		t.Fatalf("qmgr status %v", qvals)
	}
	found := false
	for _, ch := range reg.Charts() {
		if strings.HasPrefix(ch.ID, "mq.queue.depth.") && ch.Context == "mq.queue.depth" {
			found = true
			_, vals := ch.LastValues()
			if vals["current"] != 9 || vals["max"] != 100 {
				t.Fatalf("%s %v", ch.ID, vals)
			}
		}
	}
	if !found {
		t.Fatal("missing queue depth")
	}
	sid := sanitizeID("APP.Q")
	cch, ok := reg.Chart("mq.queue.connections." + sid)
	if !ok {
		t.Fatal("missing connections")
	}
	_, cvals := cch.LastValues()
	if cvals["input"] != 2 || cvals["output"] != 1 {
		t.Fatalf("connections %v", cvals)
	}
	pch, ok := reg.Chart("mq.queue.depth_percentage." + sid)
	if !ok {
		t.Fatal("missing depth %")
	}
	_, pvals := pch.LastValues()
	if pvals["percentage"] != 9 {
		t.Fatalf("pct %v", pvals)
	}
	if strings.Contains(mqRunmqsc, "MSGIN") || strings.Contains(mqRunmqsc, "MSGOUT") {
		t.Fatal("QSTATUS must not select MSGIN/MSGOUT")
	}
}

func TestMQMultipleQueueManagers(t *testing.T) {
	m := &mqCollector{cfg: mqConfig{Command: "dspmq", Runmqsc: "runmqsc", Timeout: time.Second}}
	m.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "dspmq" {
			return []byte("QMNAME(QM1) STATUS(Running)\nQMNAME(QM2) STATUS(Ended)\n"), nil
		}
		return []byte("QUEUE(APP.Q)\nCURDEPTH(1)\nMAXDEPTH(10)\n"), nil
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch1, ok := reg.Chart("mq.qmgr.status." + sanitizeID("QM1"))
	if !ok {
		t.Fatal("missing QM1 status")
	}
	_, v1 := ch1.LastValues()
	if v1["status"] != 1 {
		t.Fatalf("QM1 %v", v1)
	}
	ch2, ok := reg.Chart("mq.qmgr.status." + sanitizeID("QM2"))
	if !ok {
		t.Fatal("missing QM2 status")
	}
	_, v2 := ch2.LastValues()
	if v2["status"] != 0 {
		t.Fatalf("QM2 %v", v2)
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
	ltab, err := lx.Functions()[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ltable, ok := ltab.(Table)
	if !ok || ltable.Total != 2 {
		t.Fatalf("lxc-containers = %+v", ltab)
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
	etab, err := ec.Functions()[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	etable, ok := etab.(Table)
	if !ok || etable.Total != 1 {
		t.Fatalf("ecs-containers = %+v", etab)
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
	ctab, err := cd.Functions()[0].Run(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ctable, ok := ctab.(Table)
	if !ok || ctable.Total != 1 {
		t.Fatalf("containerd-containers = %+v", ctab)
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
	if err := (&mqCollector{cfg: mqConfig{Command: "dspmq", Timeout: time.Second}}).Init(reg); err == nil {
		t.Fatal("mq should disable without queue managers")
	}
}

func TestWebspherePrometheusParse(t *testing.T) {
	out := parseWebsphere([]byte(`# TYPE websphere_heap_used gauge
websphere_heap_used 256
websphere_heap_max 1024
websphere_threads_active 8
websphere_thread_pool 40
websphere_httpsessions 3
websphere_servlet_requests 500
`))
	if out["used"] != 256 || out["max"] != 1024 || out["active"] != 8 || out["pool"] != 40 || out["live"] != 3 || out["requests"] != 500 {
		t.Fatalf("%v", out)
	}

	was := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("websphere_heap_used 64\nwebsphere_heap_max 256\nwebsphere_threads_active 1\nwebsphere_thread_pool 10\nwebsphere_httpsessions 2\nwebsphere_servlet_requests 9\n"))
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
	if vals["used"] != 64 || vals["free"] != 192 {
		t.Fatalf("%v", vals)
	}
}

func TestPandasSortedDimensions(t *testing.T) {
	pd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"zebra": 1, "alpha": 2, "mid": 3}`))
	}))
	defer pd.Close()
	p := &pandasCollector{cfg: pandasConfig{Timeout: time.Second, Jobs: []pandasJob{{Name: "sorted", URL: pd.URL, Format: "json"}}}}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("pandas.sorted")
	if !ok {
		t.Fatal("missing pandas.sorted")
	}
	if len(ch.Dimensions) != 3 || ch.Dimensions[0].ID != "alpha" || ch.Dimensions[1].ID != "mid" || ch.Dimensions[2].ID != "zebra" {
		ids := make([]string, len(ch.Dimensions))
		for i, d := range ch.Dimensions {
			ids[i] = d.ID
		}
		t.Fatalf("unsorted dims %v", ids)
	}
}
