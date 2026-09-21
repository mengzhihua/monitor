package collect

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// sambaConfig is collectors.modules.samba (smbstatus -P).
type sambaConfig struct {
	Command string        `yaml:"command"`
	Timeout time.Duration `yaml:"timeout"`
}

type sambaCollector struct {
	cfg     sambaConfig
	run     func(ctx context.Context, name string, args ...string) ([]byte, error)
	sysSeen map[string]bool
	smbSeen map[string]bool
}

func init() {
	Register("samba", func() Collector { return &sambaCollector{} })
}

func (s *sambaCollector) Name() string { return "samba" }

func (s *sambaCollector) Configure(decode func(v any) error) error {
	if err := decode(&s.cfg); err != nil {
		return err
	}
	if s.cfg.Command == "" {
		s.cfg.Command = "smbstatus"
	}
	if s.cfg.Timeout <= 0 {
		s.cfg.Timeout = 3 * time.Second
	}
	return nil
}

func (s *sambaCollector) Init(reg *registry.Registry) error {
	if s.cfg.Command == "" {
		if err := s.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	s.sysSeen, s.smbSeen = map[string]bool{}, map[string]bool{}
	m, err := s.profile(context.Background())
	if err != nil {
		return err
	}
	if len(m) == 0 {
		return fmt.Errorf("samba: empty profile")
	}
	return nil
}

func (s *sambaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	m, err := s.profile(ctx)
	if err != nil {
		return err
	}
	done := map[string]bool{}
	for k, v := range m {
		if name, ok := sambaCallName(k, "syscall_", "_count"); ok {
			s.ensureSyscall(reg, name)
			_ = reg.Collect("samba.syscall_calls."+name, now, map[string]float64{"syscalls": v})
		} else if name, ok := sambaCallName(k, "syscall_", "_bytes"); ok {
			s.ensureSyscall(reg, name)
			_ = reg.Collect("samba.syscall_transferred_data."+name, now, map[string]float64{"transferred": v})
		} else if name, ok := sambaCallName(k, "smb2_", "_count"); ok {
			s.ensureSMB2(reg, name)
			_ = reg.Collect("samba.smb2_call_calls."+name, now, map[string]float64{"smb2": v})
		} else if name, ok := sambaCallName(k, "smb2_", "_inbytes"); ok {
			if done[name] {
				continue
			}
			done[name] = true
			s.ensureSMB2(reg, name)
			_ = reg.Collect("samba.smb2_call_transferred_data."+name, now, map[string]float64{
				"in": v, "out": m["smb2_"+name+"_outbytes"]})
		} else if name, ok := sambaCallName(k, "smb2_", "_outbytes"); ok {
			if done[name] {
				continue
			}
			done[name] = true
			s.ensureSMB2(reg, name)
			_ = reg.Collect("samba.smb2_call_transferred_data."+name, now, map[string]float64{
				"in": m["smb2_"+name+"_inbytes"], "out": v})
		}
	}
	return nil
}

func (s *sambaCollector) ensureSyscall(reg *registry.Registry, name string) {
	if s.sysSeen[name] {
		return
	}
	s.sysSeen[name] = true
	inc := registry.Incremental
	calls := &registry.Chart{ID: "samba.syscall_calls." + name, Context: "samba.syscall_calls",
		Title: "Syscalls Count", Units: "calls/s", Priority: 52800,
		Labels:     map[string]string{"syscall": name},
		Dimensions: []*registry.Dimension{{ID: "syscalls", Algorithm: inc}}}
	data := &registry.Chart{ID: "samba.syscall_transferred_data." + name, Context: "samba.syscall_transferred_data",
		Title: "Syscall Transferred Data", Units: "bytes/s", Type: registry.Area, Priority: 52810,
		Labels:     map[string]string{"syscall": name},
		Dimensions: []*registry.Dimension{{ID: "transferred", Algorithm: inc}}}
	for _, c := range []*registry.Chart{calls, data} {
		c.Family, c.Plugin, c.Module = "samba", "samba", "samba"
		reg.AddChart(c)
	}
}

func (s *sambaCollector) ensureSMB2(reg *registry.Registry, name string) {
	if s.smbSeen[name] {
		return
	}
	s.smbSeen[name] = true
	inc := registry.Incremental
	calls := &registry.Chart{ID: "samba.smb2_call_calls." + name, Context: "samba.smb2_call_calls",
		Title: "SMB2 Calls Count", Units: "calls/s", Priority: 52820,
		Labels:     map[string]string{"smb2call": name},
		Dimensions: []*registry.Dimension{{ID: "smb2", Algorithm: inc}}}
	data := &registry.Chart{ID: "samba.smb2_call_transferred_data." + name, Context: "samba.smb2_call_transferred_data",
		Title: "SMB2 Call Transferred Data", Units: "bytes/s", Type: registry.Area, Priority: 52830,
		Labels: map[string]string{"smb2call": name},
		Dimensions: []*registry.Dimension{
			{ID: "in", Algorithm: inc}, {ID: "out", Algorithm: inc, Multiplier: -1}}}
	for _, c := range []*registry.Chart{calls, data} {
		c.Family, c.Plugin, c.Module = "samba", "samba", "samba"
		reg.AddChart(c)
	}
}

func (s *sambaCollector) profile(ctx context.Context) (map[string]float64, error) {
	run := s.run
	if run == nil {
		run = execRun(s.cfg.Timeout)
	}
	out, err := run(ctx, s.cfg.Command, "-P")
	if err != nil {
		return nil, fmt.Errorf("smbstatus: %w", err)
	}
	m := parseSambaProfile(out)
	if len(m) == 0 {
		return nil, fmt.Errorf("samba: no profile metrics")
	}
	return m, nil
}

func parseSambaProfile(b []byte) map[string]float64 {
	out := map[string]float64{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "syscall_") && !strings.HasPrefix(line, "smb2_") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				continue
			}
			key, val = fields[0], fields[len(fields)-1]
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if !strings.HasSuffix(key, "count") && !strings.HasSuffix(key, "bytes") {
			continue
		}
		out[key] = firstFloat(val)
	}
	return out
}

func sambaCallName(s, prefix, suffix string) (string, bool) {
	if !strings.HasPrefix(s, prefix) || !strings.HasSuffix(s, suffix) {
		return "", false
	}
	name := strings.TrimPrefix(s, prefix)
	name = strings.TrimSuffix(name, suffix)
	if name == "" {
		return "", false
	}
	return name, true
}
