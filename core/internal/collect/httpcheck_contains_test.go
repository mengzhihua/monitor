package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHTTPCheckContainsRequiredText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("status=ok"))
		case "/no":
			_, _ = w.Write([]byte("status=down"))
		default:
			http.Error(w, "status=ok", http.StatusInternalServerError)
		}
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	h := &httpcheckCollector{}
	if err := h.Configure(func(v any) error {
		*v.(*httpcheckConfig) = httpcheckConfig{Jobs: []httpJob{
			{Name: "ok", URL: srv.URL + "/ok", Contains: "status=ok"},
			{Name: "missing", URL: srv.URL + "/no", Contains: "status=ok"},
			{Name: "badstatus", URL: srv.URL + "/err", Contains: "status=ok", StatusOK: []int{200}},
		}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	_ = h.Collect(context.Background(), reg, time.Unix(50, 0))
	if _, v := mustChart(t, reg, "httpcheck.status.ok").LastValues(); v["success"] != 1 || v["failure"] != 0 {
		t.Fatalf("match %+v", v)
	}
	if _, v := mustChart(t, reg, "httpcheck.status.missing").LastValues(); v["success"] != 0 || v["failure"] != 1 {
		t.Fatalf("missing text %+v", v)
	}
	if _, v := mustChart(t, reg, "httpcheck.status.badstatus").LastValues(); v["success"] != 0 || v["failure"] != 1 {
		t.Fatalf("bad status %+v", v)
	}
	_, code := mustChart(t, reg, "httpcheck.status_code.badstatus").LastValues()
	if code["code"] != 500 {
		t.Fatalf("status code %+v", code)
	}

	long := &httpcheckCollector{}
	if err := long.Configure(func(v any) error {
		*v.(*httpcheckConfig) = httpcheckConfig{Jobs: []httpJob{{Name: "long", URL: "http://127.0.0.1/", Contains: strings.Repeat("a", 257)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := long.Init(reg); err == nil || !strings.Contains(err.Error(), "contains") {
		t.Fatalf("long contains: %v", err)
	}
}
