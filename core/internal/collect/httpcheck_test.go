package collect

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHTTPCheckJob(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	h := &httpcheckCollector{}
	_ = h.Configure(func(v any) error {
		c := v.(*httpcheckConfig)
		c.Jobs = []httpJob{{Name: "local", URL: srv.URL}}
		return nil
	})
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("httpcheck.status.local")
	_, v := ch.LastValues()
	if v["success"] != 1 || v["failure"] != 0 {
		t.Fatalf("%v", v)
	}
}

func TestPortCheckLocalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, _ := net.SplitHostPort(ln.Addr().String())

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &portcheckCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*portcheckConfig)
		c.Jobs = []portJob{{Name: "loop", Host: "127.0.0.1", Port: atoi(port)}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("portcheck.status.loop")
	_, v := ch.LastValues()
	if v["success"] != 1 {
		t.Fatalf("%v", v)
	}
}

func TestPrometheusScrape(t *testing.T) {
	n := 10.0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n += 5
		_, _ = io.WriteString(w, "# TYPE demo_total counter\ndemo_total  "+itoa(int(n))+"\n")
	}))
	defer srv.Close()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &prometheusCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Jobs = []prometheusJob{{Name: "demo", URL: srv.URL}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := p.Collect(context.Background(), reg, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	ch, ok := reg.Chart("prom.demo_total")
	if !ok {
		t.Fatal("missing chart")
	}
	_, v := ch.LastValues()
	if v["value"] != 5 {
		t.Fatalf("%v", v)
	}
}

func TestMemcachedStats(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 64)
				_, _ = c.Read(buf)
				_, _ = c.Write([]byte("STAT bytes 1048576\r\nSTAT limit_maxbytes 2097152\r\nSTAT curr_connections 3\r\nSTAT total_connections 9\r\nSTAT curr_items 2\r\nSTAT evictions 1\r\nSTAT get_hits 8\r\nSTAT get_misses 2\r\nSTAT cmd_get 10\r\nSTAT cmd_set 4\r\nSTAT bytes_read 100\r\nSTAT bytes_written 200\r\nEND\r\n"))
			}()
		}
	}()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	m := &memcachedCollector{}
	_ = m.Configure(func(v any) error { v.(*memcachedConfig).Address = ln.Addr().String(); return nil })
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := m.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("memcached.cache")
	_, v := ch.LastValues()
	if v["used"] != 1 || v["free"] != 1 { // MiB after divisor
		t.Fatalf("%v", v)
	}
}

func TestMLAnomalyRate(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "demo.x", Dimensions: []*registry.Dimension{{ID: "v"}}})
	m := &mlCollector{}
	_ = m.Configure(func(any) error { return nil })
	m.cfg.Window = 40
	m.cfg.Sigma = 3
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 40; i++ {
		_ = reg.Collect("demo.x", now.Add(time.Duration(i)*time.Second), map[string]float64{"v": float64(i % 3)})
	}
	_ = reg.Collect("demo.x", now.Add(40*time.Second), map[string]float64{"v": 1000})
	_ = m.Collect(context.Background(), reg, now.Add(40*time.Second))
	ch, _ := reg.Chart("anomaly_detection.anomaly_rate")
	_, v := ch.LastValues()
	if v["anomaly_rate"] <= 0 {
		t.Fatalf("expected a positive rate, got %v (weights=%v)", v, m.Weights("anomaly-rate"))
	}
}

func TestMLKMeansWeights(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "demo.steady", Dimensions: []*registry.Dimension{{ID: "v"}}})
	reg.AddChart(&registry.Chart{ID: "demo.spike", Dimensions: []*registry.Dimension{{ID: "v"}}})
	m := &mlCollector{}
	_ = m.Configure(func(any) error { return nil })
	m.cfg.Window = 80
	m.cfg.MinTrain = 24
	m.cfg.TrainEvery = time.Hour
	m.cfg.Lag = 3
	m.cfg.Models = 3
	m.cfg.Threshold = 0.95
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 40; i++ {
		t0 := now.Add(time.Duration(i) * time.Second)
		_ = reg.Collect("demo.steady", t0, map[string]float64{"v": 10})
		_ = reg.Collect("demo.spike", t0, map[string]float64{"v": 10})
	}
	spikeT := now.Add(40 * time.Second)
	_ = reg.Collect("demo.steady", spikeT, map[string]float64{"v": 10})
	_ = reg.Collect("demo.spike", spikeT, map[string]float64{"v": 800})
	_ = m.Collect(context.Background(), reg, spikeT)
	km := m.Weights("kmeans")
	ar := m.Weights("anomaly-rate")
	if len(ar) == 0 {
		t.Fatalf("anomaly-rate empty (kmeans=%v)", km)
	}
	if ar[0].Chart != "demo.spike" {
		t.Fatalf("anomaly-rate ranked %s first: %v", ar[0].Chart, ar)
	}
	if len(km) == 0 || km[0].Chart != "demo.spike" {
		t.Fatalf("kmeans ranked %#v, want demo.spike first", km)
	}
}

func TestMLDiffRingStaysBounded(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	reg.AddChart(&registry.Chart{ID: "demo.x", Dimensions: []*registry.Dimension{{ID: "v"}}})
	m := &mlCollector{}
	if err := m.Configure(func(any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	m.cfg.MaxTrain = 32
	m.cfg.MinTrain = 1_000_000
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 100; i++ {
		_ = reg.Collect("demo.x", now.Add(time.Duration(i)*time.Second), map[string]float64{"v": float64(i)})
	}
	st := m.dims[registry.SeriesID("demo.x", "v")]
	if st == nil || len(st.diffRing) != 32 || st.diffN != 32 || len(st.bits) != m.cfg.Window {
		t.Fatalf("ring diff=%d/%d bits=%d", len(st.diffRing), st.diffN, len(st.bits))
	}
	got := st.copyDiffs()
	if len(got) != 32 || got[len(got)-1] != 1 { // newest diff is 99-98
		t.Fatalf("newest diff %#v", got)
	}
}

func TestKMeans2Separates(t *testing.T) {
	var pts [][]float64
	for i := 0; i < 20; i++ {
		pts = append(pts, []float64{0.1, 0.0, -0.1})
		pts = append(pts, []float64{8, 8.2, 7.9})
	}
	c0, c1 := kmeans2(pts, 12)
	if c0 == nil || c1 == nil {
		t.Fatal("no centroids")
	}
	d := l2(c0, c1)
	if d < 5 {
		t.Fatalf("centroids too close: %v %v d=%g", c0, c1, d)
	}
}
