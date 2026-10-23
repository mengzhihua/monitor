package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"gopkg.in/yaml.v3"

	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/config"
)

func TestFailedStartupClosesInitializedCollectorListeners(t *testing.T) {
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	statsdAddress := udp.LocalAddr().String()
	udp.Close()
	cfg := config.Default()
	cfg.Global.DataDir = t.TempDir()
	cfg.Web.Listen = unusedListenAddress(t)
	otlpAddress := unusedListenAddress(t)
	for otlpAddress == cfg.Web.Listen {
		otlpAddress = unusedListenAddress(t)
	}
	cfg.Collectors.Enabled = []string{"statsd", "otlp"}
	cfg.Collectors.Modules = map[string]yaml.Node{}
	for name, address := range map[string]string{"statsd": statsdAddress, "otlp": otlpAddress} {
		var node yaml.Node
		if err := node.Encode(map[string]string{"listen": address}); err != nil {
			t.Fatal(err)
		}
		cfg.Collectors.Modules[name] = node
	}
	disabled := false
	cfg.Health.Enabled, cfg.Plugins.Enabled = &disabled, &disabled
	cfg.Stream.Enabled, cfg.Stream.Destinations = true, nil
	for range 2 {
		var logs bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logs, nil))
		err := runDaemon(context.Background(), cfg, filepath.Join(cfg.Global.DataDir, "monitor.yaml"), false, logger)
		if err == nil || !strings.Contains(err.Error(), "stream.destinations") {
			t.Fatalf("expected post-collector startup failure, got %v", err)
		}
		if strings.Contains(logs.String(), "collector: disabled") {
			t.Fatal("fixture did not successfully initialize its collectors")
		}
		udp, err := net.ListenPacket("udp", statsdAddress)
		if err != nil {
			t.Fatalf("startup failure retained StatsD listener: %v", err)
		}
		udp.Close()
		tcp, err := net.Listen("tcp", otlpAddress)
		if err != nil {
			t.Fatalf("startup failure retained OTLP listener: %v", err)
		}
		tcp.Close()
	}
}

func TestDaemonCancellationReleasesLiveConnectionsAndData(t *testing.T) {
	cfg := config.Default()
	cfg.Global.DataDir = t.TempDir()
	cfg.Web.Listen = unusedListenAddress(t)
	cfg.Trapper.Listen = unusedListenAddress(t)
	for cfg.Trapper.Listen == cfg.Web.Listen {
		cfg.Trapper.Listen = unusedListenAddress(t)
	}
	cfg.Collectors.Enabled = []string{"none"}
	disabled := false
	cfg.Health.Enabled = &disabled
	cfg.Plugins.Enabled = &disabled
	cfg.Reports.Every = time.Hour
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	client := &http.Client{Timeout: 250 * time.Millisecond}
	t.Cleanup(client.CloseIdleConnections)
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		finished := make(chan struct{})
		var runErr error
		go func() {
			defer close(finished)
			runErr = runDaemon(ctx, cfg, filepath.Join(cfg.Global.DataDir, "monitor.yaml"), false, logger)
		}()
		t.Cleanup(func() {
			cancel()
			select {
			case <-finished:
			case <-time.After(10 * time.Second):
				t.Error("daemon cleanup did not finish")
			}
		})
		deadline := time.Now().Add(10 * time.Second)
		for {
			resp, err := client.Get("http://" + cfg.Web.Listen + "/healthz")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					break
				}
			}
			select {
			case <-finished:
				t.Fatalf("daemon stopped before readiness: %v", runErr)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("daemon did not become ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		token, err := os.ReadFile(filepath.Join(cfg.Global.DataDir, "web-password"))
		if err != nil {
			t.Fatal(err)
		}
		headers := http.Header{"Authorization": {"Bearer " + strings.TrimSpace(string(token))}}
		ws, _, err := websocket.DefaultDialer.Dial("ws://"+cfg.Web.Listen+"/api/v1/live", headers)
		if err != nil {
			t.Fatal("live connection failed")
		}
		t.Cleanup(func() { _ = ws.Close() })
		sender, err := net.DialTimeout("tcp", cfg.Trapper.Listen, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sender.Close() })
		// Keep a sender blocked mid-frame while stopping the real daemon.
		if _, err := sender.Write([]byte{'Z', 'B'}); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case <-finished:
			if runErr != nil {
				t.Fatal(runErr)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("daemon cancellation did not finish")
		}
		_ = ws.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		for {
			_, _, err := ws.ReadMessage()
			if err == nil {
				continue // drain samples queued before cancellation
			}
			if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
				t.Fatal("daemon retained live websocket after shutdown")
			}
			break
		}
		_ = sender.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
		_, err = sender.Read(make([]byte, 1))
		if timeout, ok := err.(net.Error); err == nil || ok && timeout.Timeout() {
			t.Fatal("daemon retained sender connection after shutdown")
		}
		for _, address := range []string{cfg.Web.Listen, cfg.Trapper.Listen} {
			listener, err := net.Listen("tcp", address)
			if err != nil {
				t.Fatalf("listener not reusable: %v", err)
			}
			listener.Close()
		}
		lock, err := backup.Lock(cfg.Global.DataDir)
		if err != nil {
			t.Fatalf("data lock retained: %v", err)
		}
		lock.Close()
	}
}

func unusedListenAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().String()
}

// Fail before and after API construction. Repeated starts must not retain
// database/API workers or the listening socket/data lock.
func TestFailedStartupReleasesResources(t *testing.T) {
	for _, stage := range []string{"stream", "trapper"} {
		t.Run(stage, func(t *testing.T) { testFailedStartupReleasesResources(t, stage) })
	}
}

func testFailedStartupReleasesResources(t *testing.T, stage string) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	cfg := config.Default()
	cfg.Global.DataDir = t.TempDir()
	cfg.Web.Listen = address
	wantError := "stream.destinations"
	if stage == "stream" {
		cfg.Stream.Enabled = true
		cfg.Stream.Destinations = nil
	} else {
		occupied, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer occupied.Close()
		cfg.Trapper.Listen = occupied.Addr().String()
		cfg.Reports.Every = time.Hour
		wantError = "trapper:"
	}
	cfg.Collectors.Enabled = []string{"mem"}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	before := resourceWorkers()
	for range 3 {
		err = runDaemon(context.Background(), cfg, filepath.Join(t.TempDir(), "monitor.yaml"), false, logger)
		if err == nil || !strings.Contains(err.Error(), wantError) {
			t.Fatalf("expected %s startup failure, got %v", stage, err)
		}
		lock, err := backup.Lock(cfg.Global.DataDir)
		if err != nil {
			t.Fatalf("startup retained data lock: %v", err)
		}
		lock.Close()
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("startup retained listener: %v", err)
		}
		listener.Close()
	}
	deadline := time.Now().Add(time.Second)
	for resourceWorkers() > before && time.Now().Before(deadline) {
		runtime.Gosched()
		time.Sleep(time.Millisecond)
	}
	if after := resourceWorkers(); after > before {
		t.Fatalf("failed starts leaked database/notification workers: before=%d after=%d", before, after)
	}
}

func resourceWorkers() int {
	buf := make([]byte, 1<<20)
	stack := string(buf[:runtime.Stack(buf, true)])
	return strings.Count(stack, "tsdb.(*Store).loop(") + strings.Count(stack, "health.(*Engine).dispatch(") +
		strings.Count(stack, "api.(*Server).selfMonitor(") + strings.Count(stack, "api.(*Server).exportReports(")
}
