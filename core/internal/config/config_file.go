package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"gopkg.in/yaml.v3"
)

// Parse decodes YAML bytes on top of Default(). It is the validation entry
// for config edits (web API, agent apply) and is also used by Load; it never
// touches the environment.
func Parse(b []byte) (*Config, error) {
	c := Default()
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	if c.Global.UpdateEvery < 1 {
		c.Global.UpdateEvery = 1
	}
	if c.Global.UpdateEvery > 3600 {
		c.Global.UpdateEvery = 3600
	}
	if c.Mode != "" && c.Mode != "agent" && c.Mode != "hub" {
		return nil, fmt.Errorf("config: unknown mode %q (agent|hub)", c.Mode)
	}
	return c, nil
}

// Save backs up the current file (if any) to path+".bak" and atomically
// writes content, preserving the existing permissions.
func Save(path string, content []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if cur, err := os.ReadFile(path); err == nil {
			if err := os.WriteFile(path+".bak", cur, mode); err != nil {
				return fmt.Errorf("backup %s: %w", path+".bak", err)
			}
		}
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), mode); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// CheckLocked verifies the fields an agent must not change through a
// hub-pushed config: mode and the stream endpoint/credential. Editing those
// remotely could brick the agent's path back to the hub.
func CheckLocked(cur, next *Config) error {
	if cur.Mode != next.Mode {
		return fmt.Errorf("mode is locked (current: %q)", cur.Mode)
	}
	if !reflect.DeepEqual(cur.Stream.Destinations, next.Stream.Destinations) {
		return fmt.Errorf("stream.destinations is locked (current: %v)", cur.Stream.Destinations)
	}
	if cur.Stream.APIKey != next.Stream.APIKey {
		return fmt.Errorf("stream.api_key is locked")
	}
	return nil
}
