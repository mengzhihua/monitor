// Package backup provides exclusive offline snapshots with integrity verification.
package backup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const manifestName = "monitor-backup.json"

type Manifest struct {
	Version int               `json:"version"`
	Files   map[string]string `json:"files"`
}

func Lock(dir string) (*flock.Flock, error) {
	lock := flock.New(filepath.Join(dir, ".monitor.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("data directory is in use; stop monitord before backup/restore")
	}
	return lock, nil
}

// Create requires an existing, stopped data directory and a new destination.
func Create(src, dst string) error {
	lock, err := Lock(src)
	if err != nil {
		return err
	}
	defer lock.Close()
	return copyTree(src, dst, nil)
}

// Restore verifies the entire backup before creating a new data directory.
func Restore(src, dst string) error {
	b, err := os.ReadFile(filepath.Join(src, manifestName))
	if err != nil {
		return err
	}
	var m Manifest
	if err = json.Unmarshal(b, &m); err != nil {
		return err
	}
	if m.Version != 1 {
		return fmt.Errorf("unsupported backup version")
	}
	for name, want := range m.Files {
		if !filepath.IsLocal(name) {
			return fmt.Errorf("invalid backup path")
		}
		got, err := checksum(filepath.Join(src, name))
		if err != nil {
			return err
		}
		if got != want {
			return fmt.Errorf("backup checksum mismatch: %s", name)
		}
	}
	return copyTree(src, dst, &m)
}

func copyTree(src, dst string, restore *Manifest) error {
	src, err := filepath.Abs(src)
	if err != nil {
		return err
	}
	dst, err = filepath.Abs(dst)
	if err != nil {
		return err
	}
	// Resolving the destination's parent prevents a symlink from hiding recursion.
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(dst))
	if err != nil {
		return err
	}
	dst = filepath.Join(parent, filepath.Base(dst))
	if dst == src || strings.HasPrefix(dst, src+string(os.PathSeparator)) {
		return fmt.Errorf("destination must be outside source")
	}
	if err = os.Mkdir(dst, 0700); err != nil {
		return fmt.Errorf("destination must be new: %w", err)
	}
	m := Manifest{Version: 1, Files: map[string]string{}}
	err = filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlinks not supported: %s", rel)
		}
		if d.IsDir() {
			return os.Mkdir(filepath.Join(dst, rel), 0700)
		}
		if rel == ".monitor.lock" || rel == manifestName || strings.HasSuffix(rel, ".tmp") {
			return nil
		}
		if restore != nil {
			if _, ok := restore.Files[rel]; !ok {
				return fmt.Errorf("unlisted backup file: %s", rel)
			}
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		st, err := in.Stat()
		if err != nil {
			return err
		}
		if !st.Mode().IsRegular() {
			return fmt.Errorf("not a regular file: %s", rel)
		}
		out, err := os.OpenFile(filepath.Join(dst, rel), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		h := sha256.New()
		_, err = io.Copy(io.MultiWriter(out, h), in)
		if err == nil {
			err = out.Sync()
		}
		closeErr := out.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		sum := hex.EncodeToString(h.Sum(nil))
		if restore != nil && sum != restore.Files[rel] {
			return fmt.Errorf("backup changed while restoring: %s", rel)
		}
		m.Files[rel] = sum
		return nil
	})
	if err != nil {
		return err
	}
	if restore != nil {
		return nil
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dst, manifestName), b, 0600)
}
func checksum(path string) (string, error) {
	st, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !st.Mode().IsRegular() {
		return "", fmt.Errorf("not a regular backup file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	_, err = io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), err
}
