// Package config loads monitor.yaml; every key has a default so an empty or
// missing file yields a working agent.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
		// Enabled turns off the embedded HTTP server entirely (agent-only
		// deployments reporting to a Hub). nil (unset) keeps it on, so existing
		// configs behave unchanged.
		Enabled       *bool    `yaml:"enabled"`
		Listen        string   `yaml:"listen"`
		AllowFrom     []string `yaml:"allow_from"` // CIDRs; empty = all
		Token         string   `yaml:"token"`      // admin password/bearer token; generated when no auth is configured
		Users         []User   `yaml:"users"`      // named credentials with roles
		OIDC          OIDC     `yaml:"oidc"`
		LDAP          LDAP     `yaml:"ldap"`
		TicketWebhook string   `yaml:"ticket_webhook"` // POST handling JSON after a successful save; empty = off
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
	// Protocol is "mqtt"/"aclk" for MQTT-over-WebSocket (/api/v1/aclk), or
	// empty/"stream" for JSON frames (/api/v1/stream).
	Protocol string `yaml:"protocol"`
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
	// Storage is "full" (default, hub keeps samples) or "proxy" (query live agent).
	Storage string `yaml:"storage"`
}

// OIDC is web.oidc.
type LDAP struct {
	URL      string `yaml:"url"`
	UserDN   string `yaml:"user_dn"`
	BindDN   string `yaml:"bind_dn"`
	BindPass string `yaml:"bind_password"`
	Role     string `yaml:"role"`
}

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
	Enabled          *bool                      `yaml:"enabled"`            // default true
	Dir              string                     `yaml:"dir"`                // extra rule files (*.yaml), relative to the config file
	Builtin          *bool                      `yaml:"builtin"`            // load the rules shipped with the agent (default true)
	LogKeep          int                        `yaml:"log_keep"`           // alarm log entries kept in memory
	Silent           bool                       `yaml:"silent"`             // evaluate but never notify
	InhibitSameChart *bool                      `yaml:"inhibit_same_chart"` // nil = suppress warnings while the same chart is critical
	GroupWait        string                     `yaml:"group_wait"`         // hold same-chart notifications, e.g. 10s; empty = off
	EscalateAfter    string                     `yaml:"escalate_after"`     // critical repeats change recipient after this, e.g. 15m
	EscalateTo       string                     `yaml:"escalate_to"`
	OnCall           []health.OnCallWindow      `yaml:"oncall"` // local clock windows that set the notify role; empty = off
	Notify           Notify                     `yaml:"notify"`
	Alarms           []health.RuleSpec          `yaml:"alarms"`  // inline rules, same schema as health.d files
	Windows          []health.MaintenanceWindow `yaml:"windows"` // recurring maintenance calendar
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
		Server      string   `yaml:"server"` // host:port
		From        string   `yaml:"from"`
		To          []string `yaml:"to"`
		Username    string   `yaml:"username"`
		Password    string   `yaml:"password"`
		PasswordEnv string   `yaml:"password_env"`
		TLSMode     string   `yaml:"tls_mode"`
		Insecure    bool     `yaml:"insecure_skip_verify"`
	} `yaml:"email"`
	DingTalk struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"dingtalk"`
	WeCom struct {
		WebhookURL string `yaml:"webhook_url"`
	} `yaml:"wecom"`
	Feishu struct {
		WebhookURL    string `yaml:"webhook_url"`
		WebhookURLEnv string `yaml:"webhook_url_env"`
		Secret        string `yaml:"secret"`
		SecretEnv     string `yaml:"secret_env"`
	} `yaml:"feishu"`
	Ntfy struct {
		URL      string `yaml:"url"`
		Topic    string `yaml:"topic"`
		TopicEnv string `yaml:"topic_env"`
		Token    string `yaml:"token"`
		TokenEnv string `yaml:"token_env"`
	} `yaml:"ntfy"`
	Gotify struct {
		URL      string `yaml:"url"`
		Token    string `yaml:"token"`
		TokenEnv string `yaml:"token_env"`
	} `yaml:"gotify"`
	Bark struct {
		URL          string `yaml:"url"`
		DeviceKey    string `yaml:"device_key"`
		DeviceKeyEnv string `yaml:"device_key_env"`
		Group        string `yaml:"group"`
	} `yaml:"bark"`
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
	Push struct {
		URL     string            `yaml:"url"`
		Headers map[string]string `yaml:"headers"`
	} `yaml:"push"`
	APNs struct {
		Key         string `yaml:"key"`
		KeyID       string `yaml:"key_id"`
		TeamID      string `yaml:"team_id"`
		Topic       string `yaml:"topic"`
		DeviceToken string `yaml:"device_token"`
	} `yaml:"apns"`
	FCM struct {
		ServerKey string `yaml:"server_key"`
		Token     string `yaml:"token"`
	} `yaml:"fcm"`
	Huawei struct {
		AppID string `yaml:"app_id"`
		Token string `yaml:"token"`
		RegID string `yaml:"reg_id"`
	} `yaml:"huawei"`
	Xiaomi struct {
		AppSecret string `yaml:"app_secret"`
		Package   string `yaml:"package"`
		RegID     string `yaml:"reg_id"`
	} `yaml:"xiaomi"`
	SMS struct {
		Provider  string `yaml:"provider"`
		AccessKey string `yaml:"access_key"`
		Secret    string `yaml:"secret"`
		SignName  string `yaml:"sign_name"`
		Template  string `yaml:"template"`
		Phone     string `yaml:"phone"`
		URL       string `yaml:"url"`
	} `yaml:"sms"`
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

// WebEnabled reports whether the embedded HTTP server should run. web.enabled
// is unset (nil) by default, which keeps the server on for backward compat.
func (c *Config) WebEnabled() bool { return c.Web.Enabled == nil || *c.Web.Enabled }

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
	// Resolve only explicit secret references, never expand arbitrary YAML.
	for _, field := range []struct {
		name  string
		value *string
		env   string
	}{
		{"health.notify.email.password", &c.Health.Notify.Email.Password, c.Health.Notify.Email.PasswordEnv},
		{"health.notify.feishu.webhook_url", &c.Health.Notify.Feishu.WebhookURL, c.Health.Notify.Feishu.WebhookURLEnv},
		{"health.notify.feishu.secret", &c.Health.Notify.Feishu.Secret, c.Health.Notify.Feishu.SecretEnv},
		{"health.notify.ntfy.topic", &c.Health.Notify.Ntfy.Topic, c.Health.Notify.Ntfy.TopicEnv},
		{"health.notify.ntfy.token", &c.Health.Notify.Ntfy.Token, c.Health.Notify.Ntfy.TokenEnv},
		{"health.notify.gotify.token", &c.Health.Notify.Gotify.Token, c.Health.Notify.Gotify.TokenEnv},
		{"health.notify.bark.device_key", &c.Health.Notify.Bark.DeviceKey, c.Health.Notify.Bark.DeviceKeyEnv},
	} {
		if field.env == "" {
			continue
		}
		value, ok := os.LookupEnv(field.env)
		if !ok || value == "" {
			return nil, fmt.Errorf("%s: referenced environment variable is missing or empty", field.name)
		}
		*field.value = value
	}
	if c.Global.UpdateEvery < 1 {
		c.Global.UpdateEvery = 1
	}
	if c.Global.UpdateEvery > 3600 {
		c.Global.UpdateEvery = 3600
	}
	return c, nil
}

// LoadStartup reads path for process start. A missing file still returns defaults.
// If path exists but cannot be loaded, and path.bak can, the backup is copied
// back onto path and that config is used. The bad file is left only when no
// backup loads. raw is the file content the process actually started from.
func LoadStartup(path string) (c *Config, raw string, restored bool, err error) {
	b, readErr := os.ReadFile(path)
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			c, err = Load(path)
			return c, "", false, err
		}
		return nil, "", false, readErr
	}
	c, err = Load(path)
	if err == nil {
		return c, string(b), false, nil
	}
	bak := path + ".bak"
	if _, berr := Load(bak); berr != nil {
		return nil, "", false, err
	}
	bb, rerr := os.ReadFile(bak)
	if rerr != nil {
		return nil, "", false, err
	}
	if rerr = replaceFile(path, bb); rerr != nil {
		return nil, "", false, err
	}
	c, err = Load(path)
	if err != nil {
		return nil, "", false, err
	}
	return c, string(bb), true, nil
}

func replaceFile(path string, body []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".monitor-config-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	ok = true
	return nil
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
