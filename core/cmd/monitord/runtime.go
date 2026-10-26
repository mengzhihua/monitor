package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/api"
	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/discover"
	"github.com/mengzhihua/monitor/core/internal/export"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/trapper"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// runDaemon owns startup, workers, shutdown and restart. Configuration adapters
// construct the domain components; every acquired resource has an error-path cleanup.
func runDaemon(parent context.Context, cfg *config.Config, cfgPath string, forceWeb bool, log *slog.Logger) error {
	switch cfg.Mode {
	case "", "agent", "hub":
	default:
		return fmt.Errorf("mode must be agent or hub, got %q", cfg.Mode)
	}
	if err := os.MkdirAll(cfg.Global.DataDir, 0o755); err != nil {
		return err
	}

	lock, err := backup.Lock(cfg.Global.DataDir)
	if err != nil {
		return err
	}
	defer lock.Close()
	passwordFile, err := cfg.EnsureWebAuth()
	if err != nil {
		return fmt.Errorf("web authentication: %w", err)
	}
	if passwordFile != "" {
		log.Warn("dashboard login required; read the password file on this server", "password_file", passwordFile)
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
	storage, err := storageOptions(cfg, retSize, log.With("component", "tsdb"))
	if err != nil {
		return err
	}
	// Bind before opening the database. A few hundred megabytes of blocks
	// can take tens of seconds; the port should answer (503 loading) instead
	// of refusing connections until that finishes.
	var httpSrv *http.Server
	var httpListener net.Listener
	var httpDone chan struct{}
	gate := &loadingGate{}
	errc := make(chan error, 1)
	serverHandoff := false
	if cfg.WebEnabled() || forceWeb {
		ln, err := net.Listen("tcp", cfg.Web.Listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.Web.Listen, err)
		}
		httpListener, httpDone = ln, make(chan struct{})
		httpSrv = &http.Server{
			Addr:              cfg.Web.Listen,
			Handler:           gate,
			ReadHeaderTimeout: 10 * time.Second,
		}
		log.Info("web server listening", "addr", cfg.Web.Listen, "ready", false)
		go func() {
			defer close(httpDone)
			if err := httpSrv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				errc <- err
			}
		}()
	}
	defer func() {
		if httpSrv != nil && !serverHandoff {
			stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			_ = stopHTTPServer(stopCtx, httpSrv)
			cancel()
		}
		if httpListener != nil {
			// Shutdown only knows listeners already registered by Serve. A fast
			// startup failure can reach here before that goroutine has started.
			_ = httpListener.Close()
			<-httpDone
		}
	}()

	openStarted := time.Now()
	db, err := tsdb.Open(storage)
	if err != nil {
		return fmt.Errorf("open tsdb: %w", err)
	}
	log.Info("tsdb open", "elapsed", time.Since(openStarted).Round(time.Millisecond))
	defer db.Close()

	reg := registry.New(h, db)
	localDisabled := map[string]bool{}
	for _, n := range cfg.Collectors.Disabled {
		localDisabled[n] = true
	}
	collectOpt := collectorOptions(cfg, localDisabled)
	collectOpt.History = db
	sched := collect.NewScheduler(reg, log.With("component", "collect"), collectOpt)
	defer sched.Close()

	runner := &autoCommandRunner{commands: cfg.Commands, log: log.With("component", "auto-action")}
	var eng *health.Engine
	if cfg.HealthEnabled() {
		eng, err = newHealth(cfg, cfgPath, reg, db, log.With("component", "health"), runner)
		if err != nil {
			return fmt.Errorf("health: %w", err)
		}
		defer func() {
			if !serverHandoff {
				eng.Close()
			}
		}()
		if ml := sched.Collector("ml"); ml != nil {
			if src, ok := ml.(health.AnomalySource); ok {
				eng.SetAnomaly(src)
			}
		}
	}

	var pm *plugins.Manager
	if cfg.PluginsEnabled() {
		pdir := cfg.Plugins.Dir
		if pdir != "" && !filepath.IsAbs(pdir) {
			pdir = filepath.Join(filepath.Dir(cfgPath), pdir)
		}
		pm = plugins.New(reg, cfg.Plugins.List, pluginRuntimeOptions(cfg, pdir, log.With("component", "plugins")))
		if err := pm.Err(); err != nil {
			return fmt.Errorf("plugins: %w", err)
		}
	}

	var sc *stream.Client
	ctx, stop := context.WithCancel(parent)
	defer stop()
	restartReq := make(chan struct{}, 1)
	requestRestart := func() error {
		select {
		case restartReq <- struct{}{}:
		default:
		}
		return nil
	}
	// requestExit schedules a restart once, after a short
	// delay that lets pending WebSocket frames (e.g. a config apply ack)
	// flush before the connection drops.
	var exitOnce sync.Once
	requestExit := func() {
		exitOnce.Do(func() {
			time.AfterFunc(2*time.Second, func() { _ = requestRestart() })
		})
	}
	sc, err = newAgentStream(cfg, cfgPath, reg, db, sched, eng, localDisabled, log, requestExit)
	if err != nil {
		return err
	}

	if err := collect.OpenChecks(filepath.Join(cfg.Global.DataDir, "checks")); err != nil {
		return fmt.Errorf("external checks: %w", err)
	}

	var auditLog *audit.Log
	if cfg.AuditEnabled() {
		auditLog, err = audit.Open(filepath.Join(cfg.Global.DataDir, "operations"), cfg.Web.Audit.MaxEntries)
		if err != nil {
			return fmt.Errorf("audit log: %w", err)
		}
		defer auditLog.Close()
	}
	runner.audit = auditLog

	apiOpt := serverOptions(cfg, cfgPath, eng, pm, sc, auditLog, log)

	var srv *api.Server
	nodes, cluster, err := configureHub(cfg, db, &apiOpt, &srv, log)
	if err != nil {
		return err
	}
	if eng != nil && apiOpt.Elector != nil {
		eng.SetActive(apiOpt.Elector.IsLeader)
	}
	cfgAbs, err := filepath.Abs(cfgPath)
	if err != nil {
		return err
	}
	apiOpt.ConfigPath = cfgAbs
	apiOpt.RequestRestart = requestRestart
	srv, err = api.New(reg, db, sched, apiOpt)
	if err != nil {
		return err
	}
	defer srv.Close()
	runner.record = srv.RecordAutoAction
	runner.recordInhibited = srv.RecordInhibitedAction
	var trapperListener net.Listener
	if cfg.Trapper.Listen != "" {
		trapperListener, err = trapper.Start(ctx.Done(), cfg.Trapper.Listen, cfg.Trapper.Allowed, cfg.Trapper.PSK, reg)
		if err != nil {
			return fmt.Errorf("trapper: %w", err)
		}
		defer trapperListener.Close()
	}
	if eng != nil {
		eng.SetOnEvent(func(e health.LogEntry) {
			srv.PublishAlarm(e)
			if sc != nil {
				sc.PublishAlarm(e)
			}
		})
	}
	if httpSrv != nil {
		gate.use(srv.Handler())
		log.Info("web server ready", "addr", cfg.Web.Listen)
	} else {
		log.Info("web server disabled (web.enabled=false)")
	}

	workers := &workerGroup{}
	workers.start(ctx, "collectors", sched.Run)
	if eng != nil {
		workers.start(ctx, "health engine", eng.Run)
	}
	if pm != nil {
		workers.start(ctx, "plugins", pm.Run)
	}
	if sc != nil {
		workers.start(ctx, "stream", sc.Run)
	}
	if nodes != nil {
		workers.start(ctx, "hub", nodes.Run)
	}
	if cluster != nil {
		workers.start(ctx, "cluster", cluster.Run)
	}
	if exp := export.New(reg, cfg.Export.Destinations, log.With("component", "export")); !exp.Empty() {
		workers.start(ctx, "export", exp.Run)
	}
	if port := discover.Port(cfg.Web.Listen); port > 0 {
		workers.start(ctx, "discovery", func(ctx context.Context) {
			discover.Announce(ctx, reg.Host.Hostname, port)
		})
	}

	serverHandoff = true
	var runErr error
	restarting := false
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case <-restartReq:
		restarting = true
		stop()
		log.Info("restart requested")
	case runErr = <-errc:
		stop()
		log.Error("web server failed", "err", runErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if trapperListener != nil {
		_ = trapperListener.Close()
	}
	srv.CloseShells()
	if err := stopHTTPServer(shutdownCtx, httpSrv); err != nil {
		log.Warn("HTTP drain deadline exceeded; connections closed", "err", err)
	}
	srv.Close()
	workers.wait(shutdownCtx, log)
	sched.Close()
	if err := db.Close(); err != nil {
		log.Error("tsdb close", "err", err)
	}
	if restarting && runErr == nil {
		if err := lock.Close(); err != nil {
			log.Error("release data lock", "err", err)
		}
		log.Info("re-executing monitord")
		if err := reexec(); err != nil {
			return err
		}
		return nil
	}
	return runErr
}

// storageOptions maps db.* onto the engine. A zero block size keeps the
// tsdb default for that tier; rollup sizes apply only to tiers that are enabled.
func storageOptions(cfg *config.Config, retSize int64, log *slog.Logger) (tsdb.Options, error) {
	tiers := tsdb.DefaultTiers()
	if cfg.DB.Tiers < 1 || cfg.DB.Tiers > len(tiers)+1 {
		return tsdb.Options{}, fmt.Errorf("db.tiers must be between 1 and %d", len(tiers)+1)
	}
	tiers = tiers[:cfg.DB.Tiers-1]
	retentions := []time.Duration{cfg.DB.Tier1Retention, cfg.DB.Tier2Retention}
	blocks := []int{cfg.DB.Tier1BlockSize, cfg.DB.Tier2BlockSize}
	for i := range tiers {
		if i < len(retentions) {
			tiers[i].Retention = retentions[i]
		}
		if i < len(blocks) && blocks[i] > 0 {
			tiers[i].BlockSize = blocks[i]
		}
	}
	return tsdb.Options{
		Dir:           filepath.Join(cfg.Global.DataDir, "db"),
		Retention:     cfg.DB.Tier0Retention,
		RetentionSize: retSize,
		BlockSize:     cfg.DB.BlockSize,
		Checkpoint:    cfg.DB.Checkpoint,
		Tiers:         tiers,
		Logger:        log,
	}, nil
}

func collectorOptions(cfg *config.Config, disabled map[string]bool) collect.Options {
	return collect.Options{
		Names: cfg.Collectors.Enabled, Disabled: disabled,
		Modules: cfg.ModuleDecoders(), Timeout: cfg.Collectors.Timeout,
	}
}

func pluginRuntimeOptions(cfg *config.Config, dir string, log *slog.Logger) plugins.Options {
	functions := cfg.PluginFunctionsEnabled()
	return plugins.Options{
		Dir: dir, Disabled: cfg.Plugins.Disabled, Logger: log,
		StatePath:     filepath.Join(cfg.Global.DataDir, "plugins", "state.json"),
		RestartMin:    cfg.Plugins.RestartMin,
		RestartMax:    cfg.Plugins.RestartMax,
		MaxCharts:     cfg.Plugins.MaxCharts,
		MaxDimensions: cfg.Plugins.MaxDimensions,
		MaxLabels:     cfg.Plugins.MaxLabels,
		MaxVariables:  cfg.Plugins.MaxVariables,
		MaxLine:       cfg.Plugins.MaxLine,
		Functions:     &functions,
	}
}
