package collect

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestWebscenarioManualCookieSkipsJar(t *testing.T) {
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "from-jar", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/home":
			seen = append(seen, r.Header.Get("Cookie"))
			if strings.Count(r.Header.Get("Cookie"), "sid=") != 1 {
				http.Error(w, "duplicate cookie", 400)
				return
			}
			if c, _ := r.Cookie("sid"); c == nil || c.Value != "manual" {
				http.Error(w, "want manual sid", 401)
				return
			}
			_, _ = w.Write([]byte("home"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{Hostname: "ws", UpdateEvery: 1}, db)
	ws := &webscenarioCollector{}
	if err := ws.Configure(func(v any) error {
		*v.(*webscenarioConfig) = webscenarioConfig{Jobs: []webscenarioJob{{Name: "login", Steps: []webscenarioStep{
			{URL: srv.URL + "/login"},
			{URL: srv.URL + "/home", Headers: map[string]string{"Cookie": "sid=manual"}},
		}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := ws.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ws.Collect(context.Background(), reg, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "webscenario.status.login").LastValues(); v["success"] != 1 {
		t.Fatalf("scenario failed cookies=%v values=%v", seen, v)
	}
	if len(seen) != 1 || seen[0] != "sid=manual" {
		t.Fatalf("cookie header %q", seen)
	}
}

func TestWebscenarioJarUsedWithoutManualCookie(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			http.SetCookie(w, &http.Cookie{Name: "sid", Value: "from-jar", Path: "/"})
			_, _ = w.Write([]byte("ok"))
		case "/home":
			if c, _ := r.Cookie("sid"); c == nil || c.Value != "from-jar" {
				http.Error(w, "missing jar cookie", 401)
				return
			}
			_, _ = w.Write([]byte("home"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{Hostname: "ws", UpdateEvery: 1}, db)
	ws := &webscenarioCollector{}
	if err := ws.Configure(func(v any) error {
		*v.(*webscenarioConfig) = webscenarioConfig{Jobs: []webscenarioJob{{Name: "jar", Steps: []webscenarioStep{
			{URL: srv.URL + "/login"},
			{URL: srv.URL + "/home"},
		}}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := ws.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := ws.Collect(context.Background(), reg, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if _, v := mustChart(t, reg, "webscenario.status.jar").LastValues(); v["success"] != 1 {
		t.Fatalf("%+v", v)
	}
}
