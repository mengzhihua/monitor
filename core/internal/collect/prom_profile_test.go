package collect

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

const etcdMetrics = `# HELP etcd_server_has_leader Whether or not a leader exists
# TYPE etcd_server_has_leader gauge
etcd_server_has_leader 1
# TYPE etcd_server_leader_changes_seen_total counter
etcd_server_leader_changes_seen_total 3
# TYPE etcd_mvcc_db_total_size_in_bytes gauge
etcd_mvcc_db_total_size_in_bytes 1048576
# TYPE process_resident_memory_bytes gauge
process_resident_memory_bytes 4096
# TYPE go_goroutines gauge
go_goroutines 42
`

func TestPrometheusNativeEtcdAuto(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, etcdMetrics)
	}))
	defer srv.Close()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &prometheusCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Jobs = []prometheusJob{{Name: "etcd", URL: srv.URL}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := p.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("etcd.has_leader")
	if !ok {
		t.Fatalf("missing native etcd.has_leader; charts=%v", chartIDs(reg))
	}
	if ch.Context != "etcd.has_leader" {
		t.Fatalf("context %s", ch.Context)
	}
	_, v := ch.LastValues()
	if v["value"] != 1 {
		t.Fatalf("has_leader %v", v)
	}
	if _, ok := reg.Chart("prom.etcd_server_has_leader"); ok {
		t.Fatal("mapped family should not stay on prom.*")
	}
	if _, ok := reg.Chart("prom.process_resident_memory_bytes"); !ok {
		t.Fatal("unmapped leftover should stay prom.*")
	}
	if _, ok := reg.Chart("go.goroutines"); ok {
		t.Fatal("go_runtime must not auto-select")
	}
}

func TestPrometheusNativeJobSuffixAndNoFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, etcdMetrics)
	}))
	defer srv.Close()
	fb := false
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &prometheusCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Jobs = []prometheusJob{{Name: "prod", URL: srv.URL, Profile: "etcd", Fallback: &fb}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("etcd.has_leader.prod"); !ok {
		t.Fatalf("expected job suffix, charts=%v", chartIDs(reg))
	}
	if _, ok := reg.Chart("prom.process_resident_memory_bytes"); ok {
		t.Fatal("fallback=false should drop leftovers")
	}
}

func TestPrometheusProfilesOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, etcdMetrics)
	}))
	defer srv.Close()
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &prometheusCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Profiles = "off"
		c.Jobs = []prometheusJob{{Name: "etcd", URL: srv.URL}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("etcd.has_leader"); ok {
		t.Fatal("profiles: off should not wrap")
	}
	if _, ok := reg.Chart("prom.etcd_server_has_leader"); !ok {
		t.Fatal("expected generic prom.* chart")
	}
}

func TestPrometheusFastAPIExplicit(t *testing.T) {
	body := `# TYPE http_requests_total counter
http_requests_total{status="200"} 10
http_requests_total{status="500"} 1
# TYPE http_requests_inprogress gauge
http_requests_inprogress{handler="/v1"} 2
`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := &prometheusCollector{}
	_ = p.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Jobs = []prometheusJob{{Name: "app", URL: srv.URL}}
		return nil
	})
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("fastapi.requests"); ok {
		t.Fatal("fastapi must not auto-select on generic http_*")
	}

	p2 := &prometheusCollector{}
	_ = p2.Configure(func(v any) error {
		c := v.(*prometheusConfig)
		c.Jobs = []prometheusJob{{Name: "api", URL: srv.URL, Profile: "fastapi"}}
		return nil
	})
	reg2 := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	if err := p2.Init(reg2); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < 2; i++ {
		if err := p2.Collect(context.Background(), reg2, now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	ch, ok := reg2.Chart("fastapi.requests.api")
	if !ok {
		t.Fatalf("missing fastapi.requests.api; %v", chartIDs(reg2))
	}
	_, v := ch.LastValues()
	if v["200"] != 0 || v["500"] != 0 {
		// incremental: first tick seeds, second tick is delta 0 because body is constant
	}
	if _, has := v["200"]; !has {
		t.Fatalf("status dims %v", v)
	}
}

func TestPromCatalog(t *testing.T) {
	c := PromCatalog()
	if c["profiles"] == nil || c["dedicated"] == nil {
		t.Fatalf("%v", c)
	}
	if LookupPromProfile("etcd") == nil || LookupPromProfile("fastapi") == nil {
		t.Fatal("missing stock profiles")
	}
	if LookupPromProfile("off") != nil {
		t.Fatal("off is not a profile")
	}
	names := PromProfileNames()
	if len(names) < 10 || !containsStr(names, "etcd") {
		t.Fatalf("%v", names)
	}
}

func TestApplyPromProfileKafkaLabels(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	p := LookupPromProfile("kafka")
	samples := ingest.ParseOpenMetrics(`# TYPE kafka_brokers gauge
kafka_brokers 3
# TYPE kafka_consumergroup_lag gauge
kafka_consumergroup_lag{consumergroup="orders"} 12
`)
	used := map[string]bool{}
	if n := applyPromProfile(reg, time.Unix(1, 0), p, "kafka", samples, used); n == 0 {
		t.Fatal("no samples")
	}
	ch, ok := reg.Chart("kafka.consumergroup_lag")
	if !ok {
		t.Fatal(chartIDs(reg))
	}
	_, v := ch.LastValues()
	if v["orders"] != 12 {
		t.Fatalf("%v", v)
	}
}

func chartIDs(reg *registry.Registry) []string {
	var ids []string
	for _, c := range reg.Charts() {
		ids = append(ids, c.ID)
	}
	return ids
}

func containsStr(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

func TestMetricMatches(t *testing.T) {
	if !metricMatches([]string{"etcd_"}, "etcd_server_has_leader") {
		t.Fatal("prefix")
	}
	if metricMatches([]string{"etcd_"}, "process_resident_memory_bytes") {
		t.Fatal("no match")
	}
	if !strings.HasPrefix("vllm:num_requests_running", "vllm:") {
		t.Fatal("colon prefix")
	}
}
