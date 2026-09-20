// Package config loads monitor.yaml; every key has a default so an empty or
// missing file yields a working agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Mode   string `yaml:"mode"` // agent | hub (hub not implemented yet)
	Global struct {
		Hostname    string `yaml:"hostname"`
		UpdateEvery int    `yaml:"update_every"`
		DataDir     string `yaml:"data_dir"`
	} `yaml:"global"`
	DB struct {
		Tier0Retention     time.Duration `yaml:"tier0_retention"`
		Tier0RetentionSize string        `yaml:"tier0_retention_size"`
		Checkpoint         time.Duration `yaml:"checkpoint"`
	} `yaml:"db"`
	Web struct {
		Listen    string   `yaml:"listen"`
		AllowFrom []string `yaml:"allow_from"` // CIDRs; empty = all
		Token     string   `yaml:"token"`      // optional bearer token for the API
	} `yaml:"web"`
	Collectors struct {
		Enabled  []string `yaml:"enabled"`  // empty = all
		Disabled []string `yaml:"disabled"` // names to turn off
	} `yaml:"collectors"`
}

func Default() *Config {
	c := &Config{Mode: "agent"}
	c.Global.UpdateEvery = 1
	c.Global.DataDir = "./data"
	c.DB.Tier0Retention = 14 * 24 * time.Hour
	c.DB.Tier0RetentionSize = "1GiB"
	c.DB.Checkpoint = 10 * time.Minute
	c.Web.Listen = ":19999"
	return c
}

// Load reads path (if it exists) on top of Default().
func Load(path string) (*Config, error) {
	c := Default()
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c, nil
		}
		return nil, err
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if c.Global.UpdateEvery < 1 {
		c.Global.UpdateEvery = 1
	}
	if c.Global.UpdateEvery > 3600 {
		c.Global.UpdateEvery = 3600
	}
	return c, nil
}

// ParseSize parses "512MiB", "1GiB", "100MB", "1024" (bytes).
func ParseSize(s string) (int64, error) {
	if s == "" {
		return 0, nil
	}
	var n float64
	var unit string
	if _, err := fmt.Sscanf(s, "%f%s", &n, &unit); err != nil {
		if _, err2 := fmt.Sscanf(s, "%f", &n); err2 != nil {
			return 0, fmt.Errorf("bad size %q", s)
		}
	}
	mult := map[string]float64{"": 1, "B": 1, "KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
		"KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}
	m, ok := mult[unit]
	if !ok {
		return 0, fmt.Errorf("bad size unit %q", unit)
	}
	return int64(n * m), nil
}
