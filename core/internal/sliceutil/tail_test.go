package sliceutil

import "testing"

func TestKeepTailReleasesTheDiscardedPrefix(t *testing.T) {
	s := make([]string, 0, 8)
	for i := range 8 {
		s = append(s, string(rune('a'+i)))
	}
	got := KeepTail(s, 3)
	if len(got) != 3 || cap(got) != 3 || got[0] != "f" || got[2] != "h" {
		t.Fatalf("tail=%v cap=%d", got, cap(got))
	}
	if same := KeepTail(got, 3); len(same) != 3 || &same[0] != &got[0] {
		t.Fatal("a slice already inside the limit was copied")
	}
	if empty := KeepTail(s, 0); len(empty) != 0 || cap(empty) != 0 {
		t.Fatalf("empty=%v cap=%d", empty, cap(empty))
	}
}
