package hub

import (
	"crypto/sha1"
	"crypto/sha256"
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

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

const (
	readTimeout  = 90 * time.Second
	writeTimeout = 10 * time.Second
	pingEvery    = 30 * time.Second
)

func (n *Nodes) extraKeys() []string {
	if n.opt.ExtraKeys == nil {
		return nil
	}
	return n.opt.ExtraKeys()
}

func (n *Nodes) allKeys() []string {
	return append(append([]string{}, n.opt.Keys...), n.extraKeys()...)
}

// IngestEnabled reports whether any agent key is configured.
func (n *Nodes) IngestEnabled() bool { return len(n.allKeys()) > 0 }

// Authorized checks an agent's stream credential.
func (n *Nodes) Authorized(r *http.Request) bool {
	_, ok := n.authorize(r)
	return ok
}

// StreamKey extracts the bearer/api_key credential from r.
func StreamKey(r *http.Request) string {
	key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if key == "" || key == r.Header.Get("Authorization") {
		key = r.URL.Query().Get("api_key")
	}
	return key
}

// authorize returns the hash of the accepted key; nodes are bound to it.
func (n *Nodes) authorize(r *http.Request) (string, bool) {
	return n.authorizeKey(StreamKey(r))
}

func (n *Nodes) authorizeKey(key string) (string, bool) {
	if key == "" {
		return "", false
	}
	for _, k := range n.allKeys() {
		if subtle.ConstantTimeCompare([]byte(k), []byte(key)) == 1 {
			sum := sha256.Sum256([]byte(key))
			return hex.EncodeToString(sum[:]), true
		}
	}
	return "", false
}

var upgrader = websocket.Upgrader{ReadBufferSize: 64 * 1024, WriteBufferSize: 16 * 1024,
	CheckOrigin: func(*http.Request) bool { return true }} // agents, not browsers

// HandleStream is the /api/v1/stream endpoint agents connect to.
func (n *Nodes) HandleStream(w http.ResponseWriter, r *http.Request) {
	if !n.IngestEnabled() {
		http.Error(w, "streaming not enabled on this hub", http.StatusNotFound)
		return
	}
	keyHash, ok := n.authorize(r)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &session{n: n, ws: ws, out: make(chan []byte, 4096), done: make(chan struct{}), remote: r.RemoteAddr, keyHash: keyHash}
	s.serve()
}

// session is one agent connection.
type session struct {
	n       *Nodes
	ws      *websocket.Conn
	out     chan []byte
	done    chan struct{}
	remote  string
	keyHash string
	node    *Node
	once    sync.Once
	err     error
	mqtt    bool
}

func (s *session) send(f stream.Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if s.mqtt {
		id := "pending"
		if s.node != nil {
			id = s.node.ID
		}
		b = stream.EncodeMQTTPublish("agent/"+id+"/from-cloud", b)
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
	mt := websocket.TextMessage
	if s.mqtt {
		mt = websocket.BinaryMessage
	}
	for {
		select {
		case <-s.done:
			return
		case b := <-s.out:
			_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if err := s.ws.WriteMessage(mt, b); err != nil {
				s.close(err)
				return
			}
		case <-ping.C:
			_ = s.ws.SetWriteDeadline(time.Now().Add(writeTimeout))
			if s.mqtt {
				if err := s.ws.WriteMessage(websocket.BinaryMessage, stream.EncodeMQTTPingreq()); err != nil {
					s.close(err)
					return
				}
				continue
			}
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
	node, err := s.attach(hello, now)
	if err != nil {
		_ = s.send(stream.Frame{Type: stream.TypeError, Error: err.Error()})
		s.n.log.Warn("hub: node rejected", "host", hello.Host.Hostname, "from", s.remote, "err", err)
		time.Sleep(100 * time.Millisecond) // let the error frame flush before close
		return
	}
	defer s.detach(node, now)

	// Per-chart watermark = the oldest last-sample across its dimensions, so a
	// dimension that lagged (or never arrived) is not skipped by the replay.
	// Re-sent samples are idempotent for the TSDB.
	last := map[string]int64{}
	for _, c := range node.reg.Charts() {
		var wm int64 = -1
		for _, d := range c.Dims() {
			_, l, ok := node.db.Bounds(registry.SeriesID(c.ID, d.ID))
			if !ok {
				l = 0
			}
			if wm < 0 || l < wm {
				wm = l
			}
		}
		if wm > 0 {
			last[c.ID] = wm
		}
	}
	if err := s.send(stream.Frame{Type: stream.TypeWelcome, Last: last, ReplicateFrom: now.Unix() - int64(s.n.opt.Replicate.Seconds())}); err != nil {
		return
	}
	if s.n.opt.NodeConfig != nil {
		if disabled := s.n.opt.NodeConfig(node.ID); len(disabled) > 0 {
			_ = s.send(stream.Frame{Type: stream.TypeConfig, Disabled: disabled})
		}
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

var (
	errTooManyNodes = errors.New("hub: node limit reached")
	errKeyMismatch  = errors.New("hub: node is bound to a different api key")
)

// attach registers (or refreshes) the node for this connection, replacing
// any previous connection of the same node. A node stays bound to the API
// key that first registered it; a different key may not claim its id.
func (s *session) attach(hello stream.Frame, now time.Time) (*Node, error) {
	id := nodeID(hello.Host)
	s.n.mu.Lock()
	node, ok := s.n.nodes[id]
	if !ok {
		if len(s.n.nodes) >= s.n.opt.MaxNodes {
			s.n.mu.Unlock()
			return nil, errTooManyNodes
		}
		node = s.n.newNode(id, *hello.Host)
		node.FirstSeen = now.Unix()
		s.n.nodes[id] = node
	}
	s.n.gen++
	s.n.mu.Unlock()

	node.mu.Lock()
	if node.keyHash != "" && node.keyHash != s.keyHash {
		node.mu.Unlock()
		return nil, errKeyMismatch
	}
	node.keyHash = s.keyHash
	old := node.conn
	node.conn = s
	node.replica = false // a live stream always wins over a ring copy
	node.Host = *hello.Host
	node.Version = hello.Version
	node.LastSeen = now.Unix()
	node.lastData = 0
	node.functions = hello.Functions
	if hello.Protocol != "" {
		node.protocol = hello.Protocol
	} else if s.mqtt {
		node.protocol = "mqtt"
	} else {
		node.protocol = "stream"
	}
	node.mu.Unlock()
	s.node = node
	if old != nil {
		old.close(errors.New("replaced by a new connection"))
	}
	return node, nil
}

func (s *session) detach(node *Node, _ time.Time) {
	node.mu.Lock()
	if node.conn == s {
		node.conn = nil
		node.LastSeen = s.n.opt.Now().Unix()
	}
	node.mu.Unlock()
	s.n.touch()
}

func (s *session) handle(node *Node, f stream.Frame) {
	switch f.Type {
	case stream.TypeChart:
		if f.Chart == nil || f.Chart.ID == "" {
			return
		}
		if len(f.Chart.Dimensions) > s.n.opt.MaxDimsPerChart {
			s.n.log.Warn("hub: chart rejected, too many dimensions", "node", node.ID, "chart", f.Chart.ID, "dims", len(f.Chart.Dimensions))
			return
		}
		if old, ok := node.reg.Chart(f.Chart.ID); ok {
			if stream.DefOf(old).Fingerprint() == f.Chart.Fingerprint() {
				return
			}
			node.reg.ReplaceChart(f.Chart.ToChart())
		} else {
			if len(node.reg.Charts()) >= s.n.opt.MaxChartsPerNode {
				s.n.log.Warn("hub: chart rejected, node chart limit reached", "node", node.ID, "chart", f.Chart.ID)
				return
			}
			node.reg.AddChart(f.Chart.ToChart())
		}
		s.n.touch()
	case stream.TypeChartDel:
		if node.reg.RemoveChart(f.ID) {
			s.n.touch()
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
		s.n.touch()
		if s.n.opt.OnAlarm != nil {
			s.n.opt.OnAlarm(node.ID, e)
		}
	case stream.TypeAlarms:
		snap := f.Alarms
		if snap == nil {
			snap = []health.LogEntry{}
		}
		for i := range snap {
			if snap[i].Hostname == "" {
				snap[i].Hostname = node.Host.Hostname
			}
		}
		changed := node.replaceAlarms(snap)
		s.n.touch()
		if s.n.opt.OnAlarm != nil {
			for _, e := range changed {
				s.n.opt.OnAlarm(node.ID, e)
			}
		}
	case stream.TypeFuncResult, stream.TypeQueryResult:
		node.deliverResult(f)
	default:
		s.n.log.Debug("hub: unknown frame", "node", node.ID, "type", f.Type)
	}
}

// String implements fmt.Stringer for logs.
func (nd *Node) String() string { return fmt.Sprintf("%s(%s)", nd.ID, nd.Host.Hostname) }
