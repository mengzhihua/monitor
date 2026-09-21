package collect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestElasticsearchCollector(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/_cluster/health", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "green", "number_of_nodes": 1, "number_of_data_nodes": 1,
			"active_shards": 5, "relocating_shards": 0, "initializing_shards": 0, "unassigned_shards": 0,
		})
	})
	mux.HandleFunc("/_nodes/_local/stats", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"nodes": map[string]any{"n1": map[string]any{
			"jvm": map[string]any{"mem": map[string]any{"heap_used_in_bytes": 10, "heap_committed_in_bytes": 20},
				"gc": map[string]any{"collectors": map[string]any{"young": map[string]any{"collection_time_in_millis": 3}, "old": map[string]any{"collection_time_in_millis": 4}}}},
			"indices": map[string]any{
				"docs": map[string]any{"count": 9}, "store": map[string]any{"size_in_bytes": 100},
				"indexing": map[string]any{"index_total": 1, "delete_total": 0},
				"search":   map[string]any{"query_total": 2, "fetch_total": 1},
			},
			"http": map[string]any{"current_open": 7},
		}}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	e := &elasticsearchCollector{cfg: elasticsearchConfig{URL: srv.URL, Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := e.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("elasticsearch.cluster_health_status"); !ok {
		t.Fatal("missing chart")
	}
}

func TestRabbitMQCollector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/overview" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"object_totals": map[string]any{"connections": 2, "channels": 3, "queues": 4, "consumers": 1, "exchanges": 5},
			"queue_totals":  map[string]any{"messages": 9, "messages_ready": 6, "messages_unacknowledged": 3},
			"message_stats": map[string]any{"publish": 10, "deliver_get": 8, "ack": 7, "get": 1},
		})
	}))
	defer srv.Close()
	r := &rabbitmqCollector{cfg: rabbitmqConfig{URL: srv.URL, User: "guest", Password: "guest", Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := r.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := r.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestFileLogs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "syslog")
	if err := os.WriteFile(p, []byte("Jan  2 03:04:05 host sshd[12]: Failed password for root\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := QueryLogs(LogQuery{Source: "file", Files: []string{p}, Query: "Failed", Limit: 10})
	if len(rows) != 1 || rows[0].Unit != "sshd" || rows[0].PID != "12" {
		t.Fatalf("%+v", rows)
	}
}

func TestParseNvidiaSMI(t *testing.T) {
	gpus, err := parseNvidiaSMI("0, NVIDIA A100, 12, 34, 1024, 4096, 41, 90.5\n")
	if err != nil || len(gpus) != 1 || gpus[0].util != 12 || gpus[0].memTotal != 4096 {
		t.Fatalf("%v %+v", err, gpus)
	}
}

func TestOTLPCollectorIngest(t *testing.T) {
	c := &otlpCollector{}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	n, err := c.Ingest(reg, []byte(`{"resourceMetrics":[{"scopeMetrics":[{"metrics":[{"name":"demo","gauge":{"dataPoints":[{"asDouble":4}]}}]}]}]}`), "application/json")
	if err != nil || n == 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	if _, ok := reg.Chart("otlp.demo"); !ok {
		t.Fatal("chart missing")
	}
}

func TestParseOTLPViaIngest(t *testing.T) {
	s, err := ingest.ParseOTLP([]byte(`{"resourceMetrics":[]}`), "application/json")
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		s = []ingest.Sample{}
	}
	_ = s
}
