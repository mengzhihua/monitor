package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestJolokiaErrorMarksUnsupported(t *testing.T) {
	mode := "error"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if mode == "error" {
			_, _ = w.Write([]byte(`{"status":404,"error":"InstanceNotFoundException: java.lang:type=Memory"}`))
			return
		}
		_, _ = w.Write([]byte(`{"value":80,"status":200}`))
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	j := &jolokiaCollector{}
	if err := j.Configure(func(v any) error {
		*v.(*jolokiaConfig) = jolokiaConfig{Timeout: 200 * time.Millisecond, Jobs: []jolokiaJob{{
			Name: "heap", URL: srv.URL, MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage", Path: "used",
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := j.Collect(context.Background(), reg, time.Unix(10, 0)); err != nil {
		t.Fatal(err)
	}
	ch := mustChart(t, reg, "jolokia.heap")
	if ch.Unsupported == "" || ch.LastCollectionTime() != 0 {
		t.Fatalf("jolokia unsupported %q at %d", ch.Unsupported, ch.LastCollectionTime())
	}
	mode = "ok"
	if err := j.Collect(context.Background(), reg, time.Unix(11, 0)); err != nil {
		t.Fatal(err)
	}
	if ch.Unsupported != "" {
		t.Fatalf("error stuck: %q", ch.Unsupported)
	}
	if _, v := ch.LastValues(); v["value"] != 80 {
		t.Fatalf("recovered %+v", v)
	}

	down := &jolokiaCollector{}
	if err := down.Configure(func(v any) error {
		*v.(*jolokiaConfig) = jolokiaConfig{Timeout: 200 * time.Millisecond, Jobs: []jolokiaJob{{
			Name: "gone", URL: "http://127.0.0.1:1", MBean: "java.lang:type=Memory", Attribute: "HeapMemoryUsage",
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := down.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := down.Collect(context.Background(), reg, time.Unix(12, 0)); err != nil {
		t.Fatal(err)
	}
	gone := mustChart(t, reg, "jolokia.gone")
	if gone.Unsupported == "" || gone.LastCollectionTime() != 0 {
		t.Fatalf("transport unsupported %q at %d", gone.Unsupported, gone.LastCollectionTime())
	}
}
