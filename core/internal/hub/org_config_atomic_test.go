package hub

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestConfigCASSerializesConcurrentWriters(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	o.now = func() time.Time { return time.Unix(1000, 0) }
	initial, err := o.SetConfig(NodeConfig{NodeID: "n1", YAML: "initial"})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.SetReport("n1", "reported", 1000); err != nil {
		t.Fatal(err)
	}
	if err := o.SetApply("n1", ApplyState{Rev: initial.Updated, State: "applied"}); err != nil {
		t.Fatal(err)
	}
	const writers = 16
	start := make(chan struct{})
	results := make(chan error, writers)
	var wg sync.WaitGroup
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := o.UpdateConfig("n1", &initial.Updated, func(cur NodeConfig) (NodeConfig, error) {
				cur.YAML = fmt.Sprintf("writer-%d", i)
				cur.Reported = "must not overwrite report"
				cur.Apply.State = "must not overwrite ack"
				return cur, nil
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, ErrConfigConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if succeeded != 1 || conflicts != writers-1 {
		t.Fatalf("succeeded=%d conflicts=%d", succeeded, conflicts)
	}
	got, _ := o.GetConfig("n1")
	if got.Updated != 1001 || got.Reported != "reported" || got.Apply.State != "applied" || got.Apply.Rev != 1000 {
		t.Fatalf("new revision must preserve report and old ack: %+v", got)
	}
	// A clock rollback and restart must not reuse a prior desired revision.
	reopened, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	reopened.now = func() time.Time { return time.Unix(900, 0) }
	next, err := reopened.SetConfig(NodeConfig{NodeID: "n1", YAML: "after restart"})
	if err != nil || next.Updated != 1002 {
		t.Fatalf("after rollback/restart: %+v err=%v", next, err)
	}
}

func TestConfigCASZeroAndFailedPersistence(t *testing.T) {
	o, err := OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	zero := int64(0)
	set := func(NodeConfig) (NodeConfig, error) { return NodeConfig{YAML: "first"}, nil }
	initial, err := o.UpdateConfig("n1", &zero, set)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.UpdateConfig("n1", &zero, set); !errors.Is(err, ErrConfigConflict) {
		t.Fatalf("stale initial revision = %v", err)
	}
	originalPath := o.path
	blocked := filepath.Join(t.TempDir(), "directory")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	o.path = blocked // rename onto a directory must fail, including when run as root
	if _, err := o.UpdateConfig("n1", &initial.Updated, func(NodeConfig) (NodeConfig, error) {
		return NodeConfig{YAML: "not persisted"}, nil
	}); err == nil {
		t.Fatal("expected failed persistence")
	}
	if got, _ := o.GetConfig("n1"); got.YAML != initial.YAML || got.Updated != initial.Updated {
		t.Fatalf("failed save changed live state: %+v", got)
	}
	if _, err := o.UpdateConfig("new", &zero, set); err == nil {
		t.Fatal("expected failed creation")
	}
	if _, ok := o.GetConfig("new"); ok {
		t.Fatal("failed creation leaked live config")
	}
	o.path = originalPath
	reopened, err := OpenOrg(filepath.Dir(originalPath))
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := reopened.GetConfig("n1"); got.YAML != initial.YAML || got.Updated != initial.Updated {
		t.Fatalf("failed save changed persisted state: %+v", got)
	}
}

func TestOrgFileKeepsCredentialsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "unrelated")
	if err := os.WriteFile(unrelated, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(unrelated, o.path+".tmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetConfig(NodeConfig{NodeID: "n1", YAML: "web:\n  token: test-secret\n"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(o.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("org credentials must be private: info=%v err=%v", info, err)
	}
	if got, err := os.ReadFile(unrelated); err != nil || string(got) != "keep" {
		t.Fatalf("followed stale tmp symlink: content=%q err=%v", got, err)
	}
	if err := os.Chmod(o.path, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := o.SetReport("n1", "reported secret", 1); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(o.path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("legacy permissions not tightened: info=%v err=%v", info, err)
	}
}

func TestOpenOrgSecuresLegacyPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "org.json")
			target := path
			if symlink {
				target = filepath.Join(t.TempDir(), "legacy.json")
			}
			original := []byte(`{"configs":{"n1":{"node_id":"n1","yaml":"secret","updated":7}},"keys":["legacy-key"]}`)
			if err := os.WriteFile(target, original, 0o644); err != nil {
				t.Fatal(err)
			}
			if symlink {
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			o, err := OpenOrg(dir)
			if err != nil {
				t.Fatal(err)
			}
			cfg, ok := o.GetConfig("n1")
			if !ok || cfg.YAML != "secret" || cfg.Updated != 7 || len(o.Keys()) != 1 {
				t.Fatalf("permission upgrade changed loaded data: %+v", cfg)
			}
			info, err := os.Lstat(path)
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
				t.Fatalf("read-only startup left credentials exposed: %v %v", info, err)
			}
			if got, err := os.ReadFile(path); err != nil || string(got) != string(original) {
				t.Fatalf("migration changed persisted contents: %q %v", got, err)
			}
			if symlink {
				info, err := os.Stat(target)
				if err != nil || info.Mode().Perm() != 0o644 {
					t.Fatalf("migration chmodded symlink target: %v %v", info, err)
				}
			}
		})
	}
}

func TestRedeemClaimRejectsDuplicateNode(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	space, err := o.CreateSpace("production")
	if err != nil {
		t.Fatal(err)
	}
	room, err := o.CreateRoom(space.ID, "nodes")
	if err != nil {
		t.Fatal(err)
	}
	first, err := o.IssueClaim(space.ID, room.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := o.RedeemClaim(first.Token, "victim")
	if err != nil {
		t.Fatal(err)
	}
	second, err := o.IssueClaim(space.ID, room.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.RedeemClaim(second.Token, "victim"); err == nil {
		t.Fatal("second claim minted another key for an existing claimed node")
	}
	keys := o.Keys()
	if len(keys) != 1 || keys[0] != owner.APIKey {
		t.Fatal("rejected duplicate changed accepted stream keys")
	}
	if got := o.claims[second.Token]; got.UsedAt != 0 || got.APIKey != "" || got.NodeID != "" {
		t.Fatal("rejected duplicate consumed claim")
	}
	reopened, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.RedeemClaim(second.Token, "victim"); err == nil {
		t.Fatal("duplicate node guard was lost after restart")
	}
	if _, err := reopened.RedeemClaim(second.Token, "new-node"); err != nil {
		t.Fatalf("rejected token should still work for a new node: %v", err)
	}
}
