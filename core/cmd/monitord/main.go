// monitord is the Monitor agent: it collects host metrics every second, stores
// them in the embedded TSDB and serves the API + dashboard over HTTP.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

var version = "dev" // set via -ldflags "-X main.version=..."

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "monitord:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "monitor.yaml", "path to config file")
	listen := flag.String("listen", "", "override web.listen (e.g. :19999)")
	dataDir := flag.String("data-dir", "", "override global.data_dir")
	logLevel := flag.String("log-level", "info", "debug|info|warn|error")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Printf("monitord %s %s/%s\n", version, runtime.GOOS, runtime.GOARCH)
		return nil
	}

	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(*logLevel)); err != nil {
		return fmt.Errorf("bad -log-level: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	if *listen != "" {
		cfg.Web.Listen = *listen
	}
	if *dataDir != "" {
		cfg.Global.DataDir = *dataDir
	}
	if err := os.MkdirAll(cfg.Global.DataDir, 0o755); err != nil {
		return err
	}

	h, err := hostIdentity(cfg)
	if err != nil {
		return err
	}
	log.Info("monitord starting", "version", version, "host", h.Hostname, "os", h.OS, "arch", h.Arch, "update_every", h.UpdateEvery)

	retSize, err := config.ParseSize(cfg.DB.Tier0RetentionSize)
	if err != nil {
		return err
	}
	db, err := tsdb.Open(tsdb.Options{
		Dir:           filepath.Join(cfg.Global.DataDir, "db"),
		Retention:     cfg.DB.Tier0Retention,
		RetentionSize: retSize,
		Checkpoint:    cfg.DB.Checkpoint,
		Logger:        log.With("component", "tsdb"),
	})
	if err != nil {
		return fmt.Errorf("open tsdb: %w", err)
	}

	reg := registry.New(h, db)
	disabled := map[string]bool{}
	for _, n := range cfg.Collectors.Disabled {
		disabled[n] = true
	}
	sched := collect.NewScheduler(reg, log.With("component", "collect"), cfg.Collectors.Enabled, disabled)

	srv, err := api.New(reg, db, sched, api.Options{
		Version:   version,
		StartedAt: time.Now(),
		AllowFrom: cfg.Web.AllowFrom,
		Token:     cfg.Web.Token,
		Logger:    log.With("component", "api"),
	})
	if err != nil {
		return err
	}
	httpSrv := &http.Server{
		Addr:              cfg.Web.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go sched.Run(ctx)

	errc := make(chan error, 1)
	go func() {
		log.Info("web server listening", "addr", cfg.Web.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case err := <-errc:
		stop()
		log.Error("web server failed", "err", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if err := db.Close(); err != nil {
		log.Error("tsdb close", "err", err)
	}
	return nil
}

func hostIdentity(cfg *config.Config) (*registry.Host, error) {
	info, err := host.Info()
	if err != nil {
		return nil, fmt.Errorf("host info: %w", err)
	}
	name := cfg.Global.Hostname
	if name == "" {
		name = info.Hostname
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	id := info.HostID
	if id == "" {
		id = name
	}
	return &registry.Host{
		ID:          id,
		Hostname:    name,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		UpdateEvery: cfg.Global.UpdateEvery,
		Labels: map[string]string{
			"_os_name":        info.Platform,
			"_os_version":     info.PlatformVersion,
			"_kernel":         info.KernelVersion,
			"_virtualization": strings.TrimSpace(info.VirtualizationSystem + " " + info.VirtualizationRole),
			"_agent_version":  version,
		},
	}, nil
}
