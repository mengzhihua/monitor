package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// liveHub fans out every completed chart collection to WebSocket clients.
// Protocol (server → client), one JSON object per message:
//
//	{"chart":"system.cpu","t":1700000000,"v":{"user":12.3,"system":4.5}}
//
// Clients may send {"charts":["system.cpu","system.ram"]} at any time to
// change their subscription; an empty list means all charts. On a hub,
// ?node=<id> (or {"node":"id"}) selects which node's samples to receive;
// remote samples carry "node":"<id>".
type liveHub struct {
	log      *slog.Logger
	upgrader websocket.Upgrader

	mu    sync.RWMutex
	conns map[*liveConn]struct{}
}

type liveConn struct {
	ws      *websocket.Conn
	send    chan []byte
	mu      sync.RWMutex
	node    string          // "" = local host
	charts  map[string]bool // nil = all
	resolve func(string) (string, bool)
}

type liveMsg struct {
	Node   string             `json:"node,omitempty"`
	Chart  string             `json:"chart"`
	T      int64              `json:"t"`
	Values map[string]float64 `json:"v"`
}

const (
	// wsProto is the subprotocol the dashboard offers; the agent selects it so
	// the browser accepts the handshake when a bearer.<token> entry is also sent.
	wsProto      = "monitor"
	wsTokenProto = "bearer."
)

func newLiveHub(reg *registry.Registry, log *slog.Logger) *liveHub {
	h := &liveHub{
		log:   log,
		conns: map[*liveConn]struct{}{},
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 16 * 1024,
			CheckOrigin:     sameHostOrigin,
			Subprotocols:    []string{wsProto},
		},
	}
	reg.Subscribe(h.broadcast)
	return h
}

// sameHostOrigin accepts non-browser clients (no Origin) and browsers whose
// page was served from this agent, blocking cross-site pages from reading
// the live feed through the visitor's browser.
func sameHostOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

func (h *liveHub) broadcast(chartID string, ts int64, values map[string]float64) {
	h.broadcastNode("", chartID, ts, values)
}

func (h *liveHub) broadcastNode(node, chartID string, ts int64, values map[string]float64) {
	h.mu.RLock()
	n := len(h.conns)
	h.mu.RUnlock()
	if n == 0 {
		return
	}
	b, err := json.Marshal(liveMsg{Node: node, Chart: chartID, T: ts, Values: values})
	if err != nil {
		return
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.conns {
		if !c.wants(node, chartID) {
			continue
		}
		select {
		case c.send <- b:
		default: // slow client: drop the sample rather than block collection
		}
	}
}

// broadcastAll sends b to every client watching node, regardless of chart
// subscription.
func (h *liveHub) broadcastAll(node string, b []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.conns {
		if !c.wants(node, "") {
			continue
		}
		select {
		case c.send <- b:
		default:
		}
	}
}

func (c *liveConn) wants(node, chart string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.node != node {
		return false
	}
	return chart == "" || c.charts == nil || c.charts[chart]
}

func (c *liveConn) setNode(node string) {
	c.mu.Lock()
	c.node = node
	c.mu.Unlock()
}

func (c *liveConn) setCharts(list []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(list) == 0 {
		c.charts = nil
		return
	}
	c.charts = make(map[string]bool, len(list))
	for _, id := range list {
		if id = strings.TrimSpace(id); id != "" {
			c.charts[id] = true
		}
	}
}

// handle serves an upgraded live connection scoped to node ("" = local);
// resolve validates in-band node switches and returns the canonical id.
func (h *liveHub) handle(w http.ResponseWriter, r *http.Request, node string, resolve func(string) (string, bool)) {
	ws, err := h.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &liveConn{ws: ws, send: make(chan []byte, 256), node: node, resolve: resolve}
	if q := r.URL.Query().Get("charts"); q != "" {
		c.setCharts(strings.Split(q, ","))
	}
	h.mu.Lock()
	h.conns[c] = struct{}{}
	h.mu.Unlock()

	go h.writer(c)
	h.reader(c) // blocks until the client disconnects

	h.mu.Lock()
	delete(h.conns, c)
	h.mu.Unlock()
	close(c.send)
}

func (h *liveHub) reader(c *liveConn) {
	c.ws.SetReadLimit(64 * 1024)
	_ = c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	})
	for {
		_, msg, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		_ = c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		var req struct {
			Charts []string `json:"charts"`
			Node   *string  `json:"node"`
		}
		if json.Unmarshal(msg, &req) == nil {
			c.setCharts(req.Charts)
			if req.Node != nil {
				if n, ok := c.resolve(*req.Node); ok {
					c.setNode(n)
				} else if b, err := json.Marshal(map[string]string{"error": "unknown node " + *req.Node}); err == nil {
					select {
					case c.send <- b:
					default:
					}
				}
			}
		}
	}
}

func (h *liveHub) writer(c *liveConn) {
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	defer c.ws.Close()
	for {
		select {
		case b, ok := <-c.send:
			if !ok {
				_ = c.ws.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.TextMessage, b); err != nil {
				return
			}
		case <-ping.C:
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
