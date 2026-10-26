package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Ballot is one leader-election message between hubs.
type Ballot struct {
	Kind      string `json:"kind"` // vote or heartbeat
	Term      int    `json:"term"`
	Candidate string `json:"candidate,omitempty"`
	Leader    string `json:"leader,omitempty"`
}

// BallotResult is the peer's answer. Granted is a vote or an accepted heartbeat.
type BallotResult struct {
	Term    int  `json:"term"`
	Granted bool `json:"granted"`
}

// LeaderStatus is the local view of the elected hub.
type LeaderStatus struct {
	ID     string `json:"id"`
	Leader string `json:"leader,omitempty"`
	Term   int    `json:"term"`
	Active bool   `json:"active"`
	Lease  int64  `json:"lease,omitempty"`
	Mode   string `json:"mode"`
}

// Elector picks one leader among this hub and its peers. A peer that cannot
// see a majority does not become leader, so a partition does not double-alert.
// Metrics still replicate through the ring; this only chooses the active hub.
type Elector struct {
	id       string
	peers    []string
	token    string
	leaseFor time.Duration
	now      func() time.Time
	ask      func(peer string, ballot Ballot) (BallotResult, error)
	client   *http.Client

	mu       sync.Mutex
	term     int
	votedFor string
	leader   string
	lease    time.Time
	readyAt  time.Time
}

func NewElector(id string, peers []string, token string) *Elector {
	if id == "" {
		id = "hub"
	}
	e := &Elector{
		id: id, peers: append([]string(nil), peers...), token: token,
		leaseFor: 3 * time.Second, now: time.Now,
		client: &http.Client{Timeout: 3 * time.Second},
	}
	e.ask = e.askHTTP
	if len(e.peers) == 0 {
		e.leader = id
		e.lease = time.Unix(1<<62, 0)
	}
	return e
}

func (e *Elector) IsLeader() bool {
	if e == nil {
		return true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.leader == e.id && !e.now().After(e.lease)
}

func (e *Elector) Status() LeaderStatus {
	if e == nil {
		return LeaderStatus{Mode: "single", Active: true}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	st := LeaderStatus{ID: e.id, Leader: e.leader, Term: e.term, Mode: "cluster", Active: e.leader == e.id && !e.now().After(e.lease)}
	if len(e.peers) == 0 {
		st.Mode = "single"
		st.Active = true
	}
	if !e.lease.IsZero() && e.lease.Unix() < 1<<61 {
		st.Lease = e.lease.Unix()
	}
	return st
}

// Tick heartbeats while this hub leads, or starts an election once its backoff elapses.
func (e *Elector) Tick() LeaderStatus {
	if e == nil || len(e.peers) == 0 {
		return e.Status()
	}
	e.mu.Lock()
	leading := e.leader == e.id && !e.now().After(e.lease)
	expired := e.leader == "" || e.now().After(e.lease)
	if expired && e.readyAt.IsZero() {
		e.readyAt = e.now().Add(e.backoff())
	}
	ready := expired && !e.now().Before(e.readyAt)
	e.mu.Unlock()
	if leading {
		e.heartbeat()
	} else if ready {
		e.campaign()
	}
	return e.Status()
}

// Accept applies a vote request or a leader heartbeat from a peer.
func (e *Elector) Accept(b Ballot) BallotResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	if b.Term < e.term {
		return BallotResult{Term: e.term}
	}
	if b.Term > e.term {
		e.term = b.Term
		e.votedFor = ""
		e.leader = ""
		e.lease = time.Time{}
		e.readyAt = time.Time{}
	}
	switch b.Kind {
	case "heartbeat":
		if b.Leader == "" {
			return BallotResult{Term: e.term}
		}
		e.leader = b.Leader
		e.lease = e.now().Add(e.leaseFor)
		e.readyAt = time.Time{}
		return BallotResult{Term: e.term, Granted: true}
	case "vote":
		if e.leader != "" && e.leader != b.Candidate && !e.now().After(e.lease) {
			return BallotResult{Term: e.term}
		}
		if e.votedFor == "" || e.votedFor == b.Candidate {
			e.votedFor = b.Candidate
			return BallotResult{Term: e.term, Granted: true}
		}
		return BallotResult{Term: e.term}
	default:
		return BallotResult{Term: e.term}
	}
}

func (e *Elector) campaign() {
	e.mu.Lock()
	e.term++
	e.votedFor = e.id
	e.leader = ""
	e.lease = time.Time{}
	term := e.term
	peers := append([]string(nil), e.peers...)
	e.mu.Unlock()
	votes := 1
	for _, peer := range peers {
		res, err := e.ask(peer, Ballot{Kind: "vote", Term: term, Candidate: e.id})
		if res.Term > term {
			e.observeHigher(res.Term)
			return
		}
		if err == nil && res.Granted {
			votes++
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term != term {
		return
	}
	if votes >= e.majority() {
		e.leader = e.id
		e.lease = e.now().Add(e.leaseFor)
		e.readyAt = time.Time{}
		return
	}
	e.readyAt = e.now().Add(e.backoff())
}

func (e *Elector) heartbeat() {
	e.mu.Lock()
	term := e.term
	peers := append([]string(nil), e.peers...)
	e.mu.Unlock()
	acks := 1
	for _, peer := range peers {
		res, err := e.ask(peer, Ballot{Kind: "heartbeat", Term: term, Leader: e.id})
		if res.Term > term {
			e.observeHigher(res.Term)
			return
		}
		if err == nil && res.Granted {
			acks++
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.term != term || e.leader != e.id {
		return
	}
	if acks >= e.majority() {
		e.lease = e.now().Add(e.leaseFor)
		return
	}
	e.leader = ""
	e.lease = time.Time{}
	e.readyAt = e.now().Add(e.backoff())
}

func (e *Elector) observeHigher(term int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if term > e.term {
		e.term = term
		e.votedFor = ""
		e.leader = ""
		e.lease = time.Time{}
		e.readyAt = e.now().Add(e.backoff())
	}
}

func (e *Elector) majority() int {
	return (len(e.peers)+1)/2 + 1
}

func (e *Elector) backoff() time.Duration {
	h := 0
	for _, c := range e.id {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return time.Duration(h%3) * e.leaseFor
}

func (e *Elector) askHTTP(peer string, ballot Ballot) (BallotResult, error) {
	body, err := json.Marshal(ballot)
	if err != nil {
		return BallotResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(peer, "/")+"/api/v1/hub/vote", bytes.NewReader(body))
	if err != nil {
		return BallotResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return BallotResult{}, err
	}
	defer resp.Body.Close()
	var out BallotResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&out); err != nil && err != io.EOF {
		return BallotResult{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("vote status %d", resp.StatusCode)
	}
	return out, nil
}
