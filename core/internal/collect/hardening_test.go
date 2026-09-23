package collect

import (
	"context"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestCollectorTLSVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  CollectorTLS
		want bool
	}{
		{"untrusted", CollectorTLS{}, false}, {"trusted CA", CollectorTLS{CAFile: ca}, true}, {"explicit insecure", CollectorTLS{InsecureSkipVerify: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := collectorHTTPClient(time.Second, tc.cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseIdleConnections()
			resp, err := c.Get(srv.URL)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.want {
				t.Fatalf("success=%v want=%v: %v", err == nil, tc.want, err)
			}
		})
	}
	if _, err := collectorHTTPClient(time.Second, CollectorTLS{CAFile: ca + "missing"}); err == nil {
		t.Fatal("missing CA accepted")
	}
}

func TestMSSQLRatioAndMissingCounters(t *testing.T) {
	for _, tc := range []struct {
		body    string
		ratio   float64
		present bool
	}{
		{"User Connections 12\nBuffer cache hit ratio 98\nBuffer cache hit ratio base 200\n", 49, true},
		{"User Connections 12\nBuffer cache hit ratio 98\n", 0, false},
		{"User Connections 12\nBuffer cache hit ratio 98\nBuffer cache hit ratio base 0\n", 0, false},
	} {
		m := &mssqlCollector{run: func(context.Context, string, ...string) ([]byte, error) { return []byte(tc.body), nil }}
		reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
		if err := m.Init(reg); err != nil {
			t.Fatal(err)
		}
		if err := m.Collect(context.Background(), reg, time.Now()); err != nil {
			t.Fatal(err)
		}
		ch, _ := reg.Chart("mssql.buffer_cache_hit_ratio")
		_, v := ch.LastValues()
		got, ok := v["hit_ratio"]
		if ok != tc.present || ok && got != tc.ratio {
			t.Fatalf("ratio %v, present %v", got, ok)
		}
		ch, _ = reg.Chart("mssql.blocked_processes")
		_, v = ch.LastValues()
		if len(v) != 0 {
			t.Fatalf("missing counter invented: %v", v)
		}
	}
}

type recoveringCollector struct {
	available *atomic.Bool
	runs      *atomic.Int32
	init      bool
}

func (c *recoveringCollector) Name() string { return "hardening-test" }
func (c *recoveringCollector) Init(*registry.Registry) error {
	if !c.available.Load() {
		return fmt.Errorf("offline")
	}
	c.init = true
	return nil
}
func (c *recoveringCollector) Collect(context.Context, *registry.Registry, time.Time) error {
	if !c.init {
		panic("collect before init")
	}
	c.runs.Add(1)
	return nil
}
func (c *recoveringCollector) Stop() { c.init = false }
func TestSchedulerRecoveryAndConcurrentToggle(t *testing.T) {
	var available atomic.Bool
	var runs atomic.Int32
	Register("hardening-test", func() Collector { return &recoveringCollector{available: &available, runs: &runs} })
	defer func() { regMu.Lock(); delete(factories, "hardening-test"); regMu.Unlock() }()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	s := NewScheduler(reg, nil, Options{Names: []string{"hardening-test"}})
	now := time.Now().Add(2 * time.Minute)
	s.tick(context.Background(), now)
	s.initWG.Wait()
	if runs.Load() != 0 {
		t.Fatal("offline collector ran")
	}
	available.Store(true)
	s.tick(context.Background(), now.Add(2*time.Minute))
	s.initWG.Wait()
	s.tick(context.Background(), now.Add(2*time.Minute))
	if runs.Load() != 1 || !s.Status()[0].Enabled {
		t.Fatal("collector did not recover")
	}
	s.SetEnabled("hardening-test", false)
	s.tick(context.Background(), now.Add(3*time.Minute))
	if runs.Load() != 1 {
		t.Fatal("disabled collector ran")
	}
	s.SetEnabled("hardening-test", true)
	s.tick(context.Background(), now.Add(4*time.Minute))
	s.initWG.Wait()
	s.tick(context.Background(), now.Add(4*time.Minute))
	if runs.Load() != 2 {
		t.Fatal("collector did not reinitialize")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			s.SetEnabled("hardening-test", i%2 == 0)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			s.tick(context.Background(), now.Add(5*time.Minute))
			s.Status()
			s.Functions()
		}
	}()
	wg.Wait()
	s.stop()
}
