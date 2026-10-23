package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/mengzhihua/monitor/core/internal/backup"
	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
)

// version is the release semver in source; builds override via
// -ldflags "-X main.version=...". Untagged builds append -dev+short-hash.
var version = "3.0.17"

// commit is the full git SHA when stamped via ldflags or read from build VCS info.
var commit = ""

// buildTime is a UTC RFC3339 stamp from ldflags (SOURCE_DATE_EPOCH or build clock).
var buildTime = ""

// dirty is "true" when the build tree had uncommitted changes (vcs.modified).
var dirty = ""

// releaseBuild is "1" for tag/release stamps that must keep a bare semver.
var releaseBuild = "0"

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
	listCollectors := flag.Bool("list-collectors", false, "print registered collector names as JSON and exit")
	showVersion := flag.Bool("version", false, "print version and exit")
	btReport := flag.Bool("bluetooth-report", false, "probe connected Bluetooth devices once, print the link/codec report and exit")
	btReportJSON := flag.Bool("bluetooth-report-json", false, "same as -bluetooth-report but print JSON")
	btSnapshot := flag.String("bluetooth-snapshot", "", "directory of recorded Bluetooth command output to replay instead of probing this host")
	backupDir := flag.String("backup-dir", "", "offline backup to a new directory and exit")
	restoreFrom := flag.String("restore-from", "", "restore backup into a new data directory and exit")
	flag.Parse()

	if *listCollectors {
		return json.NewEncoder(os.Stdout).Encode(collect.Available())
	}
	if *btReport || *btReportJSON {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		report, err := collect.BluetoothReportNow(ctx, *btSnapshot)
		if err != nil {
			return err
		}
		if *btReportJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(report)
		}
		_, err = fmt.Print(collect.FormatBluetoothReport(report))
		return err
	}
	if *showVersion {
		// Keep "monitord <ver> <os>/<arch>" stable for packaging checks.
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
	if *backupDir != "" && *restoreFrom != "" {
		return errors.New("backup and restore are mutually exclusive")
	}
	if *restoreFrom != "" {
		return backup.Restore(*restoreFrom, cfg.Global.DataDir)
	}
	if *backupDir != "" {
		return backup.Create(cfg.Global.DataDir, *backupDir)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runDaemon(ctx, cfg, *cfgPath, *listen != "", log)
}
