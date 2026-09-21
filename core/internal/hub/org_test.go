package hub

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestOrgClaimSpaceRoomConfig(t *testing.T) {
	dir := t.TempDir()
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := o.CreateSpace("prod")
	if err != nil || sp.ID == "" {
		t.Fatalf("space: %+v %v", sp, err)
	}
	rm, err := o.CreateRoom(sp.ID, "edge")
	if err != nil {
		t.Fatal(err)
	}
	cl, err := o.IssueClaim(sp.ID, rm.ID, time.Hour)
	if err != nil || cl.Token == "" {
		t.Fatalf("claim: %+v %v", cl, err)
	}
	got, err := o.RedeemClaim(cl.Token, "agent-1")
	if err != nil || got.APIKey == "" || got.NodeID != "agent-1" {
		t.Fatalf("redeem: %+v %v", got, err)
	}
	if _, err := o.RedeemClaim(cl.Token, "agent-1"); err == nil {
		t.Fatal("token should be single-use")
	}
	keys := o.Keys()
	if len(keys) != 1 || keys[0] != got.APIKey {
		t.Fatalf("keys = %v", keys)
	}
	sid, rid := o.Membership("agent-1")
	if sid != sp.ID || rid != rm.ID {
		t.Fatalf("membership %s %s", sid, rid)
	}
	cfg, err := o.SetConfig(NodeConfig{NodeID: "agent-1", Disabled: []string{"nvidia"}})
	if err != nil || cfg.Disabled[0] != "nvidia" {
		t.Fatalf("config: %+v %v", cfg, err)
	}
	byKey, ok := o.ConfigByKey(got.APIKey)
	if !ok || byKey.Disabled[0] != "nvidia" {
		t.Fatalf("config by key: %+v %v", byKey, ok)
	}

	o2, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(o2.Spaces()) != 1 || len(o2.Rooms("")) != 1 || len(o2.Keys()) != 1 {
		t.Fatalf("reload spaces=%d rooms=%d keys=%d", len(o2.Spaces()), len(o2.Rooms("")), len(o2.Keys()))
	}
	if _, err := os.Stat(filepath.Join(dir, "org.json")); err != nil {
		t.Fatal(err)
	}
}
