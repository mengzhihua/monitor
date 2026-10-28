package main

import (
	"errors"
	"log/slog"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/plugins"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// newAgentStream adapts remote config to collector and health state.
func newAgentStream(cfg *config.Config, cfgPath string, reg *registry.Registry, db *tsdb.Store, sched *collect.Scheduler, eng *health.Engine, pm *plugins.Manager, localDisabled map[string]bool, log *slog.Logger, requestExit func()) (*stream.Client, error) {
	if !cfg.Stream.Enabled {
		return nil, nil
	}
	if len(cfg.Stream.Destinations) == 0 {
		return nil, errors.New("stream.enabled requires stream.destinations")
	}
	var alarms func() []health.Alarm
	if eng != nil {
		alarms = eng.Alarms
	}
	tlsCfg, err := stream.ClientTLS(stream.CertFiles{
		CertFile: cfg.Stream.CertFile, KeyFile: cfg.Stream.KeyFile, CAFile: cfg.Stream.CAFile,
		InsecureSkipVerify: cfg.Stream.InsecureSkipVerify,
	})
	if err != nil {
		return nil, err
	}
	var pushedDisabled = map[string]bool{} // collectors a push disabled; re-enable on unlink
	sc := stream.NewClient(reg, db, stream.ClientOptions{
		Alarms:             alarms,
		Destinations:       cfg.Stream.Destinations,
		APIKey:             cfg.Stream.APIKey,
		PSK:                cfg.Stream.PSK,
		ClaimToken:         cfg.Stream.ClaimToken,
		InsecureSkipVerify: cfg.Stream.InsecureSkipVerify,
		TLS:                tlsCfg,
		Timeout:            cfg.Stream.Timeout,
		Replicate:          cfg.Stream.Replicate,
		Protocol:           cfg.Stream.Protocol,
		Version:            version,
		Functions: func() []collect.Function {
			functions := sched.Functions()
			for _, fn := range pm.Functions() {
				functions = append(functions, collect.Function{Name: fn.Name, Help: fn.Help, Timeout: fn.Timeout, Run: fn.Run})
			}
			return functions
		},
		Logger:     log.With("component", "stream"),
		ConfigPath: cfgPath,
		OnConfig: func(disabled []string, ov *stream.HealthOverlay) {
			next := map[string]bool{}
			for _, n := range disabled {
				next[n] = true
			}
			if ov != nil {
				for _, n := range ov.Disabled {
					next[n] = true
				}
				if eng != nil {
					if err := eng.SetOverlay(health.OverlayLayer{Rev: ov.Rev, Macros: ov.Macros,
						Rules: ov.Rules, Removed: ov.Removed, Templates: ov.Templates, RuleSource: ov.RuleSource}); err != nil {
						log.Warn("template overlay rejected", "err", err)
					}
				}
			}
			for n := range pushedDisabled {
				if !next[n] && !localDisabled[n] {
					// re-enable only what a push disabled, never collectors
					// disabled in the local config
					sched.SetEnabled(n, true)
				}
			}
			for n := range next {
				sched.SetEnabled(n, false)
			}
			pushedDisabled = next
		}, // union(nodeConfig.Disabled, overlay.Disabled)
		OnConfigFile: func(yamlText string, rev int64) error {
			return applyAgentConfig(cfgPath, cfg, yamlText, rev, log, requestExit)
		},
	})
	return sc, nil
}
