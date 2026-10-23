package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/audit"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
)

// autoCommandRunner runs whitelisted remediation commands for health actions,
// capturing exit code / duration / truncated output for audit + handling history.
type autoCommandRunner struct {
	commands        []config.Command
	audit           *audit.Log
	record          func(health.ActionCommand, int, int64, string)
	recordInhibited func(health.ActionCommand, string)
	timeout         time.Duration
	log             *slog.Logger
}

func (r *autoCommandRunner) NoteInhibited(inv health.ActionCommand, reason string) {
	if r == nil || r.recordInhibited == nil {
		return
	}
	r.recordInhibited(inv, reason)
}

const autoCommandOutputMax = 4096

func autoActionAuditStatus(code int, timedOut bool) int {
	if timedOut {
		return 504
	}
	if code == 0 {
		return 200
	}
	return 500
}

func (r *autoCommandRunner) Run(inv health.ActionCommand) {
	if r == nil || inv.Name == "" {
		return
	}
	defaultTimeout := r.timeout
	if defaultTimeout <= 0 {
		defaultTimeout = 30 * time.Second
	}
	for _, c := range r.commands {
		if c.Name != inv.Name || len(c.Argv) == 0 || !filepath.IsAbs(c.Argv[0]) || strings.ContainsAny(strings.Join(c.Argv, ""), "\n\r") {
			continue
		}
		timeout := defaultTimeout
		if c.Timeout > 0 {
			timeout = c.Timeout
		}
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		started := time.Now()
		cmd := exec.CommandContext(ctx, c.Argv[0], c.Argv[1:]...)
		out, err := cmd.CombinedOutput()
		elapsed := time.Since(started)
		timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)
		cancel()
		msg := strings.TrimSpace(string(out))
		if timedOut && msg == "" {
			msg = "command timed out"
		}
		if len(msg) > autoCommandOutputMax {
			msg = msg[:autoCommandOutputMax]
		}
		code := 0
		if err != nil {
			if timedOut {
				code = -1
			} else if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				code = 1
			}
		}
		status := autoActionAuditStatus(code, timedOut)
		if r.audit != nil {
			exit := code
			r.audit.Append(audit.Entry{
				TS: time.Now().Unix(), User: "system", Role: "system",
				Action: "action:" + inv.Name, Target: inv.AlarmName + "/" + inv.Chart, Status: status,
				Command: inv.Name, Argv: strings.Join(c.Argv, " "), ExitCode: &exit,
				DurationMs: elapsed.Milliseconds(), Output: msg,
				Note: fmt.Sprintf("alarm=%s chart=%s recovery=%v ok=%v", inv.AlarmName, inv.Chart, inv.Recovery, code == 0 && !timedOut),
			})
		}
		if r.record != nil {
			r.record(inv, code, elapsed.Milliseconds(), msg)
		}
		if r.log != nil && code != 0 {
			r.log.Warn("auto action failed", "command", inv.Name, "alarm", inv.AlarmName, "exit", code, "status", status, "duration_ms", elapsed.Milliseconds())
		}
		return
	}
	if r.log != nil {
		r.log.Warn("auto action unknown command", "command", inv.Name, "alarm", inv.AlarmName)
	}
	if r.audit != nil {
		exit := 127
		r.audit.Append(audit.Entry{
			TS: time.Now().Unix(), User: "system", Role: "system",
			Action: "action:" + inv.Name, Target: inv.AlarmName + "/" + inv.Chart, Status: 404,
			Command: inv.Name, ExitCode: &exit, DurationMs: 0, Note: "unknown or invalid command",
		})
	}
	if r.record != nil {
		r.record(inv, 127, 0, "unknown or invalid command")
	}
}
