package api

import (
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProcessMetricsOnPrometheusEndpoint(t *testing.T) {
	ts, _ := newTestServer(t, Options{Token: "secret"})
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	text := string(body)
	for _, want := range []string{
		"monitor_process_heap_alloc_bytes",
		"monitor_process_goroutines",
		"monitor_tsdb_series",
		"monitor_charts",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %s in %s", want, text[:min(400, len(text))])
		}
	}
}
