package config

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Visual is the subset of monitor.yaml edited from the dashboard form.
// ApplyVisual updates only these fields and leaves alarms, modules, tokens and other keys in place.
type Visual struct {
	Mode               string       `json:"mode"`
	Hostname           string       `json:"hostname"`
	UpdateEvery        int          `json:"update_every"`
	DataDir            string       `json:"data_dir"`
	WebEnabled         string       `json:"web_enabled"` // default | on | off
	Listen             string       `json:"listen"`
	AllowFrom          []string     `json:"allow_from"`
	TicketWebhook      string       `json:"ticket_webhook"`
	Users              []VisualUser `json:"users"`
	CollectorsEnabled  []string     `json:"collectors_enabled"`
	CollectorsDisabled []string     `json:"collectors_disabled"`
	HealthEnabled      string       `json:"health_enabled"` // default | on | off
	HealthSilent       bool         `json:"health_silent"`
	StreamEnabled      bool         `json:"stream_enabled"`
	StreamDestinations []string     `json:"stream_destinations"`
	StreamAPIKey       string       `json:"stream_api_key"`
	StreamProtocol     string       `json:"stream_protocol"`
	HubAPIKeys         []string     `json:"hub_api_keys"`
	HubPeers           []string     `json:"hub_peers"`
	HubStorage         string       `json:"hub_storage"`
	HubSpace           string       `json:"hub_space"`
	HubRoom            string       `json:"hub_room"`
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
	}
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
	return nil
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
