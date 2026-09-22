package export

import (
	"context"
	"encoding/binary"
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

func TestFlushMongo(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var hdr [16]byte
		if _, err := io.ReadFull(c, hdr[:]); err != nil {
			return
		}
		n := int(uint32(hdr[0]) | uint32(hdr[1])<<8 | uint32(hdr[2])<<16 | uint32(hdr[3])<<24)
		if n < 16 {
			return
		}
		rest := make([]byte, n-16)
		_, _ = io.ReadFull(c, rest)
		got <- append(hdr[:], rest...)
		reply := make([]byte, 20)
		binary.LittleEndian.PutUint32(reply[0:4], 20)
		binary.LittleEndian.PutUint32(reply[12:16], mongoOpMsg)
		_, _ = c.Write(reply)
	}()
	reg := registry.New(&registry.Host{Hostname: "box", UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "system.ram", Dimensions: []*registry.Dimension{{ID: "used"}}})
	_ = reg.Collect("system.ram", time.Unix(1_700_000_000, 0), map[string]float64{"used": 12.5})
	e := New(reg, []Destination{{Type: "mongodb", Address: ln.Addr().String(), Database: "netdata", Collection: "metrics"}}, nil)
	if err := e.flushMongo(context.Background(), e.dest[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-got:
		s := string(b)
		if !strings.Contains(s, "system.ram") || !strings.Contains(s, "box") || !strings.Contains(s, "used") {
			t.Fatalf("mongo payload missing fields: %q", s)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}
