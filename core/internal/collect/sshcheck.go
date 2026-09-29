package collect

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// sshcheck runs configured commands over the ssh binary (BatchMode, no local shell).
type sshcheckConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
	Jobs    []sshJob      `yaml:"jobs"`
}

type sshJob struct {
	Name    string            `yaml:"name"`
	Address string            `yaml:"address"`
	User    string            `yaml:"user"`
	KeyFile string            `yaml:"key_file"`
	Command string            `yaml:"command"`
	Steps   []preprocess.Step `yaml:"preprocess"`
}

type sshcheckCollector struct {
	cfg  sshcheckConfig
	run  func(ctx context.Context, name string, args ...string) ([]byte, error)
	prev map[string]float64
}

func init() { Register("sshcheck", func() Collector { return &sshcheckCollector{} }) }

func (s *sshcheckCollector) Name() string { return "sshcheck" }

func (s *sshcheckCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "ssh"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 5 * time.Second
	}
	for _, j := range s.cfg.Jobs {
		if err := validateSSHJob(j); err != nil {
			return err
		}
	}
	return nil
}

func validateSSHJob(j sshJob) error {
	if !validItemName(j.Name) {
		return fmt.Errorf("sshcheck: job name %q", j.Name)
	}
	if j.User == "" || strings.ContainsAny(j.User, " \t\n@") || j.Address == "" || strings.ContainsAny(j.Address, " \t\n") {
		return fmt.Errorf("sshcheck %s: user and address must be a single token", j.Name)
	}
	if strings.ContainsAny(j.Command, "\n\r") || j.Command == "" {
		return fmt.Errorf("sshcheck %s: command must be one line", j.Name)
	}
	if j.KeyFile != "" && strings.ContainsAny(j.KeyFile, "\n\r") {
		return fmt.Errorf("sshcheck %s: key file", j.Name)
	}
	return nil
}

func (s *sshcheckCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(s.cfg.Jobs) == 0 {
		return fmt.Errorf("sshcheck: no jobs")
	}
	s.prev = map[string]float64{}
	for _, j := range s.cfg.Jobs {
		ch := &registry.Chart{ID: "sshcheck." + sanitizeID(j.Name), Context: "sshcheck.value", Title: "SSH " + j.Name, Units: "value",
			Family: "ssh", Plugin: "sshcheck", Module: "sshcheck", Priority: 56200,
			Labels: map[string]string{"address": j.Address}, Dimensions: []*registry.Dimension{{ID: "value"}}}
		reg.AddChart(ch)
	}
	return nil
}

func (s *sshcheckCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	run := s.run
	if run == nil {
		run = execRun(s.cfg.Timeout)
	}
	for _, j := range s.cfg.Jobs {
		args := []string{"-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=5"}
		if j.KeyFile != "" {
			args = append(args, "-i", j.KeyFile)
		}
		args = append(args, j.User+"@"+j.Address, j.Command)
		out, err := run(ctx, s.cfg.Command, args...)
		raw := strings.TrimSpace(string(out))
		prev := math.NaN()
		if v, ok := s.prev[j.Name]; ok {
			prev = v
		}
		value, next, perr := preprocess.Apply(j.Steps, raw, prev)
		if perr == nil {
			s.prev[j.Name] = next
		}
		if err != nil || perr != nil {
			value = math.NaN()
		}
		if !math.IsNaN(value) {
			_ = reg.Collect("sshcheck."+sanitizeID(j.Name), now, map[string]float64{"value": value})
		}
	}
	return nil
}
