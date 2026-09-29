package config

import "testing"

func TestEffectiveRoomsFollowsNestedGroups(t *testing.T) {
	groups := []Group{
		{Name: "ops", Rooms: []string{"edge"}, Groups: []string{"net"}},
		{Name: "net", Rooms: []string{"core"}, Groups: []string{"ops"}},
	}
	got := EffectiveRooms(User{Groups: []string{"ops"}, Rooms: []string{"lab"}}, groups)
	want := map[string]bool{"lab": true, "edge": true, "core": true}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for _, room := range got {
		if !want[room] {
			t.Fatalf("%v", got)
		}
	}
	if again := EffectiveRooms(User{}, nil); again != nil {
		t.Fatalf("unrestricted user got %v", again)
	}
}
