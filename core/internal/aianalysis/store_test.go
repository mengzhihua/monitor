package aianalysis

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func ownerFor(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func mustStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustSave(t *testing.T, s *Store, owner, base string, private bool) PublicConfig {
	t.Helper()
	c, err := s.Save(owner, Update{BaseURL: base, Model: "test-model", AllowPrivate: private, Revision: s.Get(owner).Revision}, true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestStorePersistenceIsolationAndRevisions(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	owner, other := ownerFor("alice"), ownerFor("bob")
	key := "private-secret-value"
	c, err := s.Save(owner, Update{BaseURL: "https://EXAMPLE.com/v1/", Model: "model", APIKey: &key}, false)
	if err != nil || c.BaseURL != "https://example.com/v1" || c.Revision != 1 || !c.HasAPIKey {
		t.Fatalf("save: %+v %v", c, err)
	}
	if s.Get(other) != (PublicConfig{}) || !s.Persistent() || mustStore(t, "").Persistent() {
		t.Fatal("incorrect identity isolation or persistence mode")
	}
	encoded, _ := json.Marshal(c)
	internal, _ := json.Marshal(s.configs[owner])
	for _, data := range [][]byte{encoded, internal} {
		if strings.Contains(string(data), key) || strings.Contains(string(data), `"api_key"`) {
			t.Fatal("secret exposed in configuration JSON")
		}
	}
	file := filepath.Join(dir, "ai-settings.json")
	info, err := os.Stat(file)
	if err != nil || (runtime.GOOS != "windows" && info.Mode().Perm() != 0600) {
		t.Fatalf("file permissions: %v %v", info, err)
	}
	reopened := mustStore(t, dir)
	if reopened.Get(owner) != c || reopened.configs[owner].APIKey != key {
		t.Fatal("settings not restored")
	}
	if _, err := s.Save(owner, Update{BaseURL: c.BaseURL, Model: "updated"}, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if err := s.Delete(owner, 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete: %v", err)
	}
	if err := s.Delete(owner, c.Revision); err != nil {
		t.Fatal(err)
	}
	deleted := mustStore(t, dir)
	if got := deleted.Get(owner); got != (PublicConfig{Revision: 2}) {
		t.Fatalf("tombstone not preserved: %+v", got)
	}
	if _, err := deleted.Save(owner, Update{BaseURL: c.BaseURL, Model: "model"}, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("ABA save: %v", err)
	}
	if _, err := deleted.Save(owner, Update{BaseURL: c.BaseURL, Model: "model", Revision: 2}, false); err != nil {
		t.Fatal(err)
	}
}

func TestSecretChangesRequireExplicitIntent(t *testing.T) {
	s := mustStore(t, "")
	owner := ownerFor("alice")
	key := "initial-secret"
	c, err := s.Save(owner, Update{BaseURL: "https://first.example/v1", Model: "model", APIKey: &key}, false)
	if err != nil {
		t.Fatal(err)
	}
	update := Update{BaseURL: "https://second.example/v1", Model: "model", Revision: c.Revision}
	if _, err := s.Save(owner, update, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("old credential forwarded to new URL: %v", err)
	}
	if s.Get(owner) != c {
		t.Fatal("rejected save changed settings")
	}
	update.ClearAPIKey = true
	c, err = s.Save(owner, update, false)
	if err != nil || c.HasAPIKey {
		t.Fatalf("clear secret: %+v %v", c, err)
	}
	update = Update{BaseURL: "https://third.example/v1", Model: "model", Revision: c.Revision, APIKey: &key}
	if _, err := s.Save(owner, update, false); err != nil {
		t.Fatal(err)
	}
	update.Revision++
	update.ClearAPIKey = true
	if _, err := s.Save(owner, update, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ambiguous key update: %v", err)
	}
}

func TestConfigValidation(t *testing.T) {
	valid := []string{"https://example.com/v1", "https://8.8.8.8:8443/api", "https://[2606:4700:4700::1111]/v1"}
	for _, base := range valid {
		if _, err := validateBaseURL(base, false); err != nil {
			t.Errorf("valid URL %q: %v", base, err)
		}
	}
	bad := []string{
		"http://example.com/v1", "file:///etc/passwd", "ftp://example.com", "https://user:secret@example.com", "https://example.com?key=x", "https://example.com?", "https://example.com#", "https://example.com#part",
		"https://example.com:0", "https://example.com:65536", "https://example.com:", "https://example.com:bad", "https://", "\nhttps://example.com",
		"https://127.0.0.1", "https://10.0.0.1", "https://[::1]", "https://[fc00::1]", "https://169.254.169.254", "https://[fe80::1]", "https://224.1.1.1", "https://0.0.0.0", "https://[::]", "https://[fe80::1%25en0]",
	}
	for _, base := range bad {
		if _, err := validateBaseURL(base, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid URL accepted %q: %v", base, err)
		}
	}
	for _, base := range []string{"http://127.0.0.1:11434/v1", "http://10.0.0.1/v1", "https://[::1]/v1"} {
		if _, err := validateBaseURL(base, true); err != nil {
			t.Errorf("private URL %q: %v", base, err)
		}
	}
	for _, base := range []string{"http://8.8.8.8", "http://169.254.169.254", "https://[fe80::1]", "https://[ff02::1]", "https://0.0.0.0"} {
		if _, err := validateBaseURL(base, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("forbidden destination with private enabled %q: %v", base, err)
		}
	}
	s := mustStore(t, "")
	owner := ownerFor("alice")
	if _, err := s.Save(owner, Update{BaseURL: "http://127.0.0.1/v1", Model: "model", AllowPrivate: true}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("non-admin enabled private networking: %v", err)
	}
	for _, model := range []string{"", "  ", "\nmodel", "model\x00", strings.Repeat("x", 129)} {
		if _, err := s.Save(owner, Update{BaseURL: "https://example.com", Model: model}, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid model accepted: %v", err)
		}
	}
	for _, key := range []string{"bad\r\nAuthorization: new", strings.Repeat("k", 8193)} {
		if _, err := s.Save(owner, Update{BaseURL: "https://example.com", Model: "model", APIKey: &key}, true); !errors.Is(err, ErrInvalid) {
			t.Errorf("invalid secret accepted: %v", err)
		}
	}
	if _, err := s.Save("invalid-identity", Update{BaseURL: "https://example.com", Model: "model"}, true); !errors.Is(err, ErrInvalid) {
		t.Fatal("invalid owner accepted")
	}
}

func TestCorruptStoreRefused(t *testing.T) {
	owner := ownerFor("alice")
	for name, data := range map[string]string{
		"empty": "", "truncated": `{"version":1`, "wrong_version": `{"version":2,"accounts":{}}`, "missing_accounts": `{"version":1}`,
		"bad_owner":        `{"version":1,"accounts":{"alice":{"revision":1}}}`,
		"zero_revision":    fmt.Sprintf(`{"version":1,"accounts":{%q:{"revision":0}}}`, owner),
		"invalid_url":      fmt.Sprintf(`{"version":1,"accounts":{%q:{"revision":1,"base_url":"file:///tmp","model":"model"}}}`, owner),
		"tombstone_secret": fmt.Sprintf(`{"version":1,"accounts":{%q:{"revision":1,"api_key":"secret"}}}`, owner),
		"too_large":        strings.Repeat(" ", maxStoreSize+1),
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "ai-settings.json"), []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("corrupt store opened: %v", err)
			}
		})
	}
}

func TestFailedWriteKeepsMemoryAndNoTempFile(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	owner := ownerFor("alice")
	c := mustSave(t, s, owner, "https://example.com", false)
	if err := os.Remove(s.path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(s.path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Save(owner, Update{BaseURL: c.BaseURL, Model: "changed", Revision: c.Revision}, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("failed disk write: %v", err)
	}
	if s.Get(owner) != c {
		t.Fatal("failed disk write mutated memory")
	}
	files, err := filepath.Glob(filepath.Join(dir, ".ai-settings-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("temporary secret file left behind: %d %v", len(files), err)
	}
}

func TestStoreCapacityAndSizeBound(t *testing.T) {
	s := mustStore(t, "")
	for i := 0; i < maxAccounts; i++ {
		mustSave(t, s, ownerFor(fmt.Sprint(i)), "https://example.com", false)
	}
	if _, err := s.Save(ownerFor("extra"), Update{BaseURL: "https://example.com", Model: "model"}, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("account limit not enforced: %v", err)
	}
	large := mustStore(t, "")
	key := strings.Repeat("k", 8192)
	for i := 0; i < maxAccounts; i++ {
		owner := ownerFor(fmt.Sprint(i))
		_, err := large.Save(owner, Update{BaseURL: "https://example.com", Model: "model", APIKey: &key}, false)
		if errors.Is(err, ErrUnavailable) {
			if large.Get(owner) != (PublicConfig{}) {
				t.Fatal("oversized store changed memory")
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Fatal("store byte limit not enforced")
}

func TestRevisionsRemainExactInBrowserJSON(t *testing.T) {
	dir := t.TempDir()
	owner := ownerFor("alice")
	state := diskState{Version: 1, Accounts: map[string]diskConfig{
		owner: {BaseURL: "https://example.com", Model: "model", Revision: maxRevision - 1},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "ai-settings.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	s := mustStore(t, dir)
	c, err := s.Save(owner, Update{BaseURL: "https://example.com", Model: "updated", Revision: maxRevision - 1}, false)
	if err != nil || c.Revision != maxRevision {
		t.Fatalf("last safe revision: %+v %v", c, err)
	}
	if _, err := s.Save(owner, Update{BaseURL: c.BaseURL, Model: c.Model, Revision: maxRevision}, false); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revision overflow save: %v", err)
	}
	if err := s.Delete(owner, maxRevision); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revision overflow delete: %v", err)
	}
	if _, err := s.Save(owner, Update{BaseURL: c.BaseURL, Model: c.Model, Revision: maxRevision + 1}, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe request revision: %v", err)
	}
	if err := s.Delete(owner, maxRevision+1); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe delete revision: %v", err)
	}
	if _, err := s.Complete(context.Background(), owner, maxRevision+1, "data"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe analysis revision: %v", err)
	}
	if got := mustStore(t, dir).Get(owner); got != c {
		t.Fatalf("safe boundary did not survive restart: %+v", got)
	}
	state.Accounts[owner] = diskConfig{Revision: maxRevision + 1}
	data, _ = json.Marshal(state)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unsafe disk revision accepted: %v", err)
	}
}

func TestPostRenameSyncFailureRequiresReopen(t *testing.T) {
	for _, operation := range []string{"save", "delete"} {
		t.Run(operation, func(t *testing.T) {
			dir := t.TempDir()
			s := mustStore(t, dir)
			owner := ownerFor("alice")
			initial := mustSave(t, s, owner, "https://example.com", false)
			oldConfig := s.configs[owner]
			calls := 0
			s.syncDir = func(string) error {
				calls++
				return errors.New("simulated directory synchronization failure")
			}
			var err error
			if operation == "save" {
				_, err = s.Save(owner, Update{BaseURL: initial.BaseURL, Model: "changed", Revision: initial.Revision}, false)
			} else {
				err = s.Delete(owner, initial.Revision)
			}
			if !errors.Is(err, ErrUnavailable) || calls != 1 || s.Available() || s.Get(owner) != (PublicConfig{}) {
				t.Fatalf("uncertain write did not mark store unavailable: %v", err)
			}
			if s.configs[owner] != oldConfig {
				t.Fatal("failed save changed in-memory config")
			}
			if _, err := s.Save(owner, Update{BaseURL: initial.BaseURL, Model: "stale retry", Revision: initial.Revision}, false); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("uncertain store accepted stale save: %v", err)
			}
			if err := s.Delete(owner, initial.Revision); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("uncertain store accepted delete: %v", err)
			}
			if _, err := s.Complete(context.Background(), owner, initial.Revision, "data"); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("uncertain store used old credentials: %v", err)
			}
			if calls != 1 {
				t.Fatal("uncertain store attempted additional disk writes")
			}
			reopened := mustStore(t, dir)
			actual := reopened.Get(owner)
			if !reopened.Available() || actual.Revision != initial.Revision+1 || (operation == "save" && actual.Model != "changed") || (operation == "delete" && actual.Model != "") {
				t.Fatalf("reopen did not recover committed state: %+v", actual)
			}
			if _, err := reopened.Save(owner, Update{BaseURL: initial.BaseURL, Model: "stale retry", Revision: initial.Revision}, false); !errors.Is(err, ErrConflict) {
				t.Fatalf("reopened store accepted pre-commit revision: %v", err)
			}
		})
	}
}
