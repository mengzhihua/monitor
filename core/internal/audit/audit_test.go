package audit

import (
	"fmt"
	"testing"
)

func TestAppendQueryOrder(t *testing.T) {
	l, err := Open(t.TempDir(), 100)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	for i := int64(1); i <= 5; i++ {
		l.Append(Entry{TS: i, User: "alice", Action: fmt.Sprintf("POST /x/%d", i), Status: 200})
	}
	out := l.Query(0, 0, "")
	if len(out) != 5 || out[0].TS != 5 || out[4].TS != 1 {
		t.Fatalf("newest-first order broken: %+v", out)
	}
	out = l.Query(2, 0, "")
	if len(out) != 3 || out[0].TS != 5 {
		t.Fatalf("after filter: %+v", out)
	}
	out = l.Query(0, 2, "")
	if len(out) != 2 || out[1].TS != 4 {
		t.Fatalf("limit: %+v", out)
	}
	l.Append(Entry{TS: 9, User: "bob", Action: "login"})
	out = l.Query(0, 0, "bob")
	if len(out) != 1 || out[0].User != "bob" {
		t.Fatalf("user filter: %+v", out)
	}
}

func TestBoundedRewriteAndReopen(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(1); i <= 25; i++ {
		l.Append(Entry{TS: i, User: "u", Action: "POST /x", Status: 200})
	}
	l.Close()
	l2, err := Open(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	out := l2.Query(0, 0, "")
	if len(out) != 10 || out[0].TS != 25 || out[9].TS != 16 {
		t.Fatalf("bounded reopen: %d entries, %+v", len(out), out)
	}
}

func TestNilSafe(t *testing.T) {
	var l *Log
	l.Append(Entry{TS: 1, Action: "x"})
	if l.Query(0, 0, "") != nil {
		t.Fatal("nil log should return nil")
	}
	l.Close()
}
