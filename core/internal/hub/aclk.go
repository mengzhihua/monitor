package hub

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

// HandleACLK is /api/v1/aclk: MQTT 3.1.1 over WebSocket carrying the same JSON Frames.
func (n *Nodes) HandleACLK(w http.ResponseWriter, r *http.Request) {
	if !n.IngestEnabled() {
		http.Error(w, "streaming not enabled on this hub", http.StatusNotFound)
		return
	}
	keyHash, headerOK := n.authorize(r)
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s := &session{n: n, ws: ws, out: make(chan []byte, 4096), done: make(chan struct{}), remote: r.RemoteAddr, keyHash: keyHash, mqtt: true}
	s.serveMQTT(headerOK)
}

func (s *session) serveMQTT(headerOK bool) {
	defer s.close(nil)

	_ = s.ws.SetReadDeadline(time.Now().Add(writeTimeout))
	_, raw, err := s.ws.ReadMessage()
	if err != nil {
		return
	}
	pkt, err := stream.ParseMQTTPacket(raw)
	if err != nil {
		return
	}
	conn, err := stream.DecodeMQTTConnect(pkt)
	if err != nil {
		return
	}
	if !headerOK {
		h, ok := s.n.authorizeKey(conn.Username)
		if !ok {
			h, ok = s.n.authorizeKey(conn.Password)
		}
		if !ok {
			_ = s.ws.WriteMessage(websocket.BinaryMessage, stream.EncodeMQTTConnack(5))
			return
		}
		s.keyHash = h
	}
	if err := s.ws.WriteMessage(websocket.BinaryMessage, stream.EncodeMQTTConnack(0)); err != nil {
		return
	}

	go s.writer()

	var node *Node
	for {
		_ = s.ws.SetReadDeadline(time.Now().Add(readTimeout))
		_, raw, err := s.ws.ReadMessage()
		if err != nil {
			if node != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				s.n.log.Warn("hub: aclk disconnected", "node", node.ID, "err", err)
			}
			return
		}
		pkt, err := stream.ParseMQTTPacket(raw)
		if err != nil {
			continue
		}
		switch pkt.Type {
		case 8: // SUBSCRIBE
			id, _, err := stream.DecodeMQTTSubscribe(pkt)
			if err != nil {
				return
			}
			select {
			case s.out <- stream.EncodeMQTTSuback(id):
			case <-s.done:
				return
			}
		case 12: // PINGREQ
			select {
			case s.out <- stream.EncodeMQTTPingresp():
			case <-s.done:
				return
			}
		case 14: // DISCONNECT
			return
		case 3: // PUBLISH
			_, payload, err := stream.DecodeMQTTPublish(pkt)
			if err != nil {
				continue
			}
			var f stream.Frame
			if json.Unmarshal(payload, &f) != nil {
				continue
			}
			if f.Type == stream.TypeHello {
				now := s.n.opt.Now()
				nd, err := s.attach(f, now)
				if err != nil {
					_ = s.send(stream.Frame{Type: stream.TypeError, Error: err.Error()})
					time.Sleep(100 * time.Millisecond)
					return
				}
				node = nd
				s.sendWelcome(nd, now)
				defer s.detach(nd, now)
				continue
			}
			if node == nil {
				continue
			}
			select {
			case <-s.done:
				return
			default:
			}
			s.handle(node, f)
		}
	}
}

func (s *session) sendWelcome(node *Node, now time.Time) {
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
	_ = s.send(stream.Frame{Type: stream.TypeWelcome, Last: last, ReplicateFrom: now.Unix() - int64(s.n.opt.Replicate.Seconds())})
	if s.n.opt.NodeConfig != nil {
		if disabled := s.n.opt.NodeConfig(node.ID); len(disabled) > 0 {
			_ = s.send(stream.Frame{Type: stream.TypeConfig, Disabled: disabled})
		}
	}
	s.n.log.Info("hub: aclk connected", "node", node.ID, "hostname", node.Host.Hostname, "from", s.remote)
}
