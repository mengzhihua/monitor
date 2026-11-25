package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestShutdownWatchdogDoesNotLimitUptimeOrOutliveDrain(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		exited := make(chan int, 1)
		stop := watchShutdown(ctx, 20*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)), func(code int) { exited <- code })
		if canceled {
			cancel()
		} else {
			// A running daemon must not have a lifetime limit.
			time.Sleep(50 * time.Millisecond)
		}
		stop()
		cancel()
		time.Sleep(30 * time.Millisecond)
		select {
		case code := <-exited:
			t.Fatalf("watchdog exited a healthy daemon: canceled=%v code=%d", canceled, code)
		default:
		}
	}
}

type blockedShutdownLog struct{ release <-chan struct{} }

func (w blockedShutdownLog) Write(p []byte) (int, error) {
	<-w.release
	return len(p), nil
}

func TestShutdownWatchdogExitsEvenWhenLoggingBlocks(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	exited := make(chan int, 1)
	stop := watchShutdown(ctx, time.Millisecond, slog.New(slog.NewTextHandler(blockedShutdownLog{release}, nil)), func(code int) { exited <- code })
	defer stop()
	cancel()
	select {
	case code := <-exited:
		if code != 2 {
			t.Fatalf("exit code = %d, want 2", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked logging prevented forced exit")
	}
}

// Run the real composition root in a subprocess: a goroutine-only timeout test
// would not prove that a stuck scheduler and its deferred Close can be escaped.
func TestDaemonShutdownDeadline(t *testing.T) {
	if phase := os.Getenv("MONITOR_TEST_SHUTDOWN_PHASE"); phase != "" {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		cfg := config.Default()
		cfg.Global.DataDir = os.Getenv("MONITOR_TEST_SHUTDOWN_DATA")
		disabled := false
		cfg.Web.Enabled, cfg.Health.Enabled, cfg.Plugins.Enabled = &disabled, &disabled, &disabled
		cfg.Web.Listen = ""
		cfg.Collectors.Enabled = []string{"shutdown-fixture"}
		if phase == "deferred-stop" {
			// Fail after collector initialization, before workers start. Only
			// deferred cleanup can reach this collector's Stop method.
			cfg.Stream.Enabled, cfg.Stream.Destinations = true, nil
		}
		collect.Register("shutdown-fixture", func() collect.Collector {
			return &shutdownFixture{phase: phase, cancel: cancel, dataDir: cfg.Global.DataDir}
		})
		log := slog.New(slog.NewTextHandler(os.Stderr, nil))
		limit := 250 * time.Millisecond
		if phase == "normal" {
			limit = 5 * time.Second
		}
		if err := runDaemonWithDeadline(ctx, cfg, filepath.Join(cfg.Global.DataDir, "monitor.yaml"), false, log, limit); err != nil {
			t.Fatal(err)
		}
		if phase != "normal" {
			t.Fatal("blocked daemon returned without the process deadline")
		}
		return
	}
	for _, phase := range []string{"init", "collect", "stop", "deferred-stop", "normal"} {
		t.Run(phase, func(t *testing.T) {
			dataDir := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDaemonShutdownDeadline$")
			cmd.Env = append(os.Environ(), "MONITOR_TEST_SHUTDOWN_PHASE="+phase, "MONITOR_TEST_SHUTDOWN_DATA="+dataDir)
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			err := cmd.Run()
			if ctx.Err() != nil {
				t.Fatalf("daemon exceeded subprocess deadline: %s", output.String())
			}
			if phase == "normal" {
				if err != nil || strings.Contains(output.String(), "forcing process exit") {
					t.Fatalf("normal stop failed: %v\n%s", err, output.String())
				}
			} else {
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 2 || !strings.Contains(output.String(), "shutdown deadline exceeded") {
					t.Fatalf("expected bounded unclean exit 2: %v\n%s", err, output.String())
				}
			}
			lock, err := backup.Lock(dataDir)
			if err != nil {
				t.Fatalf("terminated process retained database lock: %v", err)
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

type shutdownFixture struct {
	phase   string
	cancel  context.CancelFunc
	dataDir string
}

func (*shutdownFixture) Name() string { return "shutdown-fixture" }
func (f *shutdownFixture) block(phase string) {
	if f.phase != phase {
		return
	}
	// The database must remain exclusively owned until the process exits.
	if lock, err := backup.Lock(f.dataDir); err == nil {
		lock.Close()
		os.Exit(3)
	}
	f.cancel()
	select {}
}
func (f *shutdownFixture) Init(*registry.Registry) error {
	f.block("init")
	return nil
}
func (f *shutdownFixture) Collect(context.Context, *registry.Registry, time.Time) error {
	f.block("collect")
	f.cancel()
	return nil
}
func (f *shutdownFixture) Stop() {
	f.block("stop")
	f.block("deferred-stop")
}
