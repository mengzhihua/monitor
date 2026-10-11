package hub

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func membershipOrg(t *testing.T) (*Org, Space, Room, Room) {
	t.Helper()
	o, err := OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	space, err := o.CreateSpace("production")
	if err != nil {
		t.Fatal(err)
	}
	a, err := o.CreateRoom(space.ID, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := o.CreateRoom(space.ID, "b")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := o.SetRoomNodes(a.ID, []string{"n1", "n2"}); err != nil {
		t.Fatal(err)
	}
	return o, space, a, b
}

func TestMembershipMoveDeduplicatesAndPersists(t *testing.T) {
	o, _, a, b := membershipOrg(t)
	out, err := o.SetRoomNodes(b.ID, []string{"n1", "n1", "", "n3"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out.Nodes, []string{"n1", "n3"}) {
		t.Fatalf("nodes = %v", out.Nodes)
	}
	if got := o.rooms[a.ID].Nodes; !reflect.DeepEqual(got, []string{"n2"}) {
		t.Fatalf("previous room = %v", got)
	}
	reopened, err := OpenOrg(filepath.Dir(o.path))
	if err != nil {
		t.Fatal(err)
	}
	for _, org := range []*Org{o, reopened} {
		_, room, conflict := org.MembershipStatus("n1")
		if room != b.ID || conflict {
			t.Fatalf("membership = %s conflict=%t", room, conflict)
		}
	}
	if err := o.DeleteRoom(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, room, conflict := o.MembershipStatus("n1"); room != "" || conflict {
		t.Fatal("deleted room retained membership")
	}
	if err := o.DeleteSpace(a.SpaceID); err != nil {
		t.Fatal(err)
	}
	if _, room, conflict := o.MembershipStatus("n2"); room != "" || conflict {
		t.Fatal("deleted space retained membership")
	}
}

func TestMembershipSaveFailurePreservesAuthorization(t *testing.T) {
	o, space, a, b := membershipOrg(t)
	claim, err := o.IssueClaim(space.ID, b.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	before := o.Rooms("")
	durable := o.path
	o.path = t.TempDir() // rename onto a directory fails on every supported OS
	for _, mutate := range []func() error{
		func() error { _, err := o.SetRoomNodes(b.ID, []string{"n1"}); return err },
		func() error { return o.DeleteRoom(a.ID) },
		func() error { return o.DeleteSpace(space.ID) },
		func() error { _, err := o.RedeemClaim(claim.Token, "n1"); return err },
	} {
		if err := mutate(); err == nil {
			t.Fatal("expected failed save")
		}
		if got := o.Rooms(""); !reflect.DeepEqual(got, before) {
			t.Fatalf("failed mutation changed rooms: %+v", got)
		}
		if _, room, conflict := o.MembershipStatus("n1"); room != a.ID || conflict {
			t.Fatal("failed save changed authorization index")
		}
	}
	if len(o.Keys()) != 0 || o.claims[claim.Token].UsedAt != 0 {
		t.Fatal("failed claim consumed credential")
	}
	o.path = durable
	if _, err := o.RedeemClaim(claim.Token, "n1"); err != nil {
		t.Fatal(err)
	}
	if _, room, conflict := o.MembershipStatus("n1"); room != b.ID || conflict {
		t.Fatal("redeem did not move membership")
	}
	if len(o.Keys()) != 1 {
		t.Fatal("redeem did not save credential")
	}
}

func TestLegacyMembershipConflictRequiresExplicitMove(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"spaces":[{"id":"s","name":"space"}],"rooms":[{"id":"a","space_id":"s","name":"a","nodes":["conflict","single","single"]},{"id":"b","space_id":"s","name":"b","nodes":["conflict"]}]}`
	if err := os.WriteFile(filepath.Join(dir, "org.json"), []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	o, err := OpenOrg(dir)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		space, room, conflict := o.MembershipStatus("conflict")
		if !conflict || room != "" || space != "" {
			t.Fatal("legacy duplicate selected an arbitrary room")
		}
	}
	if _, room, conflict := o.MembershipStatus("single"); room != "a" || conflict {
		t.Fatal("same-room duplicate treated as conflict")
	}
	if _, err := o.SetRoomNodes("b", []string{"conflict"}); err != nil {
		t.Fatal(err)
	}
	if _, room, conflict := o.MembershipStatus("conflict"); room != "b" || conflict {
		t.Fatal("explicit assignment did not repair conflict")
	}
}

func TestClaimCannotRedeemDeletedRoom(t *testing.T) {
	o, space, _, b := membershipOrg(t)
	claim, err := o.IssueClaim(space.ID, b.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := o.DeleteRoom(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := o.RedeemClaim(claim.Token, "new-node"); err == nil {
		t.Fatal("redeemed a claim into a deleted room")
	}
	if len(o.Keys()) != 0 || o.claims[claim.Token].UsedAt != 0 {
		t.Fatal("rejected claim minted a key")
	}
}

func BenchmarkOrgMembership(b *testing.B) {
	for _, count := range []int{100, 20000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			o := &Org{rooms: map[string]*Room{}}
			for i := 0; i < count; i++ {
				room := fmt.Sprint(i / 20)
				if o.rooms[room] == nil {
					o.rooms[room] = &Room{ID: room, SpaceID: "s"}
				}
				o.rooms[room].Nodes = append(o.rooms[room].Nodes, fmt.Sprint(i))
			}
			o.rebuildMembershipsLocked()
			id := fmt.Sprint(count - 1)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				o.MembershipStatus(id)
			}
		})
	}
}
