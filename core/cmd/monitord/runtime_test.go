package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/config"
)

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
