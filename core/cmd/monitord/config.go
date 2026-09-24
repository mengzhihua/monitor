package main

import (
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
)

// applyDebounce bounds how often a hub-pushed config is applied. A second
// change inside the window is deferred and retried when it elapses, so a
// push → restart → reconnect → re-push loop cannot restart-storm the agent.
const applyDebounce = 5 * time.Minute

// ErrApplyDeferred marks an apply postponed by the debounce window.
var ErrApplyDeferred = errors.New("apply deferred: inside debounce window")

type deferredApply struct {
	yamlText string
	rev      int64
}

var applyMu sync.Mutex
var lastApply time.Time
var pendingApply *deferredApply

// applyAgentConfig validates a hub-pushed config against the running one,
// backs up and atomically writes the file, then schedules the restart.
// ErrApplyDeferred is returned when the change lands in the debounce window.
func applyAgentConfig(path string, cur *config.Config, yamlText string, rev int64, log *slog.Logger, exit func()) error {
	next, err := config.Parse([]byte(yamlText))
	if err != nil {
		return err
	}
	if err := config.CheckLocked(cur, next); err != nil {
		return err
	}
	if b, err := os.ReadFile(path); err == nil && string(b) == yamlText {
		return nil // already in effect (e.g. re-pushed after restart)
	}
	applyMu.Lock()
	defer applyMu.Unlock()
	if time.Since(lastApply) < applyDebounce {
		pendingApply = &deferredApply{yamlText: yamlText, rev: rev}
		delay := applyDebounce - time.Since(lastApply)
		time.AfterFunc(delay, func() { retryPendingApply(path, cur, log, exit) })
		return ErrApplyDeferred
	}
	return writeAndRestart(path, yamlText, rev, log, exit)
}

// writeAndRestart runs under applyMu: write, stamp the debounce clock and exit.
func writeAndRestart(path, yamlText string, rev int64, log *slog.Logger, exit func()) error {
	if err := config.Save(path, []byte(yamlText)); err != nil {
		return err
	}
	lastApply = time.Now()
	pendingApply = nil
	log.Info("applied hub-pushed config; restarting (the process manager must bring the service back)",
		"path", path, "rev", rev)
	exit()
	return nil
}

// retryPendingApply applies the deferred change once the window elapses.
func retryPendingApply(path string, cur *config.Config, log *slog.Logger, exit func()) {
	applyMu.Lock()
	p := pendingApply
	applyMu.Unlock()
	if p == nil {
		return
	}
	if err := applyAgentConfig(path, cur, p.yamlText, p.rev, log, exit); err != nil && !errors.Is(err, ErrApplyDeferred) {
		log.Warn("deferred config apply failed", "err", err)
	}
}
