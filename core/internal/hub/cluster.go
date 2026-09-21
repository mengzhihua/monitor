package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

const clusterHopHeader = "X-Monitor-Cluster-Hop"

// Cluster fans GET /api/v1/nodes out to peer hubs and reverse-proxies
// requests for nodes this process does not own (Netdata parent cluster).
type Cluster struct {
	peers []string
	token string
	log   *slog.Logger
	http  *http.Client

	mu    sync.RWMutex
	index map[string]peerNode // node id → location
	nodes *Nodes
}

type peerNode struct {
	Info
	URL string `json:"-"`
}

func NewCluster(peers []string, token string, log *slog.Logger) *Cluster {
	if log == nil {
		log = slog.Default()
	}
	clean := peers[:0]
	for _, p := range peers {
		p = strings.TrimRight(p, "/")
		if p == "" {
			continue
		}
		if !strings.Contains(p, "://") {
			p = "http://" + p
		}
		clean = append(clean, p)
	}
	return &Cluster{peers: append([]string(nil), clean...), token: token, log: log,
		http: &http.Client{Timeout: 5 * time.Second}, index: map[string]peerNode{}}
}

func (c *Cluster) Empty() bool { return c == nil || len(c.peers) == 0 }

func (c *Cluster) Run(ctx context.Context) {
	if c.Empty() {
		return
	}
	c.refresh()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.refresh()
		}
	}
}

func (c *Cluster) refresh() {
	next := map[string]peerNode{}
	for _, peer := range c.peers {
		req, err := http.NewRequest(http.MethodGet, peer+"/api/v1/nodes", nil)
		if err != nil {
			continue
		}
		req.Header.Set(clusterHopHeader, "1")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			c.log.Warn("cluster peer unreachable", "peer", peer, "err", err)
			continue
		}
		var body struct {
			Nodes []Info `json:"nodes"`
		}
		err = json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&body)
		resp.Body.Close()
		if err != nil || resp.StatusCode >= 300 {
			c.log.Warn("cluster peer nodes", "peer", peer, "status", resp.StatusCode, "err", err)
			continue
		}
		for _, n := range body.Nodes {
			if n.Local || n.ID == "" {
				continue
			}
			n.Peer = peer
			next[n.ID] = peerNode{Info: n, URL: peer}
		}
	}
	c.mu.Lock()
	c.index = next
	c.mu.Unlock()
	c.pushRing()
}

// PeerInfos is the remote-node catalog discovered from peers (not locally streamed).
func (c *Cluster) PeerInfos() []Info {
	if c.Empty() {
		return nil
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]Info, 0, len(c.index))
	for _, n := range c.index {
		out = append(out, n.Info)
	}
	return out
}

func (c *Cluster) Has(id string) bool {
	if c.Empty() {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.index[id]
	return ok
}

// Proxy reverse-proxies r to the peer that last advertised id. Returns true
// if the request was handled (including errors written to w).
func (c *Cluster) Proxy(w http.ResponseWriter, r *http.Request, id string) bool {
	if c.Empty() || id == "" {
		return false
	}
	if r.Header.Get(clusterHopHeader) != "" {
		return false
	}
	c.mu.RLock()
	n, ok := c.index[id]
	c.mu.RUnlock()
	if !ok {
		return false
	}
	u, err := url.Parse(n.URL)
	if err != nil {
		http.Error(w, "bad peer url", http.StatusBadGateway)
		return true
	}
	proxy := httputil.NewSingleHostReverseProxy(u)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		http.Error(w, fmt.Sprintf("peer %s: %v", n.URL, err), http.StatusBadGateway)
	}
	r.Header.Set(clusterHopHeader, "1")
	if c.token != "" && r.Header.Get("Authorization") == "" {
		r.Header.Set("Authorization", "Bearer "+c.token)
	}
	proxy.ServeHTTP(w, r)
	return true
}

// SetNodes wires local streamed nodes so refresh() can push last samples to peers.
func (c *Cluster) SetNodes(n *Nodes) { c.nodes = n }

// RingPayload is the JSON body of POST /api/v1/hub/ring.
type RingPayload struct {
	Host    registry.Host      `json:"host"`
	Charts  []*stream.ChartDef `json:"charts"`
	Samples []ReplicaSample    `json:"samples"`
}

func (c *Cluster) pushRing() {
	if c.nodes == nil {
		return
	}
	for _, node := range c.nodes.List() {
		host, charts, samples := node.SnapshotRing()
		if len(samples) == 0 {
			continue
		}
		defs := make([]*stream.ChartDef, 0, len(charts))
		for _, ch := range charts {
			defs = append(defs, stream.DefOf(ch))
		}
		body, err := json.Marshal(RingPayload{Host: host, Charts: defs, Samples: samples})
		if err != nil {
			continue
		}
		for _, peer := range c.peers {
			req, err := http.NewRequest(http.MethodPost, peer+"/api/v1/hub/ring", bytes.NewReader(body))
			if err != nil {
				continue
			}
			req.Header.Set(clusterHopHeader, "1")
			req.Header.Set("Content-Type", "application/json")
			if c.token != "" {
				req.Header.Set("Authorization", "Bearer "+c.token)
			}
			resp, err := c.http.Do(req)
			if err != nil {
				c.log.Warn("cluster ring push", "peer", peer, "err", err)
				continue
			}
			resp.Body.Close()
		}
	}
}
