package collect

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// logindConfig is collectors.modules.logind (`loginctl` sessions/users).
type logindConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type logindCollector struct {
	cfg logindConfig
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

func init() {
	Register("logind", func() Collector { return &logindCollector{} })
}

func (l *logindCollector) Name() string { return "logind" }

func (l *logindCollector) Configure(decode func(v any) error) error {
	if err := decode(&l.cfg); err != nil {
		return err
	}
	if l.cfg.Command == "" {
		l.cfg.Command = "loginctl"
	}
	if l.cfg.Timeout <= 0 {
		l.cfg.Timeout = 2 * time.Second
	}
	return nil
}

func (l *logindCollector) Init(reg *registry.Registry) error {
	if l.cfg.Command == "" {
		if err := l.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if l.run == nil {
		l.run = execRun(l.cfg.Timeout)
	}
	if _, err := l.stats(context.Background()); err != nil {
		return err
	}
	for _, ch := range []*registry.Chart{
		{ID: "logind.sessions", Title: "Logind Sessions", Units: "sessions", Type: registry.Stacked, Priority: 61200,
			Dimensions: []*registry.Dimension{{ID: "remote"}, {ID: "local"}}},
		{ID: "logind.sessions_type", Title: "Logind Sessions By Type", Units: "sessions", Type: registry.Stacked, Priority: 61210,
			Dimensions: []*registry.Dimension{{ID: "console"}, {ID: "graphical"}, {ID: "other"}}},
		{ID: "logind.sessions_state", Title: "Logind Sessions By State", Units: "sessions", Type: registry.Stacked, Priority: 61220,
			Dimensions: []*registry.Dimension{{ID: "online"}, {ID: "closing"}, {ID: "active"}}},
		{ID: "logind.users_state", Title: "Logind Users By State", Units: "users", Type: registry.Stacked, Priority: 61230,
			Dimensions: []*registry.Dimension{{ID: "offline"}, {ID: "closing"}, {ID: "online"}, {ID: "lingering"}, {ID: "active"}}},
	} {
		ch.Family, ch.Plugin, ch.Module = "logind", "logind", "logind"
		reg.AddChart(ch)
	}
	return nil
}

func (l *logindCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	st, err := l.stats(ctx)
	if err != nil {
		return err
	}
	_ = reg.Collect("logind.sessions", now, map[string]float64{"remote": st["sessions_remote"], "local": st["sessions_local"]})
	_ = reg.Collect("logind.sessions_type", now, map[string]float64{"console": st["sessions_type_console"], "graphical": st["sessions_type_graphical"], "other": st["sessions_type_other"]})
	_ = reg.Collect("logind.sessions_state", now, map[string]float64{"online": st["sessions_state_online"], "closing": st["sessions_state_closing"], "active": st["sessions_state_active"]})
	_ = reg.Collect("logind.users_state", now, map[string]float64{
		"offline": st["users_state_offline"], "closing": st["users_state_closing"], "online": st["users_state_online"],
		"lingering": st["users_state_lingering"], "active": st["users_state_active"],
	})
	return nil
}

func (l *logindCollector) stats(ctx context.Context) (map[string]float64, error) {
	b, err := l.run(ctx, l.cfg.Command, "list-sessions", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("logind: %w", err)
	}
	out := map[string]float64{}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.EqualFold(fields[0], "SESSION") {
			continue
		}
		props, err := l.show(ctx, "show-session", fields[0])
		if err != nil {
			continue
		}
		if strings.EqualFold(props["Remote"], "yes") {
			out["sessions_remote"]++
		} else {
			out["sessions_local"]++
		}
		switch strings.ToLower(props["Type"]) {
		case "tty", "unspecified":
			out["sessions_type_console"]++
		case "wayland", "x11", "mir":
			out["sessions_type_graphical"]++
		default:
			if props["Type"] == "" {
				out["sessions_type_other"]++
			} else if strings.Contains(strings.ToLower(props["Type"]), "tty") {
				out["sessions_type_console"]++
			} else {
				out["sessions_type_other"]++
			}
		}
		switch strings.ToLower(props["State"]) {
		case "online":
			out["sessions_state_online"]++
		case "closing":
			out["sessions_state_closing"]++
		default:
			out["sessions_state_active"]++
		}
	}
	ub, err := l.run(ctx, l.cfg.Command, "list-users", "--no-legend", "--no-pager")
	if err != nil {
		return nil, fmt.Errorf("logind: %w", err)
	}
	for _, line := range strings.Split(string(ub), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.EqualFold(fields[0], "UID") {
			continue
		}
		props, err := l.show(ctx, "show-user", fields[0])
		if err != nil {
			continue
		}
		switch strings.ToLower(props["State"]) {
		case "offline":
			out["users_state_offline"]++
		case "closing":
			out["users_state_closing"]++
		case "online":
			out["users_state_online"]++
		case "lingering":
			out["users_state_lingering"]++
		default:
			out["users_state_active"]++
		}
	}
	return out, nil
}

func (l *logindCollector) show(ctx context.Context, cmd, id string) (map[string]string, error) {
	b, err := l.run(ctx, l.cfg.Command, cmd, id)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out, nil
}
