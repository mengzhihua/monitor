package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestDockerCollectorFakeEngine(t *testing.T) {
	var mu sync.Mutex
	containers := []dockerListEntry{
		{ID: "aaa111", Names: []string{"/web"}, Image: "nginx:1", State: "running"},
		{ID: "bbb222", Names: []string{"/k8s_POD.x"}, Image: "pause", State: "running"},
		{ID: "ccc333", Names: []string{"/old"}, Image: "busybox", State: "exited"},
	}
	cpu := uint64(1_000_000_000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]string{"Version": "27.0"})
		case r.URL.Path == "/containers/json":
			_ = json.NewEncoder(w).Encode(containers)
		case strings.HasSuffix(r.URL.Path, "/stats"):
			if r.URL.Query().Get("one-shot") != "true" {
				t.Errorf("stats must be one-shot: %s", r.URL.RawQuery)
			}
			cpu += 500_000_000 // 0.5 core-second per tick
			_ = json.NewEncoder(w).Encode(map[string]any{
				"cpu_stats":    map[string]any{"cpu_usage": map[string]any{"total_usage": cpu}},
				"memory_stats": map[string]any{"usage": 300 << 20, "limit": 1 << 30, "stats": map[string]any{"inactive_file": 100 << 20}},
				"networks":     map[string]any{"eth0": map[string]any{"rx_bytes": 1000, "tx_bytes": 2000}},
				"blkio_stats":  map[string]any{"io_service_bytes_recursive": []map[string]any{{"op": "Read", "value": 10}, {"op": "Write", "value": 20}}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	d := &dockerCollector{}
	if err := d.Configure(func(v any) error {
		v.(*dockerConfig).Address = strings.Replace(srv.URL, "http://", "tcp://", 1)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := d.Collect(context.Background(), reg, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := reg.Chart("docker.k8s_POD_x.cpu"); ok {
		t.Fatal("excluded container got charts")
	}
	ch, ok := reg.Chart("docker.web.cpu")
	if !ok {
		t.Fatal("web container charts missing")
	}
	_, vals := ch.LastValues()
	if vals["cpu"] != 50 {
		t.Fatalf("cpu%% = %v", vals)
	}
	mem, _ := reg.Chart("docker.web.mem")
	_, mv := mem.LastValues()
	if mv["used"] != 200 {
		t.Fatalf("mem used MiB = %v", mv)
	}
	st, _ := reg.Chart("docker.containers")
	_, sv := st.LastValues()
	if sv["running"] != 2 || sv["exited"] != 1 {
		t.Fatalf("states = %v", sv)
	}

	// container goes away → charts removed
	mu.Lock()
	containers = containers[1:]
	mu.Unlock()
	if err := d.Collect(context.Background(), reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("docker.web.cpu"); ok {
		t.Fatal("charts of a removed container survived")
	}
}
