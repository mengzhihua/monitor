package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestParseValidAndInvalid(t *testing.T) {
	c, err := Parse([]byte("mode: hub\nglobal:\n  hostname: box-1\n"))
	if err != nil {
		t.Fatalf("valid yaml: %v", err)
	}
	if c.Mode != "hub" || c.Global.Hostname != "box-1" {
		t.Fatalf("got mode=%q hostname=%q", c.Mode, c.Global.Hostname)
	}
	if c, err = Parse(nil); err != nil || c.Mode != "agent" {
		t.Fatalf("empty input should yield defaults, got %v %v", c, err)
	}
	if _, err = Parse([]byte("mode: [broken")); err == nil {
		t.Fatal("broken yaml should fail")
	}
	if _, err = Parse([]byte("mode: satellite")); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("unknown mode should fail with a mode error, got %v", err)
	}
}

func TestSaveWritesContentAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "monitor.yaml")
	if err := os.WriteFile(path, []byte("mode: agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("mode: agent\nglobal:\n  hostname: renamed\n")); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "mode: agent\nglobal:\n  hostname: renamed\n" {
		t.Fatalf("content = %q err=%v", got, err)
	}
	bak, err := os.ReadFile(path + ".bak")
	if err != nil || string(bak) != "mode: agent\n" {
		t.Fatalf("backup = %q err=%v", bak, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions not preserved: %v", info.Mode())
	}
}

func TestSaveNewFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "monitor.yaml")
	if err := Save(path, []byte("mode: agent\n")); err != nil {
		t.Fatalf("save into missing dir/file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "mode: agent\n" {
		t.Fatalf("content = %q err=%v", got, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("no backup expected for a new file, got %v", err)
	}
}

func TestCheckLocked(t *testing.T) {
	base := "mode: agent\nstream:\n  destinations: [wss://hub:20443]\n  api_key: k1\n"
	cur, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	same, _ := Parse([]byte(base + "global:\n  hostname: other\n"))
	if err := CheckLocked(cur, same); err != nil {
		t.Fatalf("unrelated change must pass: %v", err)
	}
	bad, _ := Parse([]byte(strings.Replace(base, "mode: agent", "mode: hub", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("mode change must fail, got %v", err)
	}
	bad, _ = Parse([]byte(strings.Replace(base, "wss://hub:20443", "wss://evil:1", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "destinations") {
		t.Fatalf("destination change must fail, got %v", err)
	}
	bad, _ = Parse([]byte(strings.Replace(base, "api_key: k1", "api_key: k2", 1)))
	if err := CheckLocked(cur, bad); err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("api_key change must fail, got %v", err)
	}
}

func TestSaveKeepsConfigAndBackupPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission semantics")
	}
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "monitor.yaml")
			if existing {
				if err := os.WriteFile(path, []byte("old secret"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path+".bak", []byte("older secret"), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := Save(path, []byte("new secret")); err != nil {
				t.Fatal(err)
			}
			paths := []string{path}
			if existing {
				paths = append(paths, path+".bak")
			}
			for _, saved := range paths {
				info, err := os.Stat(saved)
				if err != nil || info.Mode().Perm() != 0o600 {
					t.Fatalf("credentials are not private: path=%s info=%v err=%v", saved, info, err)
				}
			}
		})
	}
}

func TestSaveReplacesBackupWithoutFollowingSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink availability differs on Windows")
	}
	dir := t.TempDir()
	path, unrelated := filepath.Join(dir, "monitor.yaml"), filepath.Join(dir, "unrelated")
	for file, content := range map[string]string{path: "old config", unrelated: "keep"} {
		if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(unrelated, path+".bak"); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("new config")); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{path: "new config", path + ".bak": "old config", unrelated: "keep"} {
		if got, err := os.ReadFile(file); err != nil || string(got) != want {
			t.Fatalf("%s = %q, want %q; err=%v", file, got, want, err)
		}
	}
	info, err := os.Lstat(path + ".bak")
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("backup must be a regular file: %v %v", info, err)
	}
}

func TestSaveBackupFailurePreservesCurrentFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path+".bak", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, []byte("new")); err == nil {
		t.Fatal("expected backup failure")
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "keep" {
		t.Fatalf("failed backup changed current file: %q %v", got, err)
	}
}

func TestSaveUnchangedSecuresLegacyFileWithoutReplacingBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "monitor.yaml")
			target := path
			if symlink {
				target = filepath.Join(dir, "external.yaml")
			}
			if err := os.WriteFile(target, []byte("same secret"), 0o644); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path+".bak", []byte("rollback secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := Save(path, []byte("same secret")); err != nil {
				t.Fatal(err)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("unchanged legacy file remains exposed: %v %v", info, err)
			}
			if got, err := os.ReadFile(path + ".bak"); err != nil || string(got) != "rollback secret" {
				t.Fatalf("permission upgrade changed backup: %q %v", got, err)
			}
			if symlink {
				external, err := os.Stat(target)
				if err != nil || external.Mode().Perm() != 0o644 {
					t.Fatalf("permission upgrade followed symlink: %v %v", external, err)
				}
			}
			if err := Save(path, []byte("same secret")); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(path)
			if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
				t.Fatalf("private unchanged file was rewritten: %v %v", after, err)
			}
		})
	}
}

func TestSaveIfUpdatedChecksAfterAcquiringWriteLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	if err := Save(path, []byte("initial")); err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, past, past); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := info.ModTime().UnixMicro()
	// Emulate a remote apply that already owns the shared write lock while a
	// local request has read and validated its original modification time.
	saveMu.Lock()
	result := make(chan error, 1)
	go func() { result <- SaveIfUpdated(path, []byte("stale local draft"), &expected) }()
	writeErr := writePrivateFile(path, []byte("remote update"))
	saveMu.Unlock()
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if err := <-result; !errors.Is(err, ErrFileConflict) {
		t.Fatalf("write after remote apply must conflict: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "remote update" {
		t.Fatalf("stale draft overwrote remote apply: %q %v", got, err)
	}
	if _, err := os.Stat(path + ".bak"); !os.IsNotExist(err) {
		t.Fatalf("conflict must not create backup: %v", err)
	}
}

func TestSaveIfUpdatedInitialAndCurrentRevisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.yaml")
	zero := int64(0)
	if err := SaveIfUpdated(path, []byte("initial"), &zero); err != nil {
		t.Fatal(err)
	}
	if err := SaveIfUpdated(path, []byte("stale initial"), &zero); !errors.Is(err, ErrFileConflict) {
		t.Fatalf("duplicate initial write = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	expected := info.ModTime().UnixMicro()
	if err := SaveIfUpdated(path, []byte("updated"), &expected); err != nil {
		t.Fatalf("current revision rejected: %v", err)
	}
	if got, err := os.ReadFile(path + ".bak"); err != nil || string(got) != "initial" {
		t.Fatalf("accepted CAS lost backup: %q %v", got, err)
	}
}

func TestSaveUnchangedSecuresLegacyBackup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}
	for _, mainMode := range []os.FileMode{0o600, 0o644} {
		for _, symlink := range []bool{false, true} {
			t.Run(fmt.Sprintf("main-%o-symlink-%t", mainMode, symlink), func(t *testing.T) {
				dir := t.TempDir()
				path := filepath.Join(dir, "monitor.yaml")
				if err := os.WriteFile(path, []byte("same config"), mainMode); err != nil {
					t.Fatal(err)
				}
				before, err := os.Stat(path)
				if err != nil {
					t.Fatal(err)
				}
				backup := path + ".bak"
				target := backup
				if symlink {
					target = filepath.Join(dir, "old-backup.yaml")
				}
				if err := os.WriteFile(target, []byte("different rollback secret"), 0o644); err != nil {
					t.Fatal(err)
				}
				if symlink {
					if err := os.Symlink(target, backup); err != nil {
						t.Fatal(err)
					}
				}
				if err := Save(path, []byte("same config")); err != nil {
					t.Fatal(err)
				}
				info, err := os.Lstat(backup)
				if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
					t.Fatalf("unchanged save left backup exposed: %v %v", info, err)
				}
				if got, err := os.ReadFile(backup); err != nil || string(got) != "different rollback secret" {
					t.Fatalf("backup contents were replaced: %q %v", got, err)
				}
				if symlink {
					info, err := os.Stat(target)
					if err != nil || info.Mode().Perm() != 0o644 {
						t.Fatalf("backup migration chmodded symlink target: %v %v", info, err)
					}
					if got, err := os.ReadFile(target); err != nil || string(got) != "different rollback secret" {
						t.Fatalf("backup migration changed symlink target: %q %v", got, err)
					}
				}
				if mainMode == 0o600 {
					after, err := os.Stat(path)
					if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
						t.Fatalf("backup migration unnecessarily changed primary file: %v %v", after, err)
					}
				}
			})
		}
	}
}
