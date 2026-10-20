package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoadingGateServesUntilReady(t *testing.T) {
	gate := &loadingGate{}
	srv := httptest.NewServer(gate)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable || string(body) != "loading\n" {
		t.Fatalf("healthz while loading: %d %q", resp.StatusCode, body)
	}
	if resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("retry-after %q", resp.Header.Get("Retry-After"))
	}

	if _, err = http.Get(srv.URL + "/api/v1/info"); err == nil {
		t.Fatal("api request during load should fail the dial, not return a status")
	}

	gate.use(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok\n"))
			return
		}
		http.NotFound(w, r)
	}))
	resp, err = http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
		t.Fatalf("healthz ready: %d %q", resp.StatusCode, body)
	}
}
