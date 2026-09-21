package collect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12RaidCollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"megacli", "hpssa", "adaptecraid", "redfish", "activemq", "gearman",
		"geth", "ipfs", "pihole", "powerdns_recursor", "rspamd", "typesense",
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

func TestMegaCLIParse(t *testing.T) {
	info := `
Adapter #0
Virtual Drive: 0 (Target Id: 0)
State                                        : Optimal
WWN: 5000c500aaa
Media Error Count: 2
Predictive Failure Count: 1
`
	if ads := parseMegaAdapters(info); len(ads) != 1 || ads[0] != "0" {
		t.Fatalf("adapters %v", ads)
	}
	ds := parseMegaDrives(info)
	if len(ds) != 1 || ds[0].media != 2 {
		t.Fatalf("drives %+v", ds)
	}
	m := &megacliCollector{
		cfg: megacliConfig{Command: "megacli", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "-AdpBbuCmd" {
				return []byte("BBU status for Adapter: 0\nRelative State of Charge: 91 %\n"), nil
			}
			return []byte(info), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("megacli.adapter_health_state.0"); !ok {
		t.Fatal("missing adapter chart")
	}
	if parseMegaAdapterState(info, "0") != "Optimal" {
		t.Fatalf("state %q", parseMegaAdapterState(info, "0"))
	}
}

func TestHPSSAAndAdaptec(t *testing.T) {
	h := &hpssaCollector{
		cfg: hpssaConfig{Command: "ssacli", Timeout: time.Second},
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("Smart Array P408i-a in Slot 0\n   Controller Status: OK\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	a := &adaptecraidCollector{
		cfg: adaptecraidConfig{Command: "arcconf", Timeout: time.Second},
		run: func(_ context.Context, _ string, _ ...string) ([]byte, error) {
			return []byte("Logical device number 0\n   Status of logical device                     : Optimal\nDevice #1\n      State                                 : Online\n"), nil
		},
	}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestRedfishAndActiveMQ(t *testing.T) {
	rf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Systems") {
			_, _ = w.Write([]byte(`{"Members":[{"@odata.id":"/redfish/v1/Systems/1","Id":"1","Status":{"Health":"OK"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer rf.Close()
	r := &redfishCollector{cfg: redfishConfig{URL: rf.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := r.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	amq := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<queues><queue name="TEST"><stats size="1" consumerCount="2" enqueueCount="10" dequeueCount="8"/></queue></queues>`))
	}))
	defer amq.Close()
	a := &activemqCollector{cfg: activemqConfig{URL: amq.URL, Webadmin: "admin", Timeout: time.Second}}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := a.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestGearmanStatus(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			buf := make([]byte, 64)
			n, _ := c.Read(buf)
			cmd := string(buf[:n])
			if strings.Contains(cmd, "priority") {
				_, _ = io.WriteString(c, "fn\t1\t2\t3\t4\n.\n")
			} else {
				_, _ = io.WriteString(c, "fn\t5\t3\t10\n.\n")
			}
			_ = c.Close()
		}
	}()
	g := &gearmanCollector{cfg: gearmanConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := g.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := g.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestGethIPFSPiholeRspamdTypesense(t *testing.T) {
	geth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("chain_head_block 100\np2p_peers 8\nrpc_success 20\nrpc_failure 1\nsystem_cpu_goroutines 40\n"))
	}))
	defer geth.Close()
	g := &gethCollector{cfg: gethConfig{URL: geth.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := g.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := g.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ipfs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "stats/bw") {
			_, _ = w.Write([]byte(`{"TotalIn":10,"TotalOut":4,"RateIn":1,"RateOut":1}`))
			return
		}
		_, _ = w.Write([]byte(`{"Peers":[{},{}]}`))
	}))
	defer ipfs.Close()
	i := &ipfsCollector{cfg: ipfsConfig{URL: ipfs.URL, Timeout: time.Second}}
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := i.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"dns_queries_today":100,"ads_blocked_today":20,"ads_percentage_today":20,"unique_clients":3,"domains_being_blocked":999,"queries_cached":50,"queries_forwarded":30}`))
	}))
	defer ph.Close()
	p := &piholeCollector{cfg: piholeConfig{URL: ph.URL, Timeout: time.Second}}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	rs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"scanned":9,"learned":1,"ham_count":7,"spam_count":2,"connections":3,"actions":{"reject":1,"soft reject":0,"rewrite subject":0,"add header":0,"greylist":0,"no action":8}}`))
	}))
	defer rs.Close()
	rsp := &rspamdCollector{cfg: rspamdConfig{URL: rs.URL, Timeout: time.Second}}
	if err := rsp.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := rsp.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	ty := &typesenseCollector{cfg: typesenseConfig{URL: ts.URL, Timeout: time.Second}}
	if err := ty.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ty.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestPowerDNSRecursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"name":"questions","type":"StatisticItem","value":"10"},{"name":"tcp-questions","type":"StatisticItem","value":"1"},{"name":"ipv6-questions","type":"StatisticItem","value":"0"},{"name":"all-outqueries","type":"StatisticItem","value":"4"},{"name":"tcp-outqueries","type":"StatisticItem","value":"0"},{"name":"ipv6-outqueries","type":"StatisticItem","value":"0"},{"name":"throttled-outqueries","type":"StatisticItem","value":"0"},{"name":"cache-hits","type":"StatisticItem","value":"5"},{"name":"cache-misses","type":"StatisticItem","value":"2"},{"name":"packetcache-hits","type":"StatisticItem","value":"3"},{"name":"packetcache-misses","type":"StatisticItem","value":"1"},{"name":"over-capacity-drops","type":"StatisticItem","value":"0"},{"name":"too-old-drops","type":"StatisticItem","value":"0"},{"name":"cache-entries","type":"StatisticItem","value":"8"},{"name":"packetcache-entries","type":"StatisticItem","value":"7"},{"name":"negcache-entries","type":"StatisticItem","value":"1"}]`))
	}))
	defer srv.Close()
	p := &powerdnsRecursorCollector{cfg: powerdnsRecursorConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestM12RaidAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&typesenseCollector{cfg: typesenseConfig{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("typesense should disable")
	}
	if err := (&gearmanCollector{cfg: gearmanConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("gearman should disable")
	}
}
