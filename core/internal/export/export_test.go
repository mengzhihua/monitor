package export

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

func TestFlushJSONAndInflux(t *testing.T) {
	var gotJSON, gotInflux string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if strings.Contains(r.URL.Path, "json") {
			gotJSON = string(b)
		} else {
			gotInflux = string(b)
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(1_700_000_000, 0), map[string]float64{"used": 12.5})

	e := New(reg, []Destination{
		{Type: "json", URL: srv.URL + "/json"},
		{Type: "influx", URL: srv.URL + "/write"},
	}, nil)
	if err := e.flushJSON(context.Background(), e.dest[0]); err != nil {
		t.Fatal(err)
	}
	if err := e.flushInflux(context.Background(), e.dest[1]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotJSON, `"used"`) || !strings.Contains(gotJSON, "system.ram") {
		t.Fatalf("json = %s", gotJSON)
	}
	if !strings.Contains(gotInflux, "dimension=used") || !strings.Contains(gotInflux, "value=12.5") {
		t.Fatalf("influx = %s", gotInflux)
	}
}

func TestFlushOpenTSDB(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = string(b)
		w.WriteHeader(204)
	}))
	defer srv.Close()
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(1_700_000_000, 0), map[string]float64{"used": 12.5})
	e := New(reg, []Destination{{Type: "opentsdb", URL: srv.URL, Prefix: "monitor"}}, nil)
	if err := e.flushOpenTSDB(context.Background(), e.dest[0]); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, `"metric":"monitor.system_ram.used"`) || !strings.Contains(got, `"value":12.5`) {
		t.Fatalf("opentsdb = %s", got)
	}
}

func TestFlushGraphite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		b, _ := io.ReadAll(c)
		got <- string(b)
	}()
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(1_700_000_000, 0), map[string]float64{"used": 3})
	e := New(reg, []Destination{{Type: "graphite", Address: ln.Addr().String(), Prefix: "mon"}}, nil)
	if err := e.flushGraphite(context.Background(), e.dest[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case s := <-got:
		if !strings.Contains(s, "mon.h.system_ram.used 3 1700000000") {
			t.Fatalf("%q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
