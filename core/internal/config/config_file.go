package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"

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

var saveMu sync.Mutex

// ErrFileConflict means another writer changed the config after it was read.
var ErrFileConflict = errors.New("config changed since read")

// Save atomically replaces the backup and current file, keeping credentials
// private. All in-process config writers share the same backup/write lock.
func Save(path string, content []byte) error {
	return SaveIfUpdated(path, content, nil)
}

// SaveIfUpdated checks the observed Unix-microsecond modification time while
// holding the write lock shared with remote apply. Nil preserves unconditional
// writes; zero requires that the file is still absent.
func SaveIfUpdated(path string, content []byte, expected *int64) error {
	saveMu.Lock()
	defer saveMu.Unlock()
	if expected != nil {
		var updated int64
		info, err := os.Stat(path)
		if err == nil {
			updated = info.ModTime().UnixMicro()
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if *expected != updated {
			return ErrFileConflict
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	current, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(current, content) {
			if err := secureExistingPrivateFile(path + ".bak"); err != nil {
				return fmt.Errorf("secure backup %s: %w", path+".bak", err)
			}
			info, err := os.Lstat(path)
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm() == 0o600) {
				return nil
			}
			// Tighten legacy permissions without changing rollback contents or
			// following a config symlink with Chmod.
			return writePrivateFile(path, content)
		}
		if err := writePrivateFile(path+".bak", current); err != nil {
			return fmt.Errorf("backup %s: %w", path+".bak", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return writePrivateFile(path, content)
}

// secureExistingPrivateFile preserves an old backup's contents while replacing
// permissive files or symlinks. Chmod would follow and modify symlink targets.
func secureExistingPrivateFile(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() && (runtime.GOOS == "windows" || info.Mode().Perm() == 0o600) {
		return nil
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return writePrivateFile(path, content)
}

func writePrivateFile(path string, content []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".monitor-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
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
