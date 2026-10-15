package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Optional target lists distinguish an untouched legacy module from an explicit
// empty list. Secret pointers likewise distinguish omission from explicit removal.
type VisualSNMPTarget struct {
	Name           string  `json:"name" yaml:"name"`
	Address        string  `json:"address" yaml:"address"`
	Profile        string  `json:"profile" yaml:"profile"`
	Version        string  `json:"version" yaml:"version"`
	Community      *string `json:"community,omitempty" yaml:"community"`
	User           string  `json:"user" yaml:"user"`
	AuthProtocol   string  `json:"auth_protocol" yaml:"auth_protocol"`
	AuthPassphrase *string `json:"auth_passphrase,omitempty" yaml:"auth_passphrase"`
	PrivProtocol   string  `json:"priv_protocol" yaml:"priv_protocol"`
	PrivPassphrase *string `json:"priv_passphrase,omitempty" yaml:"priv_passphrase"`
	Timeout        string  `json:"timeout" yaml:"timeout"`
}

type VisualRedfishTarget struct {
	Name     string           `json:"name" yaml:"name"`
	URL      string           `json:"url" yaml:"url"`
	User     string           `json:"user" yaml:"user"`
	Password *string          `json:"password,omitempty" yaml:"password"`
	Timeout  string           `json:"timeout" yaml:"timeout"`
	TLS      *VisualDeviceTLS `json:"tls,omitempty" yaml:"tls"`
}

type VisualDeviceTLS struct {
	CAFile             *string `json:"ca_file,omitempty" yaml:"ca_file"`
	InsecureSkipVerify *bool   `json:"insecure_skip_verify,omitempty" yaml:"insecure_skip_verify"`
}

func readDeviceTargets(modules map[string]yaml.Node, v *Visual) error {
	for name, module := range modules {
		if name != "snmp" && name != "redfish" {
			continue
		}
		targets := get(&module, "targets")
		legacy := targets == nil && len(module.Content) != 0
		if name == "snmp" {
			v.SNMPLegacy = legacy
		} else {
			v.RedfishLegacy = legacy
		}
		if targets == nil {
			continue
		}
		if targets.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s targets must be a list", name)
		}
		if name == "snmp" {
			list := []VisualSNMPTarget{}
			if err := targets.Decode(&list); err != nil {
				return fmt.Errorf("snmp targets: %w", err)
			}
			v.SNMPTargets = &list
		} else {
			list := []VisualRedfishTarget{}
			if err := targets.Decode(&list); err != nil {
				return fmt.Errorf("redfish targets: %w", err)
			}
			v.RedfishTargets = &list
		}
	}
	return nil
}

var visualDeviceName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,48}$`)

func normalizeDeviceTargets(v *Visual) error {
	check := func(module, name, timeout string, seen map[string]bool) error {
		if !visualDeviceName.MatchString(name) || seen[name] {
			return fmt.Errorf("%s device names must be unique and contain 1..48 letters, digits, _ or -", module)
		}
		seen[name] = true
		if timeout != "" {
			d, err := time.ParseDuration(timeout)
			if err != nil || d <= 0 || d > time.Minute {
				return fmt.Errorf("%s device %s timeout must be greater than 0 and at most 1m", module, name)
			}
		}
		return nil
	}
	if v.SNMPTargets != nil {
		if len(*v.SNMPTargets) > 64 {
			return fmt.Errorf("snmp supports at most 64 devices")
		}
		seen := map[string]bool{}
		for i := range *v.SNMPTargets {
			t := &(*v.SNMPTargets)[i]
			t.Address, t.Profile = strings.TrimSpace(t.Address), strings.TrimSpace(t.Profile)
			t.Version, t.Timeout = strings.TrimSpace(t.Version), strings.TrimSpace(t.Timeout)
			if err := check("snmp", t.Name, t.Timeout, seen); err != nil {
				return err
			}
			if t.Address == "" || strings.ContainsAny(t.Address, " \t\r\n") || strings.HasPrefix(t.Address, "-") {
				return fmt.Errorf("snmp device %s needs a valid address", t.Name)
			}
			switch t.Profile {
			case "", "network", "ups", "printer", "host":
			default:
				return fmt.Errorf("snmp device %s has an unsupported profile", t.Name)
			}
			switch t.Version {
			case "", "1", "2c", "3":
			default:
				return fmt.Errorf("snmp device %s version must be 1, 2c or 3", t.Name)
			}
		}
	}
	if v.RedfishTargets != nil {
		if len(*v.RedfishTargets) > 64 {
			return fmt.Errorf("redfish supports at most 64 devices")
		}
		seen := map[string]bool{}
		for i := range *v.RedfishTargets {
			t := &(*v.RedfishTargets)[i]
			t.URL, t.Timeout = strings.TrimSpace(t.URL), strings.TrimSpace(t.Timeout)
			if err := check("redfish", t.Name, t.Timeout, seen); err != nil {
				return err
			}
			u, err := url.Parse(t.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
				return fmt.Errorf("redfish device %s needs an http(s) URL without credentials, query or fragment", t.Name)
			}
		}
	}
	return nil
}

func applyDeviceTargets(cols *yaml.Node, v Visual) error {
	if v.SNMPTargets != nil {
		if err := mergeDeviceList(cols, "snmp", len(*v.SNMPTargets), func(i int) (string, func(*yaml.Node)) {
			t := (*v.SNMPTargets)[i]
			return t.Name, func(n *yaml.Node) {
				for _, f := range [][2]string{{"name", t.Name}, {"address", t.Address}, {"profile", t.Profile}, {"version", t.Version}, {"user", t.User}, {"auth_protocol", t.AuthProtocol}, {"priv_protocol", t.PrivProtocol}, {"timeout", t.Timeout}} {
					setDeviceString(n, f[0], f[1])
				}
				setDeviceSecret(n, "community", t.Community)
				setDeviceSecret(n, "auth_passphrase", t.AuthPassphrase)
				setDeviceSecret(n, "priv_passphrase", t.PrivPassphrase)
			}
		}); err != nil {
			return err
		}
	}
	if v.RedfishTargets != nil {
		return mergeDeviceList(cols, "redfish", len(*v.RedfishTargets), func(i int) (string, func(*yaml.Node)) {
			t := (*v.RedfishTargets)[i]
			return t.Name, func(n *yaml.Node) {
				for _, f := range [][2]string{{"name", t.Name}, {"url", t.URL}, {"user", t.User}, {"timeout", t.Timeout}} {
					setDeviceString(n, f[0], f[1])
				}
				setDeviceSecret(n, "password", t.Password)
				if t.TLS != nil && (t.TLS.CAFile != nil || t.TLS.InsecureSkipVerify != nil) {
					tls := ensureMap(n, "tls")
					setDeviceSecret(tls, "ca_file", t.TLS.CAFile)
					if t.TLS.InsecureSkipVerify != nil {
						setBool(tls, "insecure_skip_verify", *t.TLS.InsecureSkipVerify)
					}
				}
			}
		})
	}
	return nil
}

func setDeviceString(n *yaml.Node, key, value string) {
	if old := get(n, key); old != nil && old.Kind == yaml.ScalarNode && old.Value == value {
		return
	}
	setOptionalString(n, key, value)
}

func setDeviceSecret(n *yaml.Node, key string, value *string) {
	if value == nil {
		return
	}
	if old := get(n, key); old != nil && old.Kind == yaml.ScalarNode && old.Value == *value {
		return
	}
	if *value == "" {
		deleteKey(n, key)
	} else {
		setString(n, key, *value)
	}
}

// Merge by stable name, retaining per-device custom OIDs, TLS fields and future
// options. A legacy singleton must be deliberately migrated through YAML first.
func mergeDeviceList(cols *yaml.Node, module string, count int, item func(int) (string, func(*yaml.Node))) error {
	mods := ensureMap(cols, "modules")
	mod := get(mods, module)
	if mod != nil && (mod.Kind != yaml.MappingNode || (get(mod, "targets") == nil && len(mod.Content) != 0)) {
		return fmt.Errorf("%s uses legacy settings; migrate them to a named targets list in YAML before editing devices", module)
	}
	if mod == nil {
		mod = ensureMap(mods, module)
	}
	old := get(mod, "targets")
	byName := map[string]*yaml.Node{}
	if old != nil {
		if old.Kind != yaml.SequenceNode {
			return fmt.Errorf("%s targets must be a list", module)
		}
		for _, node := range old.Content {
			name := nodeScalar(*node, "name")
			if name == "" || byName[name] != nil || node.Kind != yaml.MappingNode {
				return fmt.Errorf("%s existing targets need unique names before form editing", module)
			}
			byName[name] = node
		}
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for i := 0; i < count; i++ {
		name, update := item(i)
		n := byName[name]
		if n == nil {
			n = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		}
		update(n)
		if module == "snmp" {
			if nodeScalar(*n, "version") == "3" {
				if strings.TrimSpace(nodeScalar(*n, "user")) == "" {
					return fmt.Errorf("snmp device %s requires a v3 user", name)
				}
				if nodeScalar(*n, "priv_passphrase") != "" && nodeScalar(*n, "auth_passphrase") == "" {
					return fmt.Errorf("snmp device %s privacy requires authentication", name)
				}
			} else if nodeScalar(*n, "community") == "" {
				return fmt.Errorf("snmp device %s requires a read-only community", name)
			}
		}
		seq.Content = append(seq.Content, n)
	}
	replaceKey(mod, "targets", seq)
	return nil
}
