package collect

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM12FCollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"icecast", "phpdaemon", "pika", "maxscale", "nginxplus", "nginxunit",
		"docker_engine", "riakkv", "litespeed", "boinc", "spigotmc", "w1sensor",
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

func TestIcecastPHPDaemonMaxscaleNginx(t *testing.T) {
	ice := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"icestats":{"source":[{"listenurl":"http://h/live","listeners":4},{"listenurl":"http://h/radio","listeners":1}]}}`))
	}))
	defer ice.Close()
	i := &icecastCollector{cfg: icecastConfig{URL: ice.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := i.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := i.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	php := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"uptime":9,"workers":{"alive-count":5,"shutdown":1,"alive":{"idle":3,"busy":2,"reloading":0,"idle-states":{"preinit":1,"init":1,"initialized":1}}}}`))
	}))
	defer php.Close()
	p := &phpdaemonCollector{cfg: phpdaemonConfig{URL: php.URL, Timeout: time.Second}}
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	mx := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "threads") {
			_, _ = w.Write([]byte(`{"data":[{"attributes":{"stats":{"sessions":2,"reads":10,"writes":4,"accepts":3,"errors":1,"hangups":0}}}]}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"attributes":{"uptime":100}}}`))
	}))
	defer mx.Close()
	m := &maxscaleCollector{cfg: maxscaleConfig{URL: mx.URL, Timeout: time.Second}}
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := m.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	np := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/" || r.URL.Path == "/api":
			_, _ = w.Write([]byte(`[6,7,8]`))
		case strings.HasSuffix(r.URL.Path, "/connections"):
			_, _ = w.Write([]byte(`{"accepted":20,"dropped":1,"active":3,"idle":2}`))
		case strings.HasSuffix(r.URL.Path, "/http/requests"):
			_, _ = w.Write([]byte(`{"total":50,"current":4}`))
		case strings.HasSuffix(r.URL.Path, "/ssl"):
			_, _ = w.Write([]byte(`{"handshakes":8}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer np.Close()
	n := &nginxplusCollector{cfg: nginxplusConfig{URL: np.URL, Timeout: time.Second}}
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	nu := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"connections":{"accepted":10,"closed":8,"active":1,"idle":1},"requests":{"total":40}}`))
	}))
	defer nu.Close()
	u := &nginxunitCollector{cfg: nginxunitConfig{URL: nu.URL, Timeout: time.Second}}
	if err := u.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := u.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDockerEngineRiakKV(t *testing.T) {
	de := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`engine_daemon_container_actions_seconds_count{action="create"} 4
engine_daemon_container_actions_seconds_count{action="start"} 3
engine_daemon_container_actions_seconds_count{action="delete"} 1
engine_daemon_container_states_containers{state="running"} 2
engine_daemon_container_states_containers{state="paused"} 0
engine_daemon_container_states_containers{state="stopped"} 1
engine_daemon_health_checks_failed_total 5
`))
	}))
	defer de.Close()
	d := &dockerEngineCollector{cfg: dockerEngineConfig{URL: de.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	rk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"node_gets_total":10,"node_puts_total":4,"node_get_fsm_time_mean":1500,"node_get_fsm_time_median":1200,"node_get_fsm_time_95":3000,"sys_processes":80,"pbc_active":2,"node_get_fsm_active":1,"node_put_fsm_active":0}`))
	}))
	defer rk.Close()
	r := &riakkvCollector{cfg: riakkvConfig{URL: rk.URL, Timeout: time.Second}}
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := r.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("riak.kv.throughput"); !ok {
		t.Fatal("missing riak.kv.throughput")
	}
}

func TestPikaINFO(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	info := "connected_clients:3\nused_memory:2048\ntotal_connections_received:9\ntotal_commands_processed:40\nconnected_slaves:1\nuptime_in_seconds:99\n"
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 256)
				_, _ = c.Read(buf)
				body := fmt.Sprintf("$%d\r\n%s\r\n", len(info), info)
				_, _ = io.WriteString(c, body)
			}(c)
		}
	}()
	p := &pikaCollector{cfg: pikaConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("pika.memory"); !ok {
		t.Fatal("missing pika.memory")
	}
}

func TestLitespeedBoincSpigotW1(t *testing.T) {
	report := `VERSION: LiteSpeed
BPS_IN: 100, BPS_OUT: 200, SSL_BPS_IN: 10, SSL_BPS_OUT: 20
MAXCONN: 1000, MAXSSL_CONN: 1000, AVAILCONN: 990, AVAILSSL: 995
PLAINCONN: 10, SSLCONN: 5, IDLECONN: 1
REQ_RATE []: REQ_PROCESSING: 2, REQ_PER_SEC: 3.5, PUB_CACHE_HITS_PER_SEC: 1.2, PRIVATE_CACHE_HITS_PER_SEC: 0.4, STATIC_HITS_PER_SEC: 0.8
`
	ls := &litespeedCollector{
		cfg: litespeedConfig{ReportsDir: "/tmp/lshttpd"},
		readDir: func(dir string) ([]string, error) {
			return []string{dir + "/.rtreport"}, nil
		},
		readFile: func(path string) ([]byte, error) { return []byte(report), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := ls.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ls.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("litespeed.requests"); !ok {
		t.Fatal("missing litespeed.requests")
	}

	b := &boincCollector{
		cfg: boincConfig{Address: "127.0.0.1:31416", Timeout: time.Second},
		results: func(context.Context) ([]boincResult, error) {
			return []boincResult{
				{State: 2, Active: true, ActiveState: 1, SchedulerState: 2},
				{State: 3},
			}, nil
		},
	}
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	s := &spigotmcCollector{
		cfg: spigotmcConfig{Address: "127.0.0.1:25575", Timeout: time.Second},
		cmd: func(_ context.Context, command string) (string, error) {
			if command == "tps" {
				return "TPS from last 1m, 5m, 15m: 20.0, 19.5, 19.0\nMem: 512/1024M: 2048\n", nil
			}
			return "There are 3 of a max of 20 players online", nil
		},
	}
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}

	w := &w1sensorCollector{
		cfg: w1sensorConfig{SensorsPath: "/sys/bus/w1/devices"},
		readDir: func(dir string) ([]string, error) {
			return []string{"28-00000aabbccd", "w1_bus_master1"}, nil
		},
		readFile: func(path string) ([]byte, error) {
			return []byte("e0 02 ff ff 7f ff ff ff c2 : crc=c2 YES\ne0 02 ff ff 7f ff ff ff c2 t=46000\n"), nil
		},
	}
	if err := w.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := w.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("w1sensor.temperature.28-00000aabbccd"); !ok {
		t.Fatal("missing w1sensor chart")
	}
}

func TestM12FAutoDisable(t *testing.T) {
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := (&icecastCollector{cfg: icecastConfig{URL: "http://127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("icecast should disable")
	}
	if err := (&pikaCollector{cfg: pikaConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("pika should disable")
	}
	if err := (&litespeedCollector{cfg: litespeedConfig{ReportsDir: "/no/such/lshttpd"}}).Init(reg); err == nil {
		t.Fatal("litespeed should disable")
	}
	if err := (&w1sensorCollector{cfg: w1sensorConfig{SensorsPath: "/no/such/w1"}}).Init(reg); err == nil {
		t.Fatal("w1sensor should disable")
	}
	if err := (&boincCollector{cfg: boincConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("boinc should disable")
	}
	if err := (&spigotmcCollector{cfg: spigotmcConfig{Address: "127.0.0.1:1", Timeout: 50 * time.Millisecond}}).Init(reg); err == nil {
		t.Fatal("spigotmc should disable")
	}
}
