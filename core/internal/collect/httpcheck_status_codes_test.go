package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestHttpcheckStatusCodesRange(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	h := &httpcheckCollector{cfg: httpcheckConfig{Jobs: []httpJob{{
		Name:        "ok",
		URL:         srv.URL,
		StatusCodes: []string{"200-299"},
	}}}}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.probe(context.Background(), reg, time.Unix(1, 0), h.cfg.Jobs[0]); err != nil {
		t.Fatal(err)
	}
	ch, _ := reg.Chart("httpcheck.status.ok")
	_, vals := ch.LastValues()
	if vals["success"] != 1 {
		t.Fatalf("success=%v", vals["success"])
	}
}

func TestHttpcheckStatusCodesReject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	h := &httpcheckCollector{cfg: httpcheckConfig{Jobs: []httpJob{{
		Name:        "bad",
		URL:         srv.URL,
		StatusCodes: []string{"200"},
	}}}}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.probe(context.Background(), reg, time.Unix(1, 0), h.cfg.Jobs[0]); err == nil {
		t.Fatal("expected status error")
	}
}

func TestHttpcheckFollowRedirects(t *testing.T) {
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
	}))
	defer final.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, final.URL, http.StatusFound)
	}))
	defer redirect.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	follow := true
	h := &httpcheckCollector{cfg: httpcheckConfig{Jobs: []httpJob{{
		Name:            "redir",
		URL:             redirect.URL,
		StatusCodes:     []string{"200"},
		FollowRedirects: &follow,
	}}}}
	if err := h.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := h.probe(context.Background(), reg, time.Unix(1, 0), h.cfg.Jobs[0]); err != nil {
		t.Fatalf("follow redirects: %v", err)
	}
}

func TestParseStatusCodesRejectsBadRange(t *testing.T) {
	if _, err := parseStatusCodes([]string{"200-99"}); err == nil {
		t.Fatal("expected error")
	}
}
