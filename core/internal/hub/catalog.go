package hub

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// HostGroup is a node collection independent of Rooms. ParentID forms a tree.
type HostGroup struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	ParentID string   `json:"parent_id,omitempty"`
	Nodes    []string `json:"nodes,omitempty"`
}

// Proxy is a downstream hub that serves samples for the nodes assigned to it.
// Active proxies must heartbeat. Passive proxies are pulled over their HTTP API.
type Proxy struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	URL      string   `json:"url"`
	Mode     string   `json:"mode"`
	Token    string   `json:"token,omitempty"`
	LastSeen int64    `json:"last_seen,omitempty"`
	Nodes    []string `json:"nodes,omitempty"`
}

const (
	maxHostGroups  = 256
	maxProxyCount  = 64
	maxGroupNodes  = 1024
	hostGroupDepth = 8
	proxyFreshFor  = 2 * time.Minute
)

func (o *Org) HostGroups() []HostGroup {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]HostGroup, 0, len(o.hostGroups))
	for _, g := range o.hostGroups {
		out = append(out, copyHostGroup(g))
	}
	return out
}

func (o *Org) CreateHostGroup(name, parentID string) (HostGroup, error) {
	name = strings.TrimSpace(name)
	if err := validCatalogName(name); err != nil {
		return HostGroup{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.hostGroups) >= maxHostGroups {
		return HostGroup{}, fmt.Errorf("at most %d host groups", maxHostGroups)
	}
	if err := o.hostGroupParentLocked("", parentID); err != nil {
		return HostGroup{}, err
	}
	if o.hostGroupNameTakenLocked(name, "") {
		return HostGroup{}, errors.New("host group name already exists")
	}
	g := &HostGroup{ID: randomID("hg"), Name: name, ParentID: parentID}
	o.hostGroups[g.ID] = g
	if err := o.saveLocked(); err != nil {
		delete(o.hostGroups, g.ID)
		return HostGroup{}, err
	}
	return copyHostGroup(g), nil
}

func (o *Org) UpdateHostGroup(id, name, parentID string, nodes []string) (HostGroup, error) {
	name = strings.TrimSpace(name)
	if err := validCatalogName(name); err != nil {
		return HostGroup{}, err
	}
	nodes, err := cleanNodeIDs(nodes)
	if err != nil {
		return HostGroup{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	g, ok := o.hostGroups[id]
	if !ok {
		return HostGroup{}, errors.New("unknown host group")
	}
	if err := o.hostGroupParentLocked(id, parentID); err != nil {
		return HostGroup{}, err
	}
	if o.hostGroupNameTakenLocked(name, id) {
		return HostGroup{}, errors.New("host group name already exists")
	}
	prevName, prevParent, prevNodes := g.Name, g.ParentID, g.Nodes
	g.Name, g.ParentID, g.Nodes = name, parentID, nodes
	if err := o.saveLocked(); err != nil {
		g.Name, g.ParentID, g.Nodes = prevName, prevParent, prevNodes
		return HostGroup{}, err
	}
	return copyHostGroup(g), nil
}

func (o *Org) DeleteHostGroup(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	g, ok := o.hostGroups[id]
	if !ok {
		return errors.New("unknown host group")
	}
	for _, other := range o.hostGroups {
		if other.ParentID == id {
			return errors.New("host group has children")
		}
	}
	delete(o.hostGroups, id)
	if err := o.saveLocked(); err != nil {
		o.hostGroups[id] = g
		return err
	}
	return nil
}

// NodeInHostGroups reports whether nodeID belongs to one of roots or a descendant.
func (o *Org) NodeInHostGroups(nodeID string, roots []string) bool {
	if o == nil || nodeID == "" || len(roots) == 0 {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	children := map[string][]string{}
	for _, g := range o.hostGroups {
		if g.ParentID != "" {
			children[g.ParentID] = append(children[g.ParentID], g.ID)
		}
	}
	seen := map[string]bool{}
	var walk func(id string, depth int) bool
	walk = func(id string, depth int) bool {
		if id == "" || seen[id] || depth > hostGroupDepth {
			return false
		}
		seen[id] = true
		g, ok := o.hostGroups[id]
		if !ok {
			return false
		}
		for _, node := range g.Nodes {
			if node == nodeID {
				return true
			}
		}
		for _, child := range children[id] {
			if walk(child, depth+1) {
				return true
			}
		}
		return false
	}
	for _, root := range roots {
		if walk(root, 0) {
			return true
		}
	}
	return false
}

func (o *Org) Proxies() []Proxy {
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]Proxy, 0, len(o.proxies))
	for _, p := range o.proxies {
		cp := *p
		cp.Token = ""
		cp.Nodes = append([]string(nil), p.Nodes...)
		out = append(out, cp)
	}
	return out
}

func (o *Org) CreateProxy(name, rawURL, mode string, nodes []string) (Proxy, error) {
	name = strings.TrimSpace(name)
	if err := validCatalogName(name); err != nil {
		return Proxy{}, err
	}
	mode, rawURL, nodes, err := cleanProxy(mode, rawURL, nodes)
	if err != nil {
		return Proxy{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.proxies) >= maxProxyCount {
		return Proxy{}, fmt.Errorf("at most %d proxies", maxProxyCount)
	}
	token, err := proxyToken()
	if err != nil {
		return Proxy{}, err
	}
	p := &Proxy{ID: randomID("prx"), Name: name, URL: rawURL, Mode: mode, Token: token, Nodes: nodes}
	previous := o.claimProxyNodesLocked(p)
	o.proxies[p.ID] = p
	if err := o.saveLocked(); err != nil {
		delete(o.proxies, p.ID)
		o.restoreProxyNodesLocked(previous)
		return Proxy{}, err
	}
	return *p, nil
}

func (o *Org) UpdateProxy(id, name, rawURL, mode string, nodes []string) (Proxy, error) {
	name = strings.TrimSpace(name)
	if err := validCatalogName(name); err != nil {
		return Proxy{}, err
	}
	mode, rawURL, nodes, err := cleanProxy(mode, rawURL, nodes)
	if err != nil {
		return Proxy{}, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.proxies[id]
	if !ok {
		return Proxy{}, errors.New("unknown proxy")
	}
	prev := *p
	prev.Nodes = append([]string(nil), p.Nodes...)
	p.Name, p.URL, p.Mode, p.Nodes = name, rawURL, mode, nodes
	previous := o.claimProxyNodesLocked(p)
	if err := o.saveLocked(); err != nil {
		*p = prev
		o.restoreProxyNodesLocked(previous)
		return Proxy{}, err
	}
	cp := *p
	cp.Token = ""
	cp.Nodes = append([]string(nil), p.Nodes...)
	return cp, nil
}

func (o *Org) DeleteProxy(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.proxies[id]
	if !ok {
		return errors.New("unknown proxy")
	}
	delete(o.proxies, id)
	if err := o.saveLocked(); err != nil {
		o.proxies[id] = p
		return err
	}
	return nil
}

// TouchProxy records a heartbeat when token matches. The proxy token is not a user credential.
func (o *Org) TouchProxy(id, token string, now time.Time) error {
	if id == "" || token == "" {
		return errors.New("proxy id and token required")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	p, ok := o.proxies[id]
	if !ok {
		return errors.New("unknown proxy")
	}
	if p.Token != token {
		return errors.New("unauthorized")
	}
	prev := p.LastSeen
	p.LastSeen = now.Unix()
	if err := o.saveLocked(); err != nil {
		p.LastSeen = prev
		return err
	}
	return nil
}

// ProxyRoute returns the proxy base URL that should serve nodeID.
// Active proxies must have heartbeated inside the freshness window.
func (o *Org) ProxyRoute(nodeID string, now time.Time) (string, bool) {
	if o == nil || nodeID == "" {
		return "", false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range o.proxies {
		if !containsNode(p.Nodes, nodeID) || p.URL == "" {
			continue
		}
		if p.Mode == "active" && now.Sub(time.Unix(p.LastSeen, 0)) > proxyFreshFor {
			return "", false
		}
		return p.URL, true
	}
	return "", false
}

func (o *Org) claimProxyNodesLocked(owner *Proxy) map[string][]string {
	previous := map[string][]string{}
	for _, other := range o.proxies {
		if other.ID == owner.ID {
			continue
		}
		previous[other.ID] = append([]string(nil), other.Nodes...)
		kept := make([]string, 0, len(other.Nodes))
		for _, node := range other.Nodes {
			if !containsNode(owner.Nodes, node) {
				kept = append(kept, node)
			}
		}
		other.Nodes = kept
	}
	return previous
}

func (o *Org) restoreProxyNodesLocked(previous map[string][]string) {
	for id, nodes := range previous {
		if p, ok := o.proxies[id]; ok {
			p.Nodes = nodes
		}
	}
}

func (o *Org) hostGroupParentLocked(id, parentID string) error {
	if parentID == "" {
		return nil
	}
	if _, ok := o.hostGroups[parentID]; !ok {
		return errors.New("unknown parent host group")
	}
	seen := map[string]bool{}
	for cur, depth := parentID, 1; cur != ""; depth++ {
		if cur == id || seen[cur] || depth > hostGroupDepth {
			return errors.New("host group parent would cycle or exceed depth")
		}
		seen[cur] = true
		parent, ok := o.hostGroups[cur]
		if !ok {
			return errors.New("unknown parent host group")
		}
		cur = parent.ParentID
	}
	return nil
}

func (o *Org) hostGroupNameTakenLocked(name, except string) bool {
	for _, g := range o.hostGroups {
		if g.ID != except && g.Name == name {
			return true
		}
	}
	return false
}

func copyHostGroup(g *HostGroup) HostGroup {
	cp := *g
	cp.Nodes = append([]string(nil), g.Nodes...)
	return cp
}

func validCatalogName(name string) error {
	if name == "" || len(name) > 128 {
		return errors.New("name required")
	}
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("name has control characters")
		}
	}
	return nil
}

func cleanNodeIDs(nodes []string) ([]string, error) {
	if len(nodes) > maxGroupNodes {
		return nil, fmt.Errorf("at most %d nodes", maxGroupNodes)
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(nodes))
	for _, node := range nodes {
		node = strings.TrimSpace(node)
		if node == "" || len(node) > 128 || seen[node] {
			continue
		}
		seen[node] = true
		out = append(out, node)
	}
	return out, nil
}

func cleanProxy(mode, rawURL string, nodes []string) (string, string, []string, error) {
	switch mode {
	case "", "active":
		mode = "active"
	case "passive":
	default:
		return "", "", nil, errors.New("proxy mode must be active or passive")
	}
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if len(rawURL) > 2048 || err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return "", "", nil, errors.New("proxy url must be http(s) without userinfo or fragment")
	}
	nodes, err = cleanNodeIDs(nodes)
	if err != nil {
		return "", "", nil, err
	}
	return mode, u.String(), nodes, nil
}

func proxyToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func containsNode(nodes []string, id string) bool {
	for _, node := range nodes {
		if node == id {
			return true
		}
	}
	return false
}
