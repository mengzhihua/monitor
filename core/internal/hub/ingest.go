package hub

import (
	"crypto/sha1"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

const (
	readTimeout  = 90 * time.Second
	writeTimeout = 10 * time.Second
	pingEvery    = 30 * time.Second
)

// IngestEnabled reports whether any agent key is configured.
func (n *Nodes) IngestEnabled() bool { return len(n.opt.Keys) > 0 }

// Authorized checks an agent's stream credential.
func (n *Nodes) Authorized(r *http.Request) bool {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" || key == r.Header.Get("Authorization") {
		key = r.URL.Query().Get("api_key")
	}
	if key == "" {
		return false
	}
	for _, k := range n.opt.Keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

var upgrader = websocket.Upgrader{ReadBufferSize: 64 * 1024, WriteBufferSize: 16 * 1024,
	CheckOrigin: func(*http.Request) bool { return true }} // agents, not browsers

// HandleStream is the /api/v1/stream endpoint agents connect to.
func (n *Nodes) HandleStream(w http.ResponseWriter, r *http.Request) {
	if !n.IngestEnabled() {
		http.Error(w, "streaming not enabled on this hub", http.StatusNotFound)
		return
	}
	if !n.Authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &session{n: n, ws: ws, out: make(chan []byte, 4096), done: make(chan struct{}), remote: r.RemoteAddr}
	s.serve()
}

// session is one agent connection.
type session struct {
	n      *Nodes
	ws     *websocket.Conn
	out    chan []byte
	done   chan struct{}
	remote string
	node   *Node
	once   sync.Once
	err    error
}

func (s *session) send(f stream.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	select {
	case s.out <- b:
		return nil
	case <-s.done:
		return errors.New("agent disconnected")
	case <-time.After(writeTimeout):
		return errors.New("agent not reading")
	}
}

func (s *session) close(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		_ = s.ws.Close()
	})
}

func (s *session) writer() {
	ping := time.NewTicker(pingEvery)
	defer ping.Stop()
	for {
		select {
		case <-s.done:
			return
		case b := <-s.out:
			_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := s.ws.WriteMessage(websocket.TextMessage, b); err != nil {
				s.close(err)
				return
			}
		case <-ping.C:
			_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := s.ws.WriteMessage(websocket.PingMessage, nil); err != nil {
				s.close(err)
				return
			}
		}
	}
}

// nodeID derives a filesystem/URL-safe node id from the agent's host id.
func nodeID(h *registry.Host) string {
	id := h.ID
	if id == "" {
		id = h.Hostname
	}
	safe := true
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.') {
			safe = false
			break
		}
	}
	if safe && id != "" && len(id) <= 64 {
		return id
	}
	sum := sha1.Sum([]byte(id))
	return hex.EncodeToString(sum[:8])
}

func (s *session) serve() {
	defer s.close(nil)
	go s.writer()

	s.ws.SetReadLimit(4 << 20)
	_ = s.ws.SetReadDeadline(time.Now().Add(writeTimeout))
	var hello stream.Frame
	if err := s.ws.ReadJSON(&hello); err != nil || hello.Type != stream.TypeHello || hello.Host == nil {
		_ = s.send(stream.Frame{Type: stream.TypeError, Error: "expected hello"})
		return
	}
	now := s.n.opt.Now()
	node := s.attach(hello, now)
	defer s.detach(node, now)

	last := map[string]int64{}
	for _, c := range node.reg.Charts() {
		for _, d := range c.Dims() {
			if _, l, ok := node.db.Bounds(registry.SeriesID(c.ID, d.ID)); ok && l > last[c.ID] {
				last[c.ID] = l
			}
		}
	}
	if err := s.send(stream.Frame{Type: stream.TypeWelcome, Last: last, ReplicateFrom: now.Unix() - int64(s.n.opt.Replicate.Seconds())}); err != nil {
		return
	}
	s.n.log.Info("hub: node connected", "node", node.ID, "hostname", node.Host.Hostname, "from", s.remote, "charts", len(last))

	s.ws.SetPongHandler(func(string) error { return s.ws.SetReadDeadline(time.Now().Add(readTimeout)) })
	for {
		_ = s.ws.SetReadDeadline(time.Now().Add(readTimeout))
		var f stream.Frame
		if err := s.ws.ReadJSON(&f); err != nil {
			if !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.n.log.Warn("hub: node disconnected", "node", node.ID, "err", err)
			} else {
				s.n.log.Info("hub: node disconnected", "node", node.ID)
			}
			return
		}
		select {
		case <-s.done: // replaced by a newer connection of the same node
			return
		default:
		}
		s.handle(node, f)
	}
}

// attach registers (or refreshes) the node for this connection, replacing
// any previous connection of the same node.
func (s *session) attach(hello stream.Frame, now time.Time) *Node {
	id := nodeID(hello.Host)
	s.n.mu.Lock()
	node, ok := s.n.nodes[id]
	if !ok {
		node = s.n.newNode(id, *hello.Host)
		node.FirstSeen = now.Unix()
		s.n.nodes[id] = node
	}
	s.n.dirty = true
	s.n.mu.Unlock()

	node.mu.Lock()
	old := node.conn
	node.conn = s
	node.Host = *hello.Host
	node.Version = hello.Version
	node.LastSeen = now.Unix()
	node.lastData = 0
	node.functions = hello.Functions
	node.mu.Unlock()
	s.node = node
	if old != nil {
		old.close(errors.New("replaced by a new connection"))
	}
	return node
}

func (s *session) detach(node *Node, _ time.Time) {
	node.mu.Lock()
	if node.conn == s {
		node.conn = nil
		node.LastSeen = s.n.opt.Now().Unix()
	}
	node.mu.Unlock()
	s.n.mu.Lock()
	s.n.dirty = true
	s.n.mu.Unlock()
}

func (s *session) handle(node *Node, f stream.Frame) {
	switch f.Type {
	case stream.TypeChart:
		if f.Chart == nil || f.Chart.ID == "" {
			return
		}
		if old, ok := node.reg.Chart(f.Chart.ID); ok {
			// same id, possibly new dimensions: add the missing ones
			for _, d := range f.Chart.Dimensions {
				old.AddDimension(&registry.Dimension{ID: d.ID, Name: d.Name, Hidden: d.Hidden, Algorithm: registry.Absolute})
			}
			return
		}
		node.reg.AddChart(f.Chart.ToChart())
		s.n.mu.Lock()
		s.n.dirty = true
		s.n.mu.Unlock()
	case stream.TypeChartDel:
		if node.reg.RemoveChart(f.ID) {
			s.n.mu.Lock()
			s.n.dirty = true
			s.n.mu.Unlock()
		}
	case stream.TypeData:
		if err := node.reg.Ingest(f.ChartID, f.T, f.V); err != nil {
			return
		}
		if !f.Replay {
			node.mu.Lock()
			if f.T > node.lastData {
				node.lastData = f.T
			}
			node.LastSeen = s.n.opt.Now().Unix()
			node.mu.Unlock()
		}
	case stream.TypeAlarm:
		if f.Alarm == nil {
			return
		}
		e := *f.Alarm
		if e.Hostname == "" {
			e.Hostname = node.Host.Hostname
		}
		node.recordAlarm(e)
		if s.n.opt.OnAlarm != nil {
			s.n.opt.OnAlarm(node.ID, e)
		}
	case stream.TypeFuncResult:
		node.deliverResult(f)
	default:
		s.n.log.Debug("hub: unknown frame", "node", node.ID, "type", f.Type)
	}
}

// String implements fmt.Stringer for logs.
func (nd *Node) String() string { return fmt.Sprintf("%s(%s)", nd.ID, nd.Host.Hostname) }
