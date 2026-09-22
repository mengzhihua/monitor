// Package hub keeps the set of remote nodes a hub knows about: each streams
// into its own registry backed by a namespaced view of the shared TSDB, and
// mirrors the agent's alarm transitions. Node metadata and chart definitions
// are persisted so offline nodes stay browsable across hub restarts.
package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

const (
	StatusLive    = "live"
	StatusStale   = "stale" // connected but no samples recently
	StatusOffline = "offline"

	alarmLogKeep = 1000
)

// Options configures the node set.
type Options struct {
	// Keys agents must present (Authorization: Bearer / ?api_key=). Empty
	// disables ingestion.
	Keys []string
	// Replicate bounds how far back agents may backfill on connect.
	Replicate time.Duration
	// Quotas guard hub memory/disk against a misbehaving agent; zero means
	// the defaults (1000 nodes, 5000 charts per node, 1000 dimensions per chart).
	MaxNodes         int
	MaxChartsPerNode int
	MaxDimsPerChart  int
	Logger           *slog.Logger
	// ExtraKeys are stream credentials minted at runtime (claim tokens).
	ExtraKeys func() []string
	// OnSample/OnAlarm mirror node activity to the live WebSocket feed.
	OnSample func(nodeID, chartID string, ts int64, values map[string]float64)
	OnAlarm  func(nodeID string, e health.LogEntry)
	// Now is overridable for tests.
	Now func() time.Time
}

// Node is one remote agent.
type Node struct {
	ID        string
	Host      registry.Host
	Version   string
	FirstSeen int64
	LastSeen  int64
	// keyHash pins the node to the API key it first connected with, so one
	// agent cannot take over another's node id (give each agent its own key).
	keyHash string

	reg *registry.Registry
	db  *tsdb.View

	mu        sync.Mutex
	functions []stream.FunctionInfo
	conn      *session
	lastData  int64
	alarms    map[string]health.LogEntry // chart.name → latest transition
	alog      []health.LogEntry
	nextCall  uint64
	calls     map[uint64]chan stream.Frame
	replica   bool
	spaceID   string
	roomID    string
}

// Info is the JSON view of a node.
type Info struct {
	ID          string            `json:"id"`
	Hostname    string            `json:"hostname"`
	OS          string            `json:"os"`
	Arch        string            `json:"arch"`
	Labels      map[string]string `json:"labels"`
	UpdateEvery int               `json:"update_every"`
	Version     string            `json:"version"`
	Status      string            `json:"status"`
	Local       bool              `json:"local"`
	FirstSeen   int64             `json:"first_seen"`
	LastSeen    int64             `json:"last_seen"`
	LastData    int64             `json:"last_data,omitempty"`
	ChartsCount int               `json:"charts_count"`
	Alarms      map[string]int    `json:"alarms"`
	Functions   []string          `json:"functions,omitempty"`
	Peer        string            `json:"peer,omitempty"` // cluster: originating hub URL
	SpaceID     string            `json:"space_id,omitempty"`
	RoomID      string            `json:"room_id,omitempty"`
	Replica     bool              `json:"replica,omitempty"`
}

// Nodes is the hub-side node set.
type Nodes struct {
	db   *tsdb.Store
	path string
	opt  Options
	log  *slog.Logger

	mu    sync.RWMutex
	nodes map[string]*Node
	// gen counts changes; saved is the gen last written successfully, so a
	// failed Save keeps the state dirty and the next tick retries.
	gen, saved uint64
	saveMu     sync.Mutex // one Save at a time: they share the tmp file
}

type persisted struct {
	Nodes []persistedNode `json:"nodes"`
}

type persistedNode struct {
	ID        string                `json:"id"`
	Host      registry.Host         `json:"host"`
	Version   string                `json:"version"`
	FirstSeen int64                 `json:"first_seen"`
	LastSeen  int64                 `json:"last_seen"`
	KeyHash   string                `json:"key_hash,omitempty"`
	Charts    []*stream.ChartDef    `json:"charts"`
	Functions []stream.FunctionInfo `json:"functions,omitempty"`
	Alarms    []health.LogEntry     `json:"alarms,omitempty"`
}

// Open loads nodes.json from dir (if present).
func Open(db *tsdb.Store, dir string, opt Options) (*Nodes, error) {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Replicate <= 0 {
		opt.Replicate = 24 * time.Hour
	}
	if opt.MaxNodes <= 0 {
		opt.MaxNodes = 1000
	}
	if opt.MaxChartsPerNode <= 0 {
		opt.MaxChartsPerNode = 5000
	}
	if opt.MaxDimsPerChart <= 0 {
		opt.MaxDimsPerChart = 1000
	}
	n := &Nodes{db: db, path: filepath.Join(dir, "nodes.json"), opt: opt, log: opt.Logger, nodes: map[string]*Node{}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(n.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return n, nil
		}
		return nil, err
	}
	var p persisted
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("%s: %w", n.path, err)
	}
	for _, pn := range p.Nodes {
		node := n.newNode(pn.ID, pn.Host)
		node.Version, node.FirstSeen, node.LastSeen, node.keyHash = pn.Version, pn.FirstSeen, pn.LastSeen, pn.KeyHash
		node.functions = pn.Functions
		for _, cd := range pn.Charts {
			node.reg.AddChart(cd.ToChart())
		}
		for _, a := range pn.Alarms {
			node.alarms[a.Chart+"."+a.Name] = a
		}
		n.nodes[node.ID] = node
	}
	n.log.Info("hub: nodes loaded", "count", len(n.nodes))
	return n, nil
}

func (n *Nodes) newNode(id string, host registry.Host) *Node {
	h := host
	view := tsdb.Prefixed(n.db, "node:"+id+"|")
	node := &Node{ID: id, Host: h, db: view, alarms: map[string]health.LogEntry{}, calls: map[uint64]chan stream.Frame{}}
	node.reg = registry.New(&node.Host, view)
	if n.opt.OnSample != nil {
		fn := n.opt.OnSample
		node.reg.Subscribe(func(chartID string, ts int64, values map[string]float64) { fn(id, chartID, ts, values) })
	}
	return node
}

// touch marks the node set changed.
func (n *Nodes) touch() {
	n.mu.Lock()
	n.gen++
	n.mu.Unlock()
}

// Save writes nodes.json if anything changed since the last successful save.
func (n *Nodes) Save() error {
	n.saveMu.Lock()
	defer n.saveMu.Unlock()
	n.mu.Lock()
	gen := n.gen
	if gen == n.saved {
		n.mu.Unlock()
		return nil
	}
	var p persisted
	for _, node := range n.nodes {
		node.mu.Lock()
		e := persistedNode{ID: node.ID, Host: node.Host, Version: node.Version, FirstSeen: node.FirstSeen, LastSeen: node.LastSeen, KeyHash: node.keyHash, Functions: node.functions}
		for _, a := range node.alarms {
			e.Alarms = append(e.Alarms, a)
		}
		node.mu.Unlock()
		sort.Slice(e.Alarms, func(i, j int) bool {
			return e.Alarms[i].Chart+"."+e.Alarms[i].Name < e.Alarms[j].Chart+"."+e.Alarms[j].Name
		})
		for _, c := range node.reg.Charts() {
			e.Charts = append(e.Charts, stream.DefOf(c))
		}
		p.Nodes = append(p.Nodes, e)
	}
	n.mu.Unlock()
	sort.Slice(p.Nodes, func(i, j int) bool { return p.Nodes[i].ID < p.Nodes[j].ID })
	b, err := json.MarshalIndent(p, "", " ")
	if err != nil {
		return err
	}
	tmp := n.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, n.path); err != nil {
		return err
	}
	n.mu.Lock()
	if gen > n.saved {
		n.saved = gen
	}
	n.mu.Unlock()
	return nil
}

// Run persists periodically until ctx is done, then once more.
func (n *Nodes) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			n.disconnectAll()
			if err := n.Save(); err != nil {
				n.log.Error("hub: save nodes", "err", err)
			}
			return
		case <-t.C:
			if err := n.Save(); err != nil {
				n.log.Error("hub: save nodes", "err", err)
			}
		}
	}
}

func (n *Nodes) disconnectAll() {
	n.mu.RLock()
	defer n.mu.RUnlock()
	for _, node := range n.nodes {
		node.mu.Lock()
		s := node.conn
		node.mu.Unlock()
		if s != nil {
			s.close(errors.New("hub shutting down"))
		}
	}
}

// SetMembership records the node's Space/Room (dashboard / nodes API).
func (nd *Node) SetMembership(spaceID, roomID string) {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	nd.spaceID, nd.roomID = spaceID, roomID
}

// AcceptReplica installs (or refreshes) a ring-replicated copy of a node that
// lives on a peer hub. A live local connection always wins.
func (n *Nodes) AcceptReplica(host registry.Host, charts []*registry.Chart, samples []ReplicaSample, now time.Time) (*Node, error) {
	id := nodeID(&host)
	n.mu.Lock()
	node, ok := n.nodes[id]
	if ok {
		node.mu.Lock()
		live := node.conn != nil
		node.mu.Unlock()
		if live {
			n.mu.Unlock()
			return node, nil
		}
	} else {
		if len(n.nodes) >= n.opt.MaxNodes && n.opt.MaxNodes > 0 {
			n.mu.Unlock()
			return nil, errTooManyNodes
		}
		node = n.newNode(id, host)
		node.FirstSeen = now.Unix()
		node.replica = true
		n.nodes[id] = node
		n.gen++
	}
	n.mu.Unlock()
	node.mu.Lock()
	node.Host = host
	node.LastSeen = now.Unix()
	node.replica = true
	node.mu.Unlock()
	for _, c := range charts {
		if c == nil || c.ID == "" {
			continue
		}
		if _, exists := node.reg.Chart(c.ID); !exists {
			node.reg.AddChart(stream.DefOf(c).ToChart())
		}
	}
	for _, s := range samples {
		if s.Chart == "" {
			continue
		}
		_ = node.reg.Collect(s.Chart, time.Unix(s.T, 0), s.V)
		node.mu.Lock()
		node.lastData = s.T
		node.mu.Unlock()
	}
	return node, nil
}

// ReplicaSample is one chart collection copied over the hub ring.
type ReplicaSample struct {
	Chart string             `json:"chart"`
	T     int64              `json:"t"`
	V     map[string]float64 `json:"v"`
}

// SnapshotRing is the payload this hub would send to a peer for node.
func (nd *Node) SnapshotRing() (registry.Host, []*registry.Chart, []ReplicaSample) {
	nd.mu.Lock()
	replica := nd.replica
	host := nd.Host
	nd.mu.Unlock()
	if replica {
		return host, nil, nil
	}
	var charts []*registry.Chart
	var samples []ReplicaSample
	for _, c := range nd.reg.Charts() {
		charts = append(charts, c)
		ts, vals := c.LastValues()
		if len(vals) == 0 {
			continue
		}
		samples = append(samples, ReplicaSample{Chart: c.ID, T: ts, V: vals})
	}
	return host, charts, samples
}

// Get returns a node by ID.
func (n *Nodes) Get(id string) (*Node, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	node, ok := n.nodes[id]
	return node, ok
}

// List returns nodes sorted by hostname.
func (n *Nodes) List() []*Node {
	n.mu.RLock()
	out := make([]*Node, 0, len(n.nodes))
	for _, node := range n.nodes {
		out = append(out, node)
	}
	n.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host.Hostname != out[j].Host.Hostname {
			return out[i].Host.Hostname < out[j].Host.Hostname
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Forget removes an offline node's metadata (its samples age out with
// retention). Connected nodes cannot be forgotten.
func (n *Nodes) Forget(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	node, ok := n.nodes[id]
	if !ok {
		return errors.New("unknown node")
	}
	node.mu.Lock()
	connected := node.conn != nil
	node.mu.Unlock()
	if connected {
		return errors.New("node is connected")
	}
	delete(n.nodes, id)
	n.gen++
	return nil
}

// Registry returns the node's chart registry.
func (nd *Node) Registry() *registry.Registry { return nd.reg }

// DB returns the node's namespaced TSDB view.
func (nd *Node) DB() tsdb.Reader { return nd.db }

// Status classifies the node's connection.
func (nd *Node) Status(now time.Time) string {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return nd.statusLocked(now)
}

func (nd *Node) statusLocked(now time.Time) string {
	if nd.conn == nil {
		if nd.replica && nd.lastData > 0 {
			grace := int64(30)
			if now.Unix()-nd.lastData <= grace {
				return StatusLive
			}
			return StatusStale
		}
		return StatusOffline
	}
	grace := int64(3 * nd.Host.UpdateEvery)
	if grace < 10 {
		grace = 10
	}
	if nd.lastData == 0 || now.Unix()-nd.lastData > grace {
		return StatusStale
	}
	return StatusLive
}

// Info renders the node for the API.
func (nd *Node) Info(now time.Time) Info {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	inf := Info{ID: nd.ID, Hostname: nd.Host.Hostname, OS: nd.Host.OS, Arch: nd.Host.Arch, Labels: nd.Host.Labels,
		UpdateEvery: nd.Host.UpdateEvery, Version: nd.Version, Status: nd.statusLocked(now), FirstSeen: nd.FirstSeen,
		LastSeen: nd.LastSeen, LastData: nd.lastData, ChartsCount: len(nd.reg.Charts()), Alarms: map[string]int{"warning": 0, "critical": 0},
		SpaceID: nd.spaceID, RoomID: nd.roomID, Replica: nd.replica}
	for _, a := range nd.alarms {
		switch a.Status {
		case health.StatusWarning:
			inf.Alarms["warning"]++
		case health.StatusCritical:
			inf.Alarms["critical"]++
		}
	}
	for _, f := range nd.functions {
		inf.Functions = append(inf.Functions, f.Name)
	}
	return inf
}

// Functions lists what the agent advertised.
func (nd *Node) Functions() []stream.FunctionInfo {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	return append([]stream.FunctionInfo(nil), nd.functions...)
}

// Alarms returns the latest known transition per alarm.
func (nd *Node) Alarms() []health.LogEntry {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	out := make([]health.LogEntry, 0, len(nd.alarms))
	for _, a := range nd.alarms {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Chart+"."+out[i].Name < out[j].Chart+"."+out[j].Name })
	return out
}

// AlarmLog returns mirrored transitions with UniqueID > after.
func (nd *Node) AlarmLog(after uint64) []health.LogEntry {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	var out []health.LogEntry
	for _, e := range nd.alog {
		if e.UniqueID > after {
			out = append(out, e)
		}
	}
	return out
}

func (nd *Node) recordAlarm(e health.LogEntry) {
	nd.mu.Lock()
	nd.alarms[e.Chart+"."+e.Name] = e
	nd.alog = append(nd.alog, e)
	if len(nd.alog) > alarmLogKeep {
		nd.alog = nd.alog[len(nd.alog)-alarmLogKeep:]
	}
	nd.mu.Unlock()
}

// replaceAlarms installs a full snapshot of the agent's alarm state and
// returns the entries whose status differs from what the hub held (they are
// appended to the mirrored log so the gap shows up as transitions).
func (nd *Node) replaceAlarms(snapshot []health.LogEntry) []health.LogEntry {
	nd.mu.Lock()
	defer nd.mu.Unlock()
	var changed []health.LogEntry
	var seq uint64 // snapshot entries carry no log id; continue the mirrored sequence
	for _, e := range nd.alog {
		if e.UniqueID > seq {
			seq = e.UniqueID
		}
	}
	fresh := make(map[string]health.LogEntry, len(snapshot))
	for _, e := range snapshot {
		k := e.Chart + "." + e.Name
		fresh[k] = e
		if old, ok := nd.alarms[k]; !ok || old.Status != e.Status {
			if ok {
				e.OldStatus, e.OldValue = old.Status, old.Value
			}
			seq++
			e.UniqueID = seq
			fresh[k] = e
			changed = append(changed, e)
		}
	}
	nd.alarms = fresh
	if len(changed) > 0 {
		nd.alog = append(nd.alog, changed...)
		if len(nd.alog) > alarmLogKeep {
			nd.alog = nd.alog[len(nd.alog)-alarmLogKeep:]
		}
	}
	return changed
}

// Call runs a function on the agent over the stream and returns its raw JSON
// result.
func (nd *Node) Call(ctx context.Context, name string, args map[string]string) (json.RawMessage, error) {
	nd.mu.Lock()
	s := nd.conn
	if s == nil {
		nd.mu.Unlock()
		return nil, errors.New("node is offline")
	}
	nd.nextCall++
	id := nd.nextCall
	ch := make(chan stream.Frame, 1)
	nd.calls[id] = ch
	nd.mu.Unlock()
	defer func() {
		nd.mu.Lock()
		delete(nd.calls, id)
		nd.mu.Unlock()
	}()
	if err := s.send(stream.Frame{Type: stream.TypeFuncCall, CallID: id, Name: name, Args: args}); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case res := <-ch:
		if res.Error != "" {
			return nil, errors.New(res.Error)
		}
		return res.Result, nil
	}
}

func (nd *Node) deliverResult(f stream.Frame) {
	nd.mu.Lock()
	ch := nd.calls[f.CallID]
	nd.mu.Unlock()
	if ch != nil {
		ch <- f
	}
}
