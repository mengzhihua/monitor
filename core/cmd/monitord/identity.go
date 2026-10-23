package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/shirou/gopsutil/v4/host"

	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func init() { applyBuildIdentity() }

func applyBuildIdentity() {
	if commit == "" {
		commit = vcsSetting("vcs.revision")
	}
	if dirty == "" {
		if vcsSetting("vcs.modified") == "true" {
			dirty = "true"
		} else if commit != "" {
			dirty = "false"
		}
	}
	if buildTime == "" {
		buildTime = vcsSetting("vcs.time")
	}
	if releaseBuild == "1" || commit == "" {
		return
	}
	if strings.Contains(version, "-dev+") || strings.Contains(version, "+") {
		return
	}
	short := commit
	if len(short) > 12 {
		short = short[:12]
	}
	version = version + "-dev+" + short
}

func vcsSetting(key string) string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range bi.Settings {
		if s.Key == key {
			return s.Value
		}
	}
	return ""
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
