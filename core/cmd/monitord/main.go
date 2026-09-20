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
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/plugins"
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
	tiers := tsdb.DefaultTiers()
	if cfg.DB.Tiers < 1 || cfg.DB.Tiers > len(tiers)+1 {
		return fmt.Errorf("db.tiers must be between 1 and %d", len(tiers)+1)
	}
	tiers = tiers[:cfg.DB.Tiers-1]
	for i, ret := range []time.Duration{cfg.DB.Tier1Retention, cfg.DB.Tier2Retention} {
		if i < len(tiers) {
			tiers[i].Retention = ret
		}
	}
	db, err := tsdb.Open(tsdb.Options{
		Dir:           filepath.Join(cfg.Global.DataDir, "db"),
		Retention:     cfg.DB.Tier0Retention,
		RetentionSize: retSize,
		Checkpoint:    cfg.DB.Checkpoint,
		Tiers:         tiers,
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

	var eng *health.Engine
	if cfg.HealthEnabled() {
		eng, err = newHealth(cfg, *cfgPath, reg, db, log.With("component", "health"))
		if err != nil {
			return fmt.Errorf("health: %w", err)
		}
	}

	var pm *plugins.Manager
	if cfg.PluginsEnabled() {
		pdir := cfg.Plugins.Dir
		if pdir != "" && !filepath.IsAbs(pdir) {
			pdir = filepath.Join(filepath.Dir(*cfgPath), pdir)
		}
		pm = plugins.New(reg, cfg.Plugins.List, plugins.Options{
			Dir:      pdir,
			Disabled: cfg.Plugins.Disabled,
			Logger:   log.With("component", "plugins"),
		})
	}

	srv, err := api.New(reg, db, sched, api.Options{
		Version:   version,
		StartedAt: time.Now(),
		AllowFrom: cfg.Web.AllowFrom,
		Token:     cfg.Web.Token,
		Health:    eng,
		Plugins:   pm,
		Logger:    log.With("component", "api"),
	})
	if err != nil {
		return err
	}
	if eng != nil {
		eng.SetOnEvent(srv.PublishAlarm)
	}
	httpSrv := &http.Server{
		Addr:              cfg.Web.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	schedDone := make(chan struct{})
	go func() {
		defer close(schedDone)
		sched.Run(ctx)
	}()
	healthDone := make(chan struct{})
	go func() {
		defer close(healthDone)
		if eng != nil {
			eng.Run(ctx)
		}
	}()
	pluginsDone := make(chan struct{})
	go func() {
		defer close(pluginsDone)
		if pm != nil {
			pm.Run(ctx)
		}
	}()

	errc := make(chan error, 1)
	go func() {
		log.Info("web server listening", "addr", cfg.Web.Listen)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	var runErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case runErr = <-errc:
		stop()
		log.Error("web server failed", "err", runErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	select {
	case <-schedDone:
	case <-shutdownCtx.Done():
		log.Warn("collectors did not stop in time")
	}
	select {
	case <-healthDone:
	case <-shutdownCtx.Done():
		log.Warn("health engine did not stop in time")
	}
	select {
	case <-pluginsDone:
	case <-shutdownCtx.Done():
		log.Warn("plugins did not stop in time")
	}
	if err := db.Close(); err != nil {
		log.Error("tsdb close", "err", err)
	}
	return runErr
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

func newHealth(cfg *config.Config, cfgPath string, reg *registry.Registry, db *tsdb.Store, log *slog.Logger) (*health.Engine, error) {
	var sets [][]*health.Rule
	if cfg.HealthBuiltin() {
		builtin, err := health.DefaultRules()
		if err != nil {
			return nil, err
		}
		sets = append(sets, builtin)
	}
	dir := cfg.Health.Dir
	if dir != "" && !filepath.IsAbs(dir) {
		dir = filepath.Join(filepath.Dir(cfgPath), dir)
	}
	if dir != "" {
		custom, err := health.LoadDir(dir)
		if err != nil {
			return nil, err
		}
		sets = append(sets, custom)
	}
	inline, err := health.CompileAll(cfg.Health.Alarms, cfgPath)
	if err != nil {
		return nil, err
	}
	sets = append(sets, inline)
	rules := health.Merge(sets...)

	var notifiers []health.Notifier
	n := cfg.Health.Notify
	if n.Webhook.URL != "" {
		notifiers = append(notifiers, &health.WebhookNotifier{URL: n.Webhook.URL, Headers: n.Webhook.Headers})
	}
	if n.Slack.WebhookURL != "" {
		notifiers = append(notifiers, &health.SlackNotifier{WebhookURL: n.Slack.WebhookURL, Channel: n.Slack.Channel})
	}
	if n.Email.Server != "" && len(n.Email.To) > 0 {
		notifiers = append(notifiers, &health.EmailNotifier{Server: n.Email.Server, From: n.Email.From, To: n.Email.To,
			Username: n.Email.Username, Password: n.Email.Password, Insecure: n.Email.Insecure})
	}

	vars := map[string]float64{"cpus": float64(runtime.NumCPU())}
	if vm, err := mem.VirtualMemory(); err == nil {
		vars["ram_total"] = float64(vm.Total) / (1024 * 1024)
	}
	log.Info("health engine", "rules", len(rules), "notifiers", len(notifiers), "silent", cfg.Health.Silent)
	return health.New(reg, db, health.Options{
		Rules:      rules,
		Hostname:   reg.Host.Hostname,
		LogDir:     filepath.Join(cfg.Global.DataDir, "health"),
		LogKeep:    cfg.Health.LogKeep,
		Notifiers:  notifiers,
		Roles:      n.Roles,
		HostVars:   vars,
		Logger:     log,
		SilenceAll: cfg.Health.Silent,
	})
}
