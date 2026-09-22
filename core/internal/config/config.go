// Package config loads monitor.yaml; every key has a default so an empty or
// missing file yields a working agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/mengzhihua/monitor/core/internal/export"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/plugins"
)

type Config struct {
	// Mode: "agent" collects the local host; "hub" additionally accepts
	// streaming agents (hub.api_keys) and serves them under node=<id>.
	Mode   string `yaml:"mode"`
	Global struct {
		Hostname    string `yaml:"hostname"`
		UpdateEvery int    `yaml:"update_every"`
		DataDir     string `yaml:"data_dir"`
	} `yaml:"global"`
	DB struct {
		Tier0Retention     time.Duration `yaml:"tier0_retention"`
		Tier0RetentionSize string        `yaml:"tier0_retention_size"`
		Checkpoint         time.Duration `yaml:"checkpoint"`
		Tiers              int           `yaml:"tiers"` // 1..3: tier0 only, +1m rollups, +1h rollups
		Tier1Retention     time.Duration `yaml:"tier1_retention"`
		Tier2Retention     time.Duration `yaml:"tier2_retention"`
	} `yaml:"db"`
	Web struct {
		Listen    string   `yaml:"listen"`
		AllowFrom []string `yaml:"allow_from"` // CIDRs; empty = all
		Token     string   `yaml:"token"`      // optional bearer token for the API (admin)
		Users     []User   `yaml:"users"`      // named credentials with roles
		OIDC      OIDC     `yaml:"oidc"`
	} `yaml:"web"`
	Stream     Stream `yaml:"stream"`
	Hub        Hub    `yaml:"hub"`
	Collectors struct {
		Enabled  []string `yaml:"enabled"`  // empty = all
		Disabled []string `yaml:"disabled"` // names to turn off
		// Modules holds per-collector settings keyed by collector name; the
		// schema of each section is owned by the collector (see
		// monitor.example.yaml).
		Modules map[string]yaml.Node `yaml:"modules"`
	} `yaml:"collectors"`
	Health  Health  `yaml:"health"`
	Plugins Plugins `yaml:"plugins"`
	Export  Export  `yaml:"export"`
}

// User is an API credential: role admin | troubleshooter | viewer.
type User struct {
	Name  string `yaml:"name"`
	Token string `yaml:"token"`
	Role  string `yaml:"role"`
}

// Stream configures this agent's upstream connection to a hub.
type Stream struct {
	Enabled            bool          `yaml:"enabled"`
	Destinations       []string      `yaml:"destinations"` // ws://hub:19999 (path optional), tried in order
	APIKey             string        `yaml:"api_key"`
	ClaimToken         string        `yaml:"claim_token"`
	InsecureSkipVerify bool          `yaml:"insecure_skip_verify"`
	Timeout            time.Duration `yaml:"timeout"`
	Replicate          time.Duration `yaml:"replicate"` // max history re-sent after reconnect
}

// Hub configures accepting streamed nodes (mode: hub).
type Hub struct {
	APIKeys   []string      `yaml:"api_keys"`  // credentials agents present; empty = no ingestion
	Replicate time.Duration `yaml:"replicate"` // max backfill accepted from agents
	// Ingest quotas (0 = default: 1000 / 5000 / 1000).
	MaxNodes         int `yaml:"max_nodes"`
	MaxChartsPerNode int `yaml:"max_charts_per_node"`
	MaxDimsPerChart  int `yaml:"max_dims_per_chart"`
	// Cluster: other hubs this process queries for nodes it does not own.
	Peers     []string `yaml:"peers"`
	PeerToken string   `yaml:"peer_token"`
	// Default Space/Room names created on empty org (hub mode).
	Space string `yaml:"space"`
	Room  string `yaml:"room"`
}

// OIDC is web.oidc.
type OIDC struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`
	RedirectURL  string `yaml:"redirect_url"`
	Role         string `yaml:"role"`
}

// ModuleDecoders adapts collectors.modules to the decoder callbacks the
// collect scheduler expects.
func (c *Config) ModuleDecoders() map[string]func(v any) error {
	out := make(map[string]func(v any) error, len(c.Collectors.Modules))
	for name, node := range c.Collectors.Modules {
		node := node
		out[name] = func(v any) error {
			if err := node.Decode(v); err != nil {
				return fmt.Errorf("collectors.modules.%s: %w", name, err)
			}
			return nil
		}
	}
	return out
}

// Plugins configures external plugins.d collectors.
type Plugins struct {
	Enabled  *bool          `yaml:"enabled"`  // default true
	Dir      string         `yaml:"dir"`      // scanned for executable *.plugin files, relative to the config file
	Disabled []string       `yaml:"disabled"` // plugin names not to start
	List     []plugins.Spec `yaml:"list"`     // explicit plugins (command may be relative to dir)
}

// Health configures the alarm engine and notification channels.
type Health struct {
	Enabled *bool             `yaml:"enabled"`  // default true
	Dir     string            `yaml:"dir"`      // extra rule files (*.yaml), relative to the config file
	Builtin *bool             `yaml:"builtin"`  // load the rules shipped with the agent (default true)
	LogKeep int               `yaml:"log_keep"` // alarm log entries kept in memory
	Silent  bool              `yaml:"silent"`   // evaluate but never notify
	Notify  Notify            `yaml:"notify"`
	Alarms  []health.RuleSpec `yaml:"alarms"` // inline rules, same schema as health.d files
}

// Notify holds the notification channels; a channel is active when its
// required fields are set. Roles map an alarm's `to:` to channel names.
type Notify struct {
	Roles   map[string][]string `yaml:"roles"` // e.g. sysadmin: [slack, email]
	Webhook struct {
		URL     string            `yaml:"url"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"webhook"`
	Slack struct {
		WebhookURL string `yaml:"webhook_url"`
		Channel    string `yaml:"channel"`
	} `yaml:"slack"`
	Email struct {
		Server   string   `yaml:"server"` // host:port
		From     string   `yaml:"from"`
		To       []string `yaml:"to"`
		Username string   `yaml:"username"`
		Password string   `yaml:"password"`
		Insecure bool     `yaml:"insecure_skip_verify"`
	} `yaml:"email"`
	DingTalk struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"dingtalk"`
	WeCom struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"wecom"`
	Feishu struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"feishu"`
	Telegram struct {
		Token  string `yaml:"token"`
		ChatID string `yaml:"chat_id"`
	} `yaml:"telegram"`
	Discord struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"discord"`
	PagerDuty struct {
		RoutingKey string `yaml:"routing_key"`
	} `yaml:"pagerduty"`
}

// Export pushes latest samples to Graphite / Influx / JSON HTTP.
type Export struct {
	Destinations []export.Destination `yaml:"destinations"`
}

// HealthEnabled reports whether the alarm engine should run.
func (c *Config) HealthEnabled() bool { return c.Health.Enabled == nil || *c.Health.Enabled }

// PluginsEnabled reports whether external plugins are started.
func (c *Config) PluginsEnabled() bool { return c.Plugins.Enabled == nil || *c.Plugins.Enabled }

// HealthBuiltin reports whether the shipped rules are loaded.
func (c *Config) HealthBuiltin() bool { return c.Health.Builtin == nil || *c.Health.Builtin }

func Default() *Config {
	c := &Config{Mode: "agent"}
	c.Global.UpdateEvery = 1
	c.Global.DataDir = "./data"
	c.DB.Tier0Retention = 14 * 24 * time.Hour
	c.DB.Tier0RetentionSize = "1GiB"
	c.DB.Checkpoint = 30 * time.Second
	c.DB.Tiers = 3
	c.DB.Tier1Retention = 90 * 24 * time.Hour
	c.DB.Tier2Retention = 2 * 365 * 24 * time.Hour
	c.Web.Listen = ":19999"
	c.Stream.Timeout = 10 * time.Second
	c.Stream.Replicate = time.Hour
	c.Hub.Replicate = 24 * time.Hour
	c.Health.Dir = "health.d"
	c.Health.LogKeep = 1000
	c.Plugins.Dir = "plugins.d"
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
