package hub

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"
)

// Space is an organisation (Netdata Cloud Space).
type Space struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"`
}

// Room is a node collection inside a Space.
type Room struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	SpaceID string   `json:"space_id"`
	Nodes   []string `json:"nodes,omitempty"`
}

// Claim is a one-time token an agent redeems for a long-lived stream key.
type Claim struct {
	Token   string `json:"token"`
	SpaceID string `json:"space_id"`
	RoomID  string `json:"room_id"`
	APIKey  string `json:"api_key,omitempty"`
	NodeID  string `json:"node_id,omitempty"`
	Expires int64  `json:"expires"`
	UsedAt  int64  `json:"used_at,omitempty"`
}

// NodeConfig is a hub-pushed overlay (disabled collectors + optional full
// YAML replacement) plus the agent's last reported file and apply outcome.
type NodeConfig struct {
	NodeID   string      `json:"node_id"`
	Disabled []string    `json:"disabled,omitempty"`
	YAML     string      `json:"yaml,omitempty"`     // desired full config (admin-edited)
	Updated  int64       `json:"updated"`            // monotonically increasing desired revision
	Reported string      `json:"reported,omitempty"` // agent's actual file text
	ReportAt int64       `json:"report_at,omitempty"`
	Apply    *ApplyState `json:"apply,omitempty"` // outcome acked by the agent
}

// ApplyState mirrors an agent's outcome for one pushed config revision.
type ApplyState struct {
	Rev   int64  `json:"rev"`
	State string `json:"state"` // applied | rejected | deferred
	Error string `json:"error,omitempty"`
	At    int64  `json:"at"`
}

// Org persists Spaces, Rooms, claim tokens and per-node config next to nodes.json.
type Org struct {
	path string
	now  func() time.Time

	mu      sync.Mutex
	spaces  map[string]*Space
	rooms   map[string]*Room
	claims  map[string]*Claim
	configs map[string]*NodeConfig
	keys    []string
}

type orgFile struct {
	Spaces  []*Space               `json:"spaces"`
	Rooms   []*Room                `json:"rooms"`
	Claims  []*Claim               `json:"claims"`
	Configs map[string]*NodeConfig `json:"configs"`
	Keys    []string               `json:"keys"`
}

// OpenOrg loads org.json from dir (created on first save).
func OpenOrg(dir string) (*Org, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	o := &Org{path: filepath.Join(dir, "org.json"), now: time.Now,
		spaces: map[string]*Space{}, rooms: map[string]*Room{},
		claims: map[string]*Claim{}, configs: map[string]*NodeConfig{}}
	b, err := os.ReadFile(o.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return o, nil
		}
		return nil, err
	}
	var f orgFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", o.path, err)
	}
	for _, s := range f.Spaces {
		o.spaces[s.ID] = s
	}
	for _, r := range f.Rooms {
		o.rooms[r.ID] = r
	}
	for _, c := range f.Claims {
		o.claims[c.Token] = c
	}
	if f.Configs != nil {
		o.configs = f.Configs
	}
	o.keys = append(o.keys, f.Keys...)
	info, err := os.Lstat(o.path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		// Secure files written by older versions even if this hub only serves
		// reads after upgrading. Replace symlinks instead of chmodding targets.
		if err := writeOrgFile(o.path, b); err != nil {
			return nil, fmt.Errorf("secure org credentials: %w", err)
		}
	}
	return o, nil
}

func (o *Org) saveLocked() error {
	f := orgFile{Configs: o.configs, Keys: append([]string(nil), o.keys...)}
	for _, s := range o.spaces {
		f.Spaces = append(f.Spaces, s)
	}
	for _, r := range o.rooms {
		f.Rooms = append(f.Rooms, r)
	}
	for _, c := range o.claims {
		f.Claims = append(f.Claims, c)
	}
	sort.Slice(f.Spaces, func(i, j int) bool { return f.Spaces[i].ID < f.Spaces[j].ID })
	sort.Slice(f.Rooms, func(i, j int) bool { return f.Rooms[i].ID < f.Rooms[j].ID })
	sort.Slice(f.Claims, func(i, j int) bool { return f.Claims[i].Token < f.Claims[j].Token })
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return writeOrgFile(o.path, b)
}

func writeOrgFile(path string, content []byte) error {
	// The org file contains stream keys and desired/reported YAML credentials.
	// A fresh private temporary file also avoids following a stale .tmp symlink.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".org-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (o *Org) Keys() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.keys...)
}

func (o *Org) Spaces() []Space {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Space, 0, len(o.spaces))
	for _, s := range o.spaces {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (o *Org) Rooms(spaceID string) []Room {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Room, 0, len(o.rooms))
	for _, r := range o.rooms {
		if spaceID != "" && r.SpaceID != spaceID {
			continue
		}
		cp := *r
		cp.Nodes = append([]string(nil), r.Nodes...)
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (o *Org) CreateSpace(name string) (Space, error) {
	if name == "" {
		return Space{}, errors.New("name required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	id := randomID("spc")
	s := &Space{ID: id, Name: name, Created: o.now().Unix()}
	o.spaces[id] = s
	return *s, o.saveLocked()
}

func (o *Org) DeleteSpace(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.spaces[id]; !ok {
		return errors.New("unknown space")
	}
	for rid, r := range o.rooms {
		if r.SpaceID == id {
			delete(o.rooms, rid)
		}
	}
	delete(o.spaces, id)
	return o.saveLocked()
}

func (o *Org) CreateRoom(spaceID, name string) (Room, error) {
	if name == "" {
		return Room{}, errors.New("name required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.spaces[spaceID]; !ok {
		return Room{}, errors.New("unknown space")
	}
	id := randomID("rom")
	r := &Room{ID: id, Name: name, SpaceID: spaceID}
	o.rooms[id] = r
	return *r, o.saveLocked()
}

func (o *Org) DeleteRoom(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.rooms[id]; !ok {
		return errors.New("unknown room")
	}
	delete(o.rooms, id)
	return o.saveLocked()
}

func (o *Org) SetRoomNodes(id string, nodes []string) (Room, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	r, ok := o.rooms[id]
	if !ok {
		return Room{}, errors.New("unknown room")
	}
	r.Nodes = append([]string(nil), nodes...)
	cp := *r
	cp.Nodes = append([]string(nil), r.Nodes...)
	return cp, o.saveLocked()
}

func (o *Org) Membership(nodeID string) (spaceID, roomID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, r := range o.rooms {
		for _, n := range r.Nodes {
			if n == nodeID {
				return r.SpaceID, r.ID
			}
		}
	}
	return "", ""
}

func (o *Org) IssueClaim(spaceID, roomID string, ttl time.Duration) (Claim, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.spaces[spaceID]; !ok {
		return Claim{}, errors.New("unknown space")
	}
	r, ok := o.rooms[roomID]
	if !ok || r.SpaceID != spaceID {
		return Claim{}, errors.New("unknown room")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	tok := randomID("clm")
	c := &Claim{Token: tok, SpaceID: spaceID, RoomID: roomID, Expires: o.now().Add(ttl).Unix()}
	o.claims[tok] = c
	return *c, o.saveLocked()
}

func (o *Org) Claims() []Claim {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Claim, 0, len(o.claims))
	for _, c := range o.claims {
		cp := *c
		cp.APIKey = "" // stream keys are only returned at redeem time
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Expires > out[j].Expires })
	return out
}

// RedeemClaim consumes a one-time token and returns the minted stream API key.
func (o *Org) RedeemClaim(token, nodeID string) (Claim, error) {
	if token == "" || nodeID == "" {
		return Claim{}, errors.New("token and node_id required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	c, ok := o.claims[token]
	if !ok {
		return Claim{}, errors.New("unknown claim token")
	}
	now := o.now().Unix()
	if c.UsedAt != 0 {
		return Claim{}, errors.New("claim token already used")
	}
	if c.Expires > 0 && now > c.Expires {
		return Claim{}, errors.New("claim token expired")
	}
	for _, existing := range o.claims {
		if existing.NodeID == nodeID && existing.APIKey != "" {
			return Claim{}, errors.New("node_id is already claimed")
		}
	}
	key := randomID("key")
	c.APIKey = key
	c.NodeID = nodeID
	c.UsedAt = now
	o.keys = append(o.keys, key)
	if r, ok := o.rooms[c.RoomID]; ok {
		found := false
		for _, n := range r.Nodes {
			if n == nodeID {
				found = true
				break
			}
		}
		if !found {
			r.Nodes = append(r.Nodes, nodeID)
		}
	}
	out := *c
	return out, o.saveLocked()
}

// ErrConfigConflict means the desired config changed after the caller read it.
var ErrConfigConflict = errors.New("config changed since read")

// SetConfig stores the desired overlay, preserving the agent-reported file
// state and last apply outcome.
func (o *Org) SetConfig(cfg NodeConfig) (NodeConfig, error) {
	return o.UpdateConfig(cfg.NodeID, nil, func(NodeConfig) (NodeConfig, error) { return cfg, nil })
}

// UpdateConfig compares the caller's revision and computes the desired config
// under one lock. A nil revision keeps the legacy unconditional-write behavior;
// a pointer to zero requires that no desired config has been stored yet. The
// callback receives a copy and must not call another Org method. Only YAML and
// Disabled are taken from its result; agent reports and apply outcomes survive.
func (o *Org) UpdateConfig(nodeID string, expected *int64, update func(NodeConfig) (NodeConfig, error)) (NodeConfig, error) {
	if nodeID == "" {
		return NodeConfig{}, errors.New("node_id required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	previous := o.configs[nodeID]
	current := NodeConfig{NodeID: nodeID}
	if previous != nil {
		current = *copyConfig(previous)
	}
	if expected != nil && *expected != current.Updated {
		return NodeConfig{}, ErrConfigConflict
	}
	desired, err := update(*copyConfig(&current))
	if err != nil {
		return NodeConfig{}, err
	}
	current.Disabled = append([]string(nil), desired.Disabled...)
	current.YAML = desired.YAML
	revision := o.now().Unix()
	if revision <= current.Updated {
		revision = current.Updated + 1
	}
	current.Updated = revision
	o.configs[nodeID] = &current
	if err := o.saveLocked(); err != nil {
		if previous == nil {
			delete(o.configs, nodeID)
		} else {
			o.configs[nodeID] = previous
		}
		return NodeConfig{}, err
	}
	return *copyConfig(&current), nil
}

// SetReport records the agent-reported config file, preserving desired state.
func (o *Org) SetReport(nodeID, yaml string, at int64) error {
	if nodeID == "" {
		return errors.New("node_id required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := o.configs[nodeID]
	if cp == nil {
		cp = &NodeConfig{NodeID: nodeID}
		o.configs[nodeID] = cp
	}
	cp.Reported, cp.ReportAt = yaml, at
	return o.saveLocked()
}

// SetApply records an agent apply outcome, preserving desired state.
func (o *Org) SetApply(nodeID string, st ApplyState) error {
	if nodeID == "" {
		return errors.New("node_id required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	cp := o.configs[nodeID]
	if cp == nil {
		cp = &NodeConfig{NodeID: nodeID}
		o.configs[nodeID] = cp
	}
	ap := st
	cp.Apply = &ap
	return o.saveLocked()
}

func copyConfig(c *NodeConfig) *NodeConfig {
	out := *c
	out.Disabled = append([]string(nil), c.Disabled...)
	if c.Apply != nil {
		ap := *c.Apply
		out.Apply = &ap
	}
	return &out
}

func (o *Org) GetConfig(nodeID string) (NodeConfig, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	c, ok := o.configs[nodeID]
	if !ok {
		return NodeConfig{}, false
	}
	return *copyConfig(c), true
}

func (o *Org) ConfigByKey(apiKey string) (NodeConfig, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, c := range o.claims {
		if c.APIKey != "" && c.APIKey == apiKey && c.NodeID != "" {
			if cfg, ok := o.configs[c.NodeID]; ok {
				return *copyConfig(cfg), true
			}
			return NodeConfig{NodeID: c.NodeID}, true
		}
	}
	return NodeConfig{}, false
}

func randomID(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	}
	return prefix + "-" + hex.EncodeToString(b[:])
}
