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

func TestWebScenarioRequiresResponseText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/start":
			_, _ = w.Write([]byte("token=abc"))
		case "/ok":
			if r.URL.Query().Get("token") != "abc" {
				http.Error(w, "missing", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte("hello abc"))
		case "/wrong":
			_, _ = w.Write([]byte("goodbye"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	w := &webscenarioCollector{}
	if err := w.Configure(func(v any) error {
		*v.(*webscenarioConfig) = webscenarioConfig{Jobs: []webscenarioJob{{
			Name: "login",
			Steps: []webscenarioStep{
				{URL: srv.URL + "/start", Extract: `token=([a-z]+)`, Var: "token"},
				{URL: srv.URL + "/ok?token={{token}}", Required: "hello {{token}}"},
			},
		}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := w.Collect(context.Background(), reg, time.Unix(30, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "webscenario.status.login").LastValues(); v["success"] != 1 || v["failed_step"] != 0 {
		t.Fatalf("required text %+v", v)
	}

	w.cfg.Jobs[0].Steps[1].URL = srv.URL + "/wrong"
	w.cfg.Jobs[0].Steps[1].Required = "welcome"
	if err := w.Collect(context.Background(), reg, time.Unix(31, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "webscenario.status.login").LastValues(); v["success"] != 0 || v["failed_step"] != 2 {
		t.Fatalf("missing text %+v", v)
	}

	w.cfg.Jobs[0].Steps[1].URL = srv.URL + "/ok?token={{token}}"
	w.cfg.Jobs[0].Steps[1].Required = "welcome"
	w.cfg.Jobs[0].Steps[1].Expect = 201
	if err := w.Collect(context.Background(), reg, time.Unix(32, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "webscenario.status.login").LastValues(); v["success"] != 0 || v["failed_step"] != 2 {
		t.Fatalf("status before text %+v", v)
	}

	long := strings.Repeat("a", 257)
	err := w.Configure(func(v any) error {
		*v.(*webscenarioConfig) = webscenarioConfig{Jobs: []webscenarioJob{{
			Name:  "login",
			Steps: []webscenarioStep{{URL: "http://127.0.0.1/", Required: long}},
		}}}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("long required: %v", err)
	}
}
