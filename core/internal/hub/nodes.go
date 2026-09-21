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
	Logger    *slog.Logger
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
}

// Nodes is the hub-side node set.
type Nodes struct {
	db   *tsdb.Store
	path string
	opt  Options
	log  *slog.Logger

	mu    sync.RWMutex
	nodes map[string]*Node
	dirty bool
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
	Charts    []*stream.ChartDef    `json:"charts"`
	Functions []stream.FunctionInfo `json:"functions,omitempty"`
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
		node.Version, node.FirstSeen, node.LastSeen = pn.Version, pn.FirstSeen, pn.LastSeen
		node.functions = pn.Functions
		for _, cd := range pn.Charts {
			node.reg.AddChart(cd.ToChart())
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

// Save writes nodes.json if anything changed.
func (n *Nodes) Save() error {
	n.mu.Lock()
	if !n.dirty {
		n.mu.Unlock()
		return nil
	}
	n.dirty = false
	var p persisted
	for _, node := range n.nodes {
		node.mu.Lock()
		e := persistedNode{ID: node.ID, Host: node.Host, Version: node.Version, FirstSeen: node.FirstSeen, LastSeen: node.LastSeen, Functions: node.functions}
		node.mu.Unlock()
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
	return os.Rename(tmp, n.path)
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
	n.dirty = true
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
		LastSeen: nd.LastSeen, LastData: nd.lastData, ChartsCount: len(nd.reg.Charts()), Alarms: map[string]int{"warning": 0, "critical": 0}}
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
