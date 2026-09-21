package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// fail2banConfig is collectors.modules.fail2ban (fail2ban-client status).
type fail2banConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type fail2banCollector struct {
	cfg  fail2banConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	seen map[string]bool
}

func init() {
	Register("fail2ban", func() Collector { return &fail2banCollector{} })
}

func (f *fail2banCollector) Name() string { return "fail2ban" }

func (f *fail2banCollector) Configure(decode func(v any) error) error {
	if err := decode(&f.cfg); err != nil {
		return err
	}
	if f.cfg.Command == "" {
		f.cfg.Command = "fail2ban-client"
	}
	if f.cfg.Timeout <= 0 {
		f.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (f *fail2banCollector) Init(reg *registry.Registry) error {
	if f.cfg.Command == "" {
		if err := f.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	f.seen = map[string]bool{}
	jails, err := f.jails(context.Background())
	if err != nil {
		return err
	}
	if len(jails) == 0 {
		return fmt.Errorf("fail2ban: no jails")
	}
	for _, j := range jails {
		if _, err := f.jailStatus(context.Background(), j); err != nil {
			return err
		}
	}
	return nil
}

func (f *fail2banCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	jails, err := f.jails(ctx)
	if err != nil {
		return err
	}
	if len(jails) == 0 {
		return fmt.Errorf("fail2ban: no jails")
	}
	for _, j := range jails {
		st, err := f.jailStatus(ctx, j)
		if err != nil {
			return err
		}
		f.ensure(reg, j)
		id := sanitizeID(j)
		_ = reg.Collect("fail2ban.jail_banned_ips."+id, now, map[string]float64{"banned": st.banned})
		_ = reg.Collect("fail2ban.jail_active_failures."+id, now, map[string]float64{"active_failures": st.failed})
	}
	return nil
}

func (f *fail2banCollector) ensure(reg *registry.Registry, jail string) {
	if f.seen[jail] {
		return
	}
	f.seen[jail] = true
	id := sanitizeID(jail)
	banned := &registry.Chart{ID: "fail2ban.jail_banned_ips." + id, Context: "fail2ban.jail_banned_ips",
		Title: "Fail2Ban Jail banned IPs", Units: "addresses", Priority: 52300,
		Labels: map[string]string{"jail": jail}, Dimensions: []*registry.Dimension{{ID: "banned"}}}
	failed := &registry.Chart{ID: "fail2ban.jail_active_failures." + id, Context: "fail2ban.jail_active_failures",
		Title: "Fail2Ban Jail active failures", Units: "failures", Priority: 52310,
		Labels: map[string]string{"jail": jail}, Dimensions: []*registry.Dimension{{ID: "active_failures"}}}
	for _, c := range []*registry.Chart{banned, failed} {
		c.Family, c.Plugin, c.Module = "fail2ban", "fail2ban", "fail2ban"
		reg.AddChart(c)
	}
}

func (f *fail2banCollector) jails(ctx context.Context) ([]string, error) {
	out, err := f.cmd(ctx, "status")
	if err != nil {
		return nil, fmt.Errorf("fail2ban-client: %w", err)
	}
	return parseFail2banStatus(out)
}

type fail2banJail struct{ failed, banned float64 }

func (f *fail2banCollector) jailStatus(ctx context.Context, jail string) (fail2banJail, error) {
	out, err := f.cmd(ctx, "status", jail)
	if err != nil {
		return fail2banJail{}, fmt.Errorf("fail2ban-client status %s: %w", jail, err)
	}
	return parseFail2banJailStatus(out)
}

func (f *fail2banCollector) cmd(ctx context.Context, args ...string) ([]byte, error) {
	run := f.run
	if run == nil {
		run = execRun(f.cfg.Timeout)
	}
	return run(ctx, f.cfg.Command, args...)
}

func parseFail2banStatus(b []byte) ([]string, error) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if _, after, ok := strings.Cut(text, "Jail list:"); ok {
			after = strings.ReplaceAll(after, ",", " ")
			jails := strings.Fields(after)
			if len(jails) == 0 {
				return nil, fmt.Errorf("fail2ban: empty jail list")
			}
			return jails, nil
		}
	}
	return nil, fmt.Errorf("fail2ban: no jail list")
}

func parseFail2banJailStatus(b []byte) (fail2banJail, error) {
	var st fail2banJail
	var gotF, gotB bool
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		text := strings.TrimSpace(sc.Text())
		if _, after, ok := strings.Cut(text, "Currently failed:"); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(after), 64)
			if err != nil {
				return st, err
			}
			st.failed, gotF = n, true
		}
		if _, after, ok := strings.Cut(text, "Currently banned:"); ok {
			n, err := strconv.ParseFloat(strings.TrimSpace(after), 64)
			if err != nil {
				return st, err
			}
			st.banned, gotB = n, true
		}
	}
	if !gotF || !gotB {
		return st, fmt.Errorf("fail2ban: missing failed/banned")
	}
	return st, nil
}
