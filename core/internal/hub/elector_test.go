package hub

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestElectorMajorityAndFailover(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1_000, 0)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		now = now.Add(d)
		mu.Unlock()
	}
	ids := []string{"a", "b", "c"}
	nodes := map[string]*Elector{}
	for _, id := range ids {
		e := NewElector(id, nil, "")
		e.now = clock
		e.leaseFor = time.Second
		e.peers = []string{}
		for _, other := range ids {
			if other != id {
				e.peers = append(e.peers, other)
			}
		}
		nodes[id] = e
	}
	for id, e := range nodes {
		e.ask = func(peer string, ballot Ballot) (BallotResult, error) {
			return nodes[peer].Accept(ballot), nil
		}
		_ = id
	}
	var leader *Elector
	for _, e := range nodes {
		if st := e.Tick(); st.Active {
			if leader != nil {
				t.Fatalf("two leaders: %s and %s", leader.id, e.id)
			}
			leader = e
		}
	}
	if leader == nil {
		t.Fatal("no leader after the first round")
	}
	leader.Tick()
	for _, e := range nodes {
		if e.Status().Leader != leader.id {
			t.Fatalf("peer follows %q, want %q", e.Status().Leader, leader.id)
		}
	}
	isolated := leader.id
	for _, e := range nodes {
		e.ask = func(peer string, ballot Ballot) (BallotResult, error) {
			if peer == isolated {
				return BallotResult{}, errors.New("down")
			}
			return nodes[peer].Accept(ballot), nil
		}
	}
	var next *Elector
	for i := 0; i < 4 && next == nil; i++ {
		advance(time.Second)
		for _, e := range nodes {
			if e.id == isolated {
				continue
			}
			if e.Tick(); e.IsLeader() {
				next = e
			}
		}
	}
	if next == nil {
		t.Fatal("cluster did not elect a replacement")
	}
	if nodes[isolated].IsLeader() {
		t.Fatal("isolated hub still believes it leads")
	}
}

func TestSingleHubStaysLeader(t *testing.T) {
	e := NewElector("solo", nil, "")
	if !e.IsLeader() || e.Status().Mode != "single" {
		t.Fatalf("%+v", e.Status())
	}
}
