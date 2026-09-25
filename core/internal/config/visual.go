package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Visual is the subset of monitor.yaml edited from the dashboard form.
// ApplyVisual updates only these fields and leaves alarms, modules, tokens and other keys in place.
type Visual struct {
	Mode               string         `json:"mode"`
	Hostname           string         `json:"hostname"`
	UpdateEvery        int            `json:"update_every"`
	DataDir            string         `json:"data_dir"`
	WebEnabled         string         `json:"web_enabled"` // default | on | off
	Listen             string         `json:"listen"`
	AllowFrom          []string       `json:"allow_from"`
	TicketWebhook      string         `json:"ticket_webhook"`
	Users              []VisualUser   `json:"users"`
	CollectorsEnabled  []string       `json:"collectors_enabled"`
	CollectorsDisabled []string       `json:"collectors_disabled"`
	HealthEnabled      string         `json:"health_enabled"` // default | on | off
	HealthSilent       bool           `json:"health_silent"`
	StreamEnabled      bool           `json:"stream_enabled"`
	StreamDestinations []string       `json:"stream_destinations"`
	StreamAPIKey       string         `json:"stream_api_key"`
	StreamProtocol     string         `json:"stream_protocol"`
	HubAPIKeys         []string       `json:"hub_api_keys"`
	HubPeers           []string       `json:"hub_peers"`
	HubStorage         string         `json:"hub_storage"`
	HubSpace           string         `json:"hub_space"`
	HubRoom            string         `json:"hub_room"`
	Notify             VisualNotify   `json:"notify"`
	Targets            []VisualTarget `json:"targets"`
}

// VisualTarget is one collector endpoint. Timeout, password and jobs stay in the YAML module.
type VisualTarget struct {
	Name    string `json:"name"`
	Fields  string `json:"fields"`
	URL     string `json:"url"`
	Address string `json:"address"`
	Listen  string `json:"listen"`
	User    string `json:"user"`
}

// VisualNotify is the channel section of health.notify edited from the form.
// Fields that are not listed here, including webhook headers and email passwords, stay in the file.
type VisualNotify struct {
	WebhookURL          string       `json:"webhook_url"`
	SlackWebhookURL     string       `json:"slack_webhook_url"`
	SlackChannel        string       `json:"slack_channel"`
	DingTalkWebhookURL  string       `json:"dingtalk_webhook_url"`
	WeComWebhookURL     string       `json:"wecom_webhook_url"`
	FeishuWebhookURL    string       `json:"feishu_webhook_url"`
	FeishuWebhookURLEnv string       `json:"feishu_webhook_url_env"`
	FeishuSecret        string       `json:"feishu_secret"`
	FeishuSecretEnv     string       `json:"feishu_secret_env"`
	EmailServer         string       `json:"email_server"`
	EmailFrom           string       `json:"email_from"`
	EmailTo             []string     `json:"email_to"`
	TelegramToken       string       `json:"telegram_token"`
	TelegramChatID      string       `json:"telegram_chat_id"`
	DiscordWebhookURL   string       `json:"discord_webhook_url"`
	NtfyURL             string       `json:"ntfy_url"`
	NtfyTopic           string       `json:"ntfy_topic"`
	NtfyTopicEnv        string       `json:"ntfy_topic_env"`
	GotifyURL           string       `json:"gotify_url"`
	GotifyToken         string       `json:"gotify_token"`
	GotifyTokenEnv      string       `json:"gotify_token_env"`
	BarkURL             string       `json:"bark_url"`
	BarkDeviceKey       string       `json:"bark_device_key"`
	BarkDeviceKeyEnv    string       `json:"bark_device_key_env"`
	Roles               []VisualRole `json:"roles"`
}

type VisualRole struct {
	Name     string   `json:"name"`
	Channels []string `json:"channels"`
}

type VisualUser struct {
	Name  string `json:"name"`
	Token string `json:"token"`
	Role  string `json:"role"`
}

func emptyVisual() Visual {
	return Visual{
		Mode: "agent", UpdateEvery: 1, WebEnabled: "default", HealthEnabled: "default",
		AllowFrom: []string{}, Users: []VisualUser{}, CollectorsEnabled: []string{}, CollectorsDisabled: []string{},
		StreamDestinations: []string{}, HubAPIKeys: []string{}, HubPeers: []string{},
		Notify:  VisualNotify{EmailTo: []string{}, Roles: []VisualRole{}},
		Targets: catalogTargets(),
	}
}

type targetSpec struct {
	name    string
	url     bool
	address bool
	listen  bool
	user    bool
}

var targetCatalog = []targetSpec{
	{"nginx", true, false, false, false},
	{"apache", true, false, false, false},
	{"phpfpm", true, false, false, false},
	{"elasticsearch", true, false, false, true},
	{"rabbitmq", true, false, false, true},
	{"redis", false, true, false, false},
	{"memcached", false, true, false, false},
	{"mysql", false, true, false, true},
	{"postgres", false, true, false, true},
	{"docker", false, true, false, false},
	{"statsd", false, false, true, false},
	{"otlp", false, false, true, false},
}

func (s targetSpec) fields() string {
	var parts []string
	if s.url {
		parts = append(parts, "url")
	}
	if s.address {
		parts = append(parts, "address")
	}
	if s.listen {
		parts = append(parts, "listen")
	}
	if s.user {
		parts = append(parts, "user")
	}
	return strings.Join(parts, ",")
}

func catalogTargets() []VisualTarget {
	out := make([]VisualTarget, len(targetCatalog))
	for i, s := range targetCatalog {
		out[i] = VisualTarget{Name: s.name, Fields: s.fields()}
	}
	return out
}

func lookupTarget(name string) (targetSpec, bool) {
	for _, s := range targetCatalog {
		if s.name == name {
			return s, true
		}
	}
	return targetSpec{}, false
}

// VisualFrom reads the form fields from raw YAML. Missing keys stay at their display defaults.
func VisualFrom(raw string) (Visual, error) {
	v := emptyVisual()
	if strings.TrimSpace(raw) == "" {
		return v, nil
	}
	var c Config
	if err := yaml.Unmarshal([]byte(raw), &c); err != nil {
		return Visual{}, err
	}
	if c.Mode != "" {
		v.Mode = c.Mode
	}
	v.Hostname = c.Global.Hostname
	if c.Global.UpdateEvery > 0 {
		v.UpdateEvery = c.Global.UpdateEvery
	}
	v.DataDir = c.Global.DataDir
	switch {
	case c.Web.Enabled == nil:
		v.WebEnabled = "default"
	case *c.Web.Enabled:
		v.WebEnabled = "on"
	default:
		v.WebEnabled = "off"
	}
	v.Listen = c.Web.Listen
	v.AllowFrom = append([]string{}, c.Web.AllowFrom...)
	v.TicketWebhook = c.Web.TicketWebhook
	for _, u := range c.Web.Users {
		v.Users = append(v.Users, VisualUser{Name: u.Name, Token: u.Token, Role: u.Role})
	}
	v.CollectorsEnabled = append([]string{}, c.Collectors.Enabled...)
	v.CollectorsDisabled = append([]string{}, c.Collectors.Disabled...)
	v.Targets = catalogTargets()
	for i, t := range v.Targets {
		node, ok := c.Collectors.Modules[t.Name]
		if !ok {
			continue
		}
		v.Targets[i].URL = nodeScalar(node, "url")
		v.Targets[i].Address = nodeScalar(node, "address")
		v.Targets[i].Listen = nodeScalar(node, "listen")
		v.Targets[i].User = nodeScalar(node, "user")
	}
	switch {
	case c.Health.Enabled == nil:
		v.HealthEnabled = "default"
	case *c.Health.Enabled:
		v.HealthEnabled = "on"
	default:
		v.HealthEnabled = "off"
	}
	v.HealthSilent = c.Health.Silent
	v.StreamEnabled = c.Stream.Enabled
	v.StreamDestinations = append([]string{}, c.Stream.Destinations...)
	v.StreamAPIKey = c.Stream.APIKey
	v.StreamProtocol = c.Stream.Protocol
	v.HubAPIKeys = append([]string{}, c.Hub.APIKeys...)
	v.HubPeers = append([]string{}, c.Hub.Peers...)
	v.HubStorage = c.Hub.Storage
	v.HubSpace = c.Hub.Space
	v.HubRoom = c.Hub.Room
	n := c.Health.Notify
	v.Notify = VisualNotify{
		WebhookURL: n.Webhook.URL, SlackWebhookURL: n.Slack.WebhookURL, SlackChannel: n.Slack.Channel,
		DingTalkWebhookURL: n.DingTalk.WebhookURL, WeComWebhookURL: n.WeCom.WebhookURL,
		FeishuWebhookURL: n.Feishu.WebhookURL, FeishuWebhookURLEnv: n.Feishu.WebhookURLEnv,
		FeishuSecret: n.Feishu.Secret, FeishuSecretEnv: n.Feishu.SecretEnv,
		EmailServer: n.Email.Server, EmailFrom: n.Email.From, EmailTo: append([]string{}, n.Email.To...),
		TelegramToken: n.Telegram.Token, TelegramChatID: n.Telegram.ChatID,
		DiscordWebhookURL: n.Discord.WebhookURL,
		NtfyURL:           n.Ntfy.URL, NtfyTopic: n.Ntfy.Topic, NtfyTopicEnv: n.Ntfy.TopicEnv,
		GotifyURL: n.Gotify.URL, GotifyToken: n.Gotify.Token, GotifyTokenEnv: n.Gotify.TokenEnv,
		BarkURL: n.Bark.URL, BarkDeviceKey: n.Bark.DeviceKey, BarkDeviceKeyEnv: n.Bark.DeviceKeyEnv,
		Roles: rolesFrom(n.Roles),
	}
	if v.Notify.EmailTo == nil {
		v.Notify.EmailTo = []string{}
	}
	if err := v.normalize(); err != nil {
		return Visual{}, err
	}
	return v, nil
}

// ApplyVisual merges form fields into raw YAML and returns the updated document.
func ApplyVisual(raw string, v Visual) (string, error) {
	if err := v.normalize(); err != nil {
		return "", err
	}
	var doc yaml.Node
	if strings.TrimSpace(raw) == "" {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}}
	} else if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
		return "", err
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("config root must be a map")
	}
	root := doc.Content[0]
	setString(root, "mode", v.Mode)
	global := ensureMap(root, "global")
	setOptionalString(global, "hostname", v.Hostname)
	setInt(global, "update_every", v.UpdateEvery)
	setOptionalString(global, "data_dir", v.DataDir)
	web := ensureMap(root, "web")
	switch v.WebEnabled {
	case "default":
		deleteKey(web, "enabled")
	case "on":
		setBool(web, "enabled", true)
	default:
		setBool(web, "enabled", false)
	}
	setOptionalString(web, "listen", v.Listen)
	setStrings(web, "allow_from", v.AllowFrom)
	setOptionalString(web, "ticket_webhook", v.TicketWebhook)
	setUsers(web, v.Users)
	cols := ensureMap(root, "collectors")
	setStrings(cols, "enabled", v.CollectorsEnabled)
	setStrings(cols, "disabled", v.CollectorsDisabled)
	if err := applyTargets(cols, v.Targets); err != nil {
		return "", err
	}
	health := ensureMap(root, "health")
	switch v.HealthEnabled {
	case "default":
		deleteKey(health, "enabled")
	case "on":
		setBool(health, "enabled", true)
	default:
		setBool(health, "enabled", false)
	}
	setBool(health, "silent", v.HealthSilent)
	applyNotify(health, v.Notify)
	stream := ensureMap(root, "stream")
	setBool(stream, "enabled", v.StreamEnabled)
	setStrings(stream, "destinations", v.StreamDestinations)
	setOptionalString(stream, "api_key", v.StreamAPIKey)
	setOptionalString(stream, "protocol", v.StreamProtocol)
	hub := ensureMap(root, "hub")
	setStrings(hub, "api_keys", v.HubAPIKeys)
	setStrings(hub, "peers", v.HubPeers)
	setOptionalString(hub, "storage", v.HubStorage)
	setOptionalString(hub, "space", v.HubSpace)
	setOptionalString(hub, "room", v.HubRoom)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		_ = enc.Close()
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

func (v *Visual) normalize() error {
	v.Mode = strings.TrimSpace(v.Mode)
	if v.Mode != "agent" && v.Mode != "hub" {
		return fmt.Errorf("mode must be agent or hub")
	}
	if v.UpdateEvery < 1 || v.UpdateEvery > 3600 {
		return fmt.Errorf("update_every must be 1..3600")
	}
	v.WebEnabled = strings.TrimSpace(v.WebEnabled)
	v.HealthEnabled = strings.TrimSpace(v.HealthEnabled)
	if v.WebEnabled != "default" && v.WebEnabled != "on" && v.WebEnabled != "off" {
		return fmt.Errorf("web_enabled must be default, on or off")
	}
	if v.HealthEnabled != "default" && v.HealthEnabled != "on" && v.HealthEnabled != "off" {
		return fmt.Errorf("health_enabled must be default, on or off")
	}
	v.Hostname = strings.TrimSpace(v.Hostname)
	v.DataDir = strings.TrimSpace(v.DataDir)
	v.Listen = strings.TrimSpace(v.Listen)
	v.TicketWebhook = strings.TrimSpace(v.TicketWebhook)
	if strings.ContainsAny(v.Hostname, "\r\n") || strings.ContainsAny(v.Listen, "\r\n") || strings.ContainsAny(v.DataDir, "\r\n") {
		return fmt.Errorf("hostname, listen and data_dir must be single line")
	}
	if v.TicketWebhook != "" {
		u, err := url.Parse(v.TicketWebhook)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("ticket_webhook must be an http(s) URL")
		}
	}
	v.AllowFrom = cleanList(v.AllowFrom)
	for _, cidr := range v.AllowFrom {
		if !validAllow(cidr) {
			return fmt.Errorf("allow_from %q is not an IP or CIDR", cidr)
		}
	}
	seen := map[string]bool{}
	for i := range v.Users {
		u := &v.Users[i]
		u.Name = strings.TrimSpace(u.Name)
		u.Token = strings.TrimSpace(u.Token)
		u.Role = strings.TrimSpace(u.Role)
		if u.Name == "" || u.Token == "" {
			return fmt.Errorf("each user needs a name and token")
		}
		if u.Role != "admin" && u.Role != "troubleshooter" && u.Role != "viewer" {
			return fmt.Errorf("user %s role must be admin, troubleshooter or viewer", u.Name)
		}
		if seen[u.Token] {
			return fmt.Errorf("user token reused")
		}
		seen[u.Token] = true
	}
	var err error
	if v.CollectorsEnabled, err = names(v.CollectorsEnabled); err != nil {
		return err
	}
	if v.CollectorsDisabled, err = names(v.CollectorsDisabled); err != nil {
		return err
	}
	v.StreamDestinations = cleanList(v.StreamDestinations)
	v.HubAPIKeys = cleanList(v.HubAPIKeys)
	v.HubPeers = cleanList(v.HubPeers)
	v.StreamAPIKey = strings.TrimSpace(v.StreamAPIKey)
	v.StreamProtocol = strings.TrimSpace(v.StreamProtocol)
	switch v.StreamProtocol {
	case "", "stream", "mqtt", "aclk":
	default:
		return fmt.Errorf("stream protocol must be stream, mqtt or aclk")
	}
	v.HubStorage = strings.TrimSpace(v.HubStorage)
	switch v.HubStorage {
	case "", "full", "proxy":
	default:
		return fmt.Errorf("hub storage must be full or proxy")
	}
	v.HubSpace = strings.TrimSpace(v.HubSpace)
	v.HubRoom = strings.TrimSpace(v.HubRoom)
	if v.StreamEnabled && len(v.StreamDestinations) == 0 {
		return fmt.Errorf("stream destinations are required when streaming is on")
	}
	if err := v.Notify.normalize(); err != nil {
		return err
	}
	return normalizeTargets(v.Targets)
}

func normalizeTargets(targets []VisualTarget) error {
	seen := map[string]bool{}
	for i := range targets {
		t := &targets[i]
		spec, ok := lookupTarget(t.Name)
		if !ok {
			return fmt.Errorf("unknown collector target %q", t.Name)
		}
		if seen[t.Name] {
			return fmt.Errorf("collector target %s repeated", t.Name)
		}
		seen[t.Name] = true
		t.Fields = spec.fields()
		t.URL, t.Address, t.Listen, t.User = strings.TrimSpace(t.URL), strings.TrimSpace(t.Address), strings.TrimSpace(t.Listen), strings.TrimSpace(t.User)
		if !spec.url {
			t.URL = ""
		}
		if !spec.address {
			t.Address = ""
		}
		if !spec.listen {
			t.Listen = ""
		}
		if !spec.user {
			t.User = ""
		}
		if t.URL != "" {
			u, err := url.Parse(t.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return fmt.Errorf("%s url must be http(s)", t.Name)
			}
		}
		for _, value := range []string{t.Address, t.Listen, t.User} {
			if strings.ContainsAny(value, " \r\n") {
				return fmt.Errorf("%s endpoint must be a single token", t.Name)
			}
		}
	}
	return nil
}

func (n *VisualNotify) normalize() error {
	urls := []*string{&n.WebhookURL, &n.SlackWebhookURL, &n.DingTalkWebhookURL, &n.WeComWebhookURL, &n.FeishuWebhookURL, &n.DiscordWebhookURL, &n.NtfyURL, &n.GotifyURL, &n.BarkURL}
	for _, p := range urls {
		*p = strings.TrimSpace(*p)
		if *p == "" {
			continue
		}
		u, err := url.Parse(*p)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("notification URL must be http(s)")
		}
	}
	envs := []*string{&n.FeishuWebhookURLEnv, &n.FeishuSecretEnv, &n.NtfyTopicEnv, &n.GotifyTokenEnv, &n.BarkDeviceKeyEnv}
	for _, p := range envs {
		*p = strings.TrimSpace(*p)
		if *p != "" && !envName(*p) {
			return fmt.Errorf("environment variable name %q", *p)
		}
	}
	n.SlackChannel = strings.TrimSpace(n.SlackChannel)
	n.EmailServer = strings.TrimSpace(n.EmailServer)
	n.EmailFrom = strings.TrimSpace(n.EmailFrom)
	n.TelegramToken = strings.TrimSpace(n.TelegramToken)
	n.TelegramChatID = strings.TrimSpace(n.TelegramChatID)
	n.NtfyTopic = strings.TrimSpace(n.NtfyTopic)
	n.GotifyToken = strings.TrimSpace(n.GotifyToken)
	n.FeishuSecret = strings.TrimSpace(n.FeishuSecret)
	n.BarkDeviceKey = strings.TrimSpace(n.BarkDeviceKey)
	n.EmailTo = cleanList(n.EmailTo)
	if n.Roles == nil {
		n.Roles = []VisualRole{}
	}
	seen := map[string]bool{}
	for i := range n.Roles {
		r := &n.Roles[i]
		r.Name = strings.TrimSpace(r.Name)
		if r.Name == "" || seen[r.Name] {
			return fmt.Errorf("notify role name must be unique")
		}
		seen[r.Name] = true
		var err error
		if r.Channels, err = names(r.Channels); err != nil {
			return fmt.Errorf("notify role %s: %w", r.Name, err)
		}
		if len(r.Channels) == 0 {
			return fmt.Errorf("notify role %s needs a channel", r.Name)
		}
	}
	return nil
}

func envName(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		ok := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func rolesFrom(m map[string][]string) []VisualRole {
	if len(m) == 0 {
		return []VisualRole{}
	}
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]VisualRole, 0, len(names))
	for _, name := range names {
		out = append(out, VisualRole{Name: name, Channels: append([]string{}, m[name]...)})
	}
	return out
}

func nodeScalar(n yaml.Node, key string) string {
	if n.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key && n.Content[i+1].Kind == yaml.ScalarNode {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func applyTargets(cols *yaml.Node, targets []VisualTarget) error {
	for _, t := range targets {
		spec, ok := lookupTarget(t.Name)
		if !ok {
			return fmt.Errorf("unknown collector target %q", t.Name)
		}
		mods := get(cols, "modules")
		if t.URL == "" && t.Address == "" && t.Listen == "" && t.User == "" {
			if mods == nil {
				continue
			}
			mod := get(mods, t.Name)
			if mod == nil || mod.Kind != yaml.MappingNode {
				continue
			}
			if spec.url {
				deleteKey(mod, "url")
			}
			if spec.address {
				deleteKey(mod, "address")
			}
			if spec.listen {
				deleteKey(mod, "listen")
			}
			if spec.user {
				deleteKey(mod, "user")
			}
			if len(mod.Content) == 0 {
				deleteKey(mods, t.Name)
			}
			if len(mods.Content) == 0 {
				deleteKey(cols, "modules")
			}
			continue
		}
		mod := ensureMap(ensureMap(cols, "modules"), t.Name)
		if spec.url {
			setOptionalString(mod, "url", t.URL)
		}
		if spec.address {
			setOptionalString(mod, "address", t.Address)
		}
		if spec.listen {
			setOptionalString(mod, "listen", t.Listen)
		}
		if spec.user {
			setOptionalString(mod, "user", t.User)
		}
	}
	return nil
}

func applyNotify(health *yaml.Node, n VisualNotify) {
	notify := ensureMap(health, "notify")
	setOptionalString(ensureMap(notify, "webhook"), "url", n.WebhookURL)
	slack := ensureMap(notify, "slack")
	setOptionalString(slack, "webhook_url", n.SlackWebhookURL)
	setOptionalString(slack, "channel", n.SlackChannel)
	setOptionalString(ensureMap(notify, "dingtalk"), "webhook_url", n.DingTalkWebhookURL)
	setOptionalString(ensureMap(notify, "wecom"), "webhook_url", n.WeComWebhookURL)
	feishu := ensureMap(notify, "feishu")
	setOptionalString(feishu, "webhook_url", n.FeishuWebhookURL)
	setOptionalString(feishu, "webhook_url_env", n.FeishuWebhookURLEnv)
	setOptionalString(feishu, "secret", n.FeishuSecret)
	setOptionalString(feishu, "secret_env", n.FeishuSecretEnv)
	email := ensureMap(notify, "email")
	setOptionalString(email, "server", n.EmailServer)
	setOptionalString(email, "from", n.EmailFrom)
	setStrings(email, "to", n.EmailTo)
	tg := ensureMap(notify, "telegram")
	setOptionalString(tg, "token", n.TelegramToken)
	setOptionalString(tg, "chat_id", n.TelegramChatID)
	setOptionalString(ensureMap(notify, "discord"), "webhook_url", n.DiscordWebhookURL)
	ntfy := ensureMap(notify, "ntfy")
	setOptionalString(ntfy, "url", n.NtfyURL)
	setOptionalString(ntfy, "topic", n.NtfyTopic)
	setOptionalString(ntfy, "topic_env", n.NtfyTopicEnv)
	gotify := ensureMap(notify, "gotify")
	setOptionalString(gotify, "url", n.GotifyURL)
	setOptionalString(gotify, "token", n.GotifyToken)
	setOptionalString(gotify, "token_env", n.GotifyTokenEnv)
	bark := ensureMap(notify, "bark")
	setOptionalString(bark, "url", n.BarkURL)
	setOptionalString(bark, "device_key", n.BarkDeviceKey)
	setOptionalString(bark, "device_key_env", n.BarkDeviceKeyEnv)
	if len(n.Roles) == 0 {
		deleteKey(notify, "roles")
		return
	}
	roles := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, r := range n.Roles {
		setStrings(roles, r.Name, r.Channels)
	}
	replaceKey(notify, "roles", roles)
}

func validAllow(s string) bool {
	if strings.Contains(s, "/") {
		_, _, err := net.ParseCIDR(s)
		return err == nil
	}
	return net.ParseIP(s) != nil
}

func cleanList(in []string) []string {
	if in == nil {
		return []string{}
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func names(in []string) ([]string, error) {
	out := cleanList(in)
	for _, n := range out {
		for _, r := range n {
			if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
				return nil, fmt.Errorf("collector name %q", n)
			}
		}
	}
	return out, nil
}

func ensureMap(parent *yaml.Node, key string) *yaml.Node {
	if v := get(parent, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	replaceKey(parent, key, n)
	return n
}

func get(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func replaceKey(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

func deleteKey(m *yaml.Node, key string) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content = append(m.Content[:i], m.Content[i+2:]...)
			return
		}
	}
}

func setString(m *yaml.Node, key, val string) {
	replaceKey(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: val})
}

func setOptionalString(m *yaml.Node, key, val string) {
	if strings.TrimSpace(val) == "" {
		deleteKey(m, key)
		return
	}
	setString(m, key, val)
}

func setBool(m *yaml.Node, key string, val bool) {
	replaceKey(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(val)})
}

func setInt(m *yaml.Node, key string, val int) {
	replaceKey(m, key, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: strconv.Itoa(val)})
}

func setStrings(m *yaml.Node, key string, vals []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, v := range vals {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: v})
	}
	replaceKey(m, key, seq)
}

func setUsers(web *yaml.Node, users []VisualUser) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, u := range users {
		item := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setString(item, "name", u.Name)
		setString(item, "token", u.Token)
		setString(item, "role", u.Role)
		seq.Content = append(seq.Content, item)
	}
	replaceKey(web, "users", seq)
}
