package hub

import (
	"net/http"
	"testing"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func TestAuthorizedNodeBindingSurvivesRestart(t *testing.T) {
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	opts := Options{Keys: []string{"owner", "other"}}
	nodes, err := Open(db, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	keyHash, ok := nodes.authorizeKey("owner")
	if !ok {
		t.Fatal("owner credential rejected")
	}
	for _, id := range []string{"first", "second", "unbound"} {
		node := nodes.newNode(id, registry.Host{ID: id, Hostname: id, UpdateEvery: 1})
		if id != "unbound" {
			node.keyHash = keyHash
		}
		nodes.nodes[id] = node
	}
	nodes.touch()
	if err := nodes.Save(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(db, dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, current := range []*Nodes{nodes, reopened} {
		for _, tc := range []struct {
			key, node string
			want      bool
		}{
			{"owner", "first", true}, {"owner", "second", true},
			{"other", "first", false}, {"invalid", "first", false},
			{"owner", "unbound", false}, {"owner", "unknown", false},
		} {
			req, _ := http.NewRequest(http.MethodGet, "http://hub/api/v1/agent/config", nil)
			req.Header.Set("Authorization", "Bearer "+tc.key)
			if got := current.AuthorizedNode(req, tc.node); got != tc.want {
				t.Fatalf("key=%q node=%q authorized=%v, want %v", tc.key, tc.node, got, tc.want)
			}
		}
	}
	// A stored binding alone cannot authorize a credential removed from config.
	revoked, err := Open(db, dir, Options{Keys: []string{"other"}})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "http://hub/api/v1/agent/config", nil)
	req.Header.Set("Authorization", "Bearer owner")
	if revoked.AuthorizedNode(req, "first") {
		t.Fatal("revoked key retained config access")
	}
}
