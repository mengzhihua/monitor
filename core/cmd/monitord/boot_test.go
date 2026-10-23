package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPShutdownCancelsRequestsAfterDeadline(t *testing.T) {
	entered, canceled := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(canceled)
	}))
	t.Cleanup(srv.Close)
	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		resp, err := srv.Client().Get(srv.URL)
		if err == nil {
			resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := stopHTTPServer(ctx, srv.Config); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected drain deadline, got %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("expired shutdown retained active request")
	}
	select {
	case <-clientDone:
	case <-time.After(time.Second):
		t.Fatal("expired shutdown retained client connection")
	}
}

func TestHTTPShutdownLetsActiveRequestFinish(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	draining := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-release:
			_, _ = io.WriteString(w, "finished")
		case <-r.Context().Done():
		}
	}))
	srv.Config.RegisterOnShutdown(func() { close(draining) })
	t.Cleanup(srv.Close)
	result := make(chan string, 1)
	go func() {
		resp, err := srv.Client().Get(srv.URL)
		if err != nil {
			result <- "failed"
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		result <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("request did not reach handler")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- stopHTTPServer(ctx, srv.Config) }()
	select {
	case <-draining:
	case <-time.After(time.Second):
		t.Fatal("server did not start draining")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdown returned before active request completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if body := <-result; body != "finished" {
		t.Fatalf("drain lost active response: %q", body)
	}
}

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
