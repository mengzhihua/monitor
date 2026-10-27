package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHTTPAgentStatusCodesRetrieveAndRedirects(t *testing.T) {
	noFollow := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte("8"))
		case "/down":
			http.Error(w, "down", http.StatusInternalServerError)
		case "/go":
			http.Redirect(w, r, "/final", http.StatusFound)
		case "/final":
			w.Header().Set("X-Count", "4")
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	h := &httpagentCollector{}
	if err := h.Configure(func(v any) error {
		*v.(*httpagentConfig) = httpagentConfig{Jobs: []httpagentJob{
			{Name: "created", URL: srv.URL + "/ok", StatusCodes: []string{"200-299"}},
			{Name: "down", URL: srv.URL + "/down", StatusCodes: []string{"200"}},
			{Name: "code", URL: srv.URL + "/final", Retrieve: "status"},
			{Name: "hdr", URL: srv.URL + "/final", Retrieve: "headers", Steps: []preprocess.Step{{Type: "regex", Pattern: `X-Count: ([0-9]+)`}}},
			{Name: "redir", URL: srv.URL + "/go", Retrieve: "status", FollowRedirects: &noFollow},
			{Name: "followed", URL: srv.URL + "/go", Retrieve: "status"},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.Collect(context.Background(), reg, time.Unix(20, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "httpagent.created").LastValues(); v["value"] != 8 {
		t.Fatalf("created %+v", v)
	}
	down := mustChart(t, reg, "httpagent.down")
	if _, v := down.LastValues(); v["value"] != 0 && !mathIsNaN(v["value"]) {
		t.Fatalf("down stored %+v", v)
	}
	if !strings.Contains(down.Unsupported, "500") {
		t.Fatalf("unsupported %q", down.Unsupported)
	}
	if _, v := mustChart(t, reg, "httpagent.code").LastValues(); v["value"] != 204 {
		t.Fatalf("status %+v", v)
	}
	if _, v := mustChart(t, reg, "httpagent.hdr").LastValues(); v["value"] != 4 {
		t.Fatalf("headers %+v", v)
	}
	if _, v := mustChart(t, reg, "httpagent.redir").LastValues(); v["value"] != 302 {
		t.Fatalf("nofollow %+v", v)
	}
	if _, v := mustChart(t, reg, "httpagent.followed").LastValues(); v["value"] != 204 {
		t.Fatalf("followed %+v", v)
	}

	// A later allowed response clears the unsupported flag.
	h.cfg.Jobs[1].URL = srv.URL + "/ok"
	h.cfg.Jobs[1].spans, _ = parseStatusCodes([]string{"200-299"})
	if err := h.Collect(context.Background(), reg, time.Unix(21, 0)); err != nil {
		t.Fatal(err)
	}
	if down.Unsupported != "" {
		t.Fatalf("still unsupported %q", down.Unsupported)
	}

	bad := &httpagentCollector{}
	for _, job := range []httpagentJob{
		{Name: "bad", URL: "http://127.0.0.1/", StatusCodes: []string{"20"}},
		{Name: "bad", URL: "http://127.0.0.1/", StatusCodes: []string{"300-200"}},
		{Name: "bad", URL: "http://127.0.0.1/", Retrieve: "json"},
		{Name: "bad", URL: "http://127.0.0.1/", StatusCodes: []string{"200", "201", "202", "203", "204", "205", "206", "207", "208"}},
	} {
		err := bad.Configure(func(v any) error {
			*v.(*httpagentConfig) = httpagentConfig{Jobs: []httpagentJob{job}}
			return nil
		})
		if err == nil {
			t.Fatalf("accepted %+v", job)
		}
	}
}

func mathIsNaN(v float64) bool {
	return v != v
}
