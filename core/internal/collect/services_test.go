package collect

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestNginxStubStatus(t *testing.T) {
	requests := 31070465
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests += 100
		_, _ = w.Write([]byte("Active connections: 291 \nserver accepts handled requests\n 16630948 16630948 " +
			itoa(requests) + " \nReading: 6 Writing: 179 Waiting: 106 \n"))
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	n := &nginxCollector{}
	_ = n.Configure(func(v any) error { v.(*nginxConfig).URL = srv.URL; return nil })
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := n.Collect(context.Background(), reg, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := reg.Chart("nginx.connections_status")
	_, v := c.LastValues()
	if v["reading"] != 6 || v["writing"] != 179 || v["waiting"] != 106 {
		t.Fatalf("status = %v", v)
	}
	rq, _ := reg.Chart("nginx.requests")
	_, rv := rq.LastValues()
	if rv["requests"] != 100 {
		t.Fatalf("requests/s = %v", rv)
	}
	if _, err := parseStubStatus("garbage"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestNginxStatusFailureMarksUnsupported(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "down" {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if mode == "bad" {
			_, _ = w.Write([]byte("not stub status"))
			return
		}
		_, _ = w.Write([]byte("Active connections: 4 \nserver accepts handled requests\n 10 10 20 \nReading: 1 Writing: 2 Waiting: 1 \n"))
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	n := &nginxCollector{}
	_ = n.Configure(func(v any) error { v.(*nginxConfig).URL = srv.URL; return nil })
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := n.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	active := mustChart(t, reg, "nginx.connections")
	status := mustChart(t, reg, "nginx.connections_status")
	if _, vals := active.LastValues(); vals["active"] != 4 || active.Unsupported != "" {
		t.Fatalf("active %+v %q", vals, active.Unsupported)
	}

	mode = "down"
	if err := n.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if active.Unsupported == "" || status.Unsupported == "" || active.LastCollectionTime() != now.Unix() {
		t.Fatalf("active %q at %d status %q", active.Unsupported, active.LastCollectionTime(), status.Unsupported)
	}

	mode = "bad"
	if err := n.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if active.Unsupported == "" {
		t.Fatal("bad body stayed supported")
	}

	mode = "ok"
	if err := n.Collect(context.Background(), reg, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if active.Unsupported != "" || status.Unsupported != "" {
		t.Fatalf("stuck active %q status %q", active.Unsupported, status.Unsupported)
	}
	if _, vals := active.LastValues(); vals["active"] != 4 {
		t.Fatalf("recovered %+v", vals)
	}
}

// fakeRedis speaks just enough RESP for AUTH + INFO.
func fakeRedis(t *testing.T, password string, info func() string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				br := bufio.NewReader(conn)
				authed := password == ""
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if !strings.HasPrefix(line, "*") {
						continue
					}
					var args []string
					n := int(line[1] - '0')
					for i := 0; i < n; i++ {
						_, _ = br.ReadString('\n') // $len
						a, _ := br.ReadString('\n')
						args = append(args, strings.TrimRight(a, "\r\n"))
					}
					switch strings.ToUpper(args[0]) {
					case "AUTH":
						if args[len(args)-1] == password {
							authed = true
							_, _ = conn.Write([]byte("+OK\r\n"))
						} else {
							_, _ = conn.Write([]byte("-WRONGPASS invalid username-password pair\r\n"))
						}
					case "INFO":
						if !authed {
							_, _ = conn.Write([]byte("-NOAUTH Authentication required.\r\n"))
							continue
						}
						body := info()
						_, _ = conn.Write([]byte("$" + itoa(len(body)) + "\r\n" + body + "\r\n"))
					}
				}
			}()
		}
	}()
	return ln.Addr().String()
}

func TestRedisInfo(t *testing.T) {
	cmds := 1000
	addr := fakeRedis(t, "secret", func() string {
		cmds += 250
		return "# Server\r\nredis_version:7.2.4\r\nuptime_in_seconds:42\r\n# Clients\r\nconnected_clients:3\r\nblocked_clients:0\r\n" +
			"# Memory\r\nused_memory:1048576\r\nused_memory_rss:2097152\r\nused_memory_peak:3145728\r\n" +
			"# Stats\r\ntotal_connections_received:5\r\nrejected_connections:0\r\ntotal_commands_processed:" + itoa(cmds) +
			"\r\nkeyspace_hits:10\r\nkeyspace_misses:2\r\nevicted_keys:0\r\nexpired_keys:1\r\ntotal_net_input_bytes:100\r\ntotal_net_output_bytes:200\r\n" +
			"# Keyspace\r\ndb0:keys=12,expires=0,avg_ttl=0\r\ndb2:keys=3,expires=1,avg_ttl=5\r\n"
	})

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	bad := &redisCollector{}
	_ = bad.Configure(func(v any) error { c := v.(*redisConfig); c.Address, c.Password = addr, "wrong"; return nil })
	if err := bad.Init(reg); err == nil || !strings.Contains(err.Error(), "WRONGPASS") {
		t.Fatalf("expected auth error, got %v", err)
	}

	r := &redisCollector{}
	_ = r.Configure(func(v any) error { c := v.(*redisConfig); c.Address, c.Password = addr, "secret"; return nil })
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := r.Collect(context.Background(), reg, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	c, _ := reg.Chart("redis.commands")
	_, v := c.LastValues()
	if v["processed"] != 250 {
		t.Fatalf("commands/s = %v", v)
	}
	m, _ := reg.Chart("redis.memory")
	_, mv := m.LastValues()
	if mv["used"] != 1 || mv["rss"] != 2 || mv["peak"] != 3 {
		t.Fatalf("memory = %v", mv)
	}
	k, _ := reg.Chart("redis.keys")
	_, kv := k.LastValues()
	if kv["db0"] != 12 || kv["db2"] != 3 {
		t.Fatalf("keys = %v", kv)
	}
}

func TestRedisInfoFailureMarksUnsupported(t *testing.T) {
	addr := fakeRedis(t, "", func() string {
		return "# Server\r\nuptime_in_seconds:42\r\n# Clients\r\nconnected_clients:3\r\n"
	})
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	r := &redisCollector{}
	_ = r.Configure(func(v any) error {
		c := v.(*redisConfig)
		c.Address = addr
		c.Timeout = 200 * time.Millisecond
		return nil
	})
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := r.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	uptime := mustChart(t, reg, "redis.uptime")
	clients := mustChart(t, reg, "redis.clients")
	if _, vals := uptime.LastValues(); vals["uptime"] != 42 || uptime.Unsupported != "" {
		t.Fatalf("uptime %v %q", vals, uptime.Unsupported)
	}
	r.cfg.Address = "127.0.0.1:1"
	if err := r.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if uptime.Unsupported == "" || clients.Unsupported == "" || uptime.LastCollectionTime() != now.Unix() {
		t.Fatalf("uptime %q at %d clients %q", uptime.Unsupported, uptime.LastCollectionTime(), clients.Unsupported)
	}
	r.cfg.Address = addr
	if err := r.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if uptime.Unsupported != "" || clients.Unsupported != "" {
		t.Fatalf("stuck uptime %q clients %q", uptime.Unsupported, clients.Unsupported)
	}
	if _, vals := uptime.LastValues(); vals["uptime"] != 42 {
		t.Fatalf("recovered %+v", vals)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
