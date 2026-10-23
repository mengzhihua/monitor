// Package trapper accepts the Zabbix sender protocol (ZBXD\x01 + JSON) and
// stores each item on a trapper.<host>.<key> chart.
package trapper

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const maxPayload = 1 << 20

var header = []byte{'Z', 'B', 'X', 'D', 1}

// Result is the sender response info line.
type Result struct {
	Processed int
	Failed    int
	Total     int
}

// Start listens on addr until ctx is done. allowed empty means every source.
// psk, when set, must match the JSON psk field. The returned listener owns its
// accepted connections: Close stops intake, closes connections and waits for
// all handlers to finish before returning. Repeated/concurrent Close is safe.
func Start(ctxDone <-chan struct{}, addr string, allowedCIDR []string, psk string, reg *registry.Registry) (net.Listener, error) {
	allowed, err := parseNets(allowedCIDR)
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s := &server{Listener: ln, connections: make(map[net.Conn]struct{}),
		stop: make(chan struct{}), done: make(chan struct{}), watchDone: make(chan struct{})}
	go s.serve(allowed, psk, reg)
	go func() {
		defer close(s.watchDone)
		select {
		case <-ctxDone:
			s.shutdown()
		case <-s.stop:
		}
	}()
	return s, nil
}

type server struct {
	net.Listener
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	handlers    sync.WaitGroup
	closeOnce   sync.Once
	closeErr    error
	stop        chan struct{}
	done        chan struct{}
	watchDone   chan struct{}
}

// shutdown is shared by cancellation, accept failure and Close. It never waits:
// the accept loop and cancellation watcher must not try to join themselves.
func (s *server) shutdown() {
	s.closeOnce.Do(func() {
		close(s.stop)
		s.closeErr = s.Listener.Close()
		s.mu.Lock()
		for conn := range s.connections {
			_ = conn.Close()
		}
		s.mu.Unlock()
	})
}

func (s *server) Close() error {
	s.shutdown()
	<-s.done
	<-s.watchDone
	return s.closeErr
}

func (s *server) serve(allowed []net.IPNet, psk string, reg *registry.Registry) {
	defer func() {
		s.shutdown()
		// Only this accept loop adds handlers, so no Add can race this Wait.
		s.handlers.Wait()
		close(s.done)
	}()
	for {
		conn, err := s.Listener.Accept()
		if err != nil {
			return
		}
		if !allowedIP(conn.RemoteAddr(), allowed) || !s.track(conn) {
			_ = conn.Close()
			continue
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			defer func() {
				_ = conn.Close()
				s.mu.Lock()
				delete(s.connections, conn)
				s.mu.Unlock()
			}()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			_ = Handle(conn, reg, psk)
		}()
	}
}

func (s *server) track(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.stop:
		return false
	default:
		s.connections[conn] = struct{}{}
		return true
	}
}

func parseNets(cidrs []string) ([]net.IPNet, error) {
	var out []net.IPNet
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			if strings.Contains(c, ":") {
				c += "/128"
			} else {
				c += "/32"
			}
		}
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, nil
}

func allowedIP(addr net.Addr, allowed []net.IPNet) bool {
	if len(allowed) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range allowed {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// Handle reads one sender request from r and writes the response.
func Handle(conn io.ReadWriter, reg *registry.Registry, psk string) error {
	br := bufio.NewReader(conn)
	magic := make([]byte, 5)
	if _, err := io.ReadFull(br, magic); err != nil {
		return err
	}
	if string(magic) != string(header) {
		return errors.New("trapper: bad header")
	}
	var nbuf [8]byte
	if _, err := io.ReadFull(br, nbuf[:]); err != nil {
		return err
	}
	n := binary.LittleEndian.Uint64(nbuf[:])
	if n == 0 || n > maxPayload {
		return errors.New("trapper: bad length")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(br, body); err != nil {
		return err
	}
	var req struct {
		Request string `json:"request"`
		PSK     string `json:"psk"`
		Data    []struct {
			Host  string `json:"host"`
			Key   string `json:"key"`
			Value string `json:"value"`
			Clock int64  `json:"clock"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return err
	}
	res := Result{Total: len(req.Data)}
	if req.Request != "sender data" || (psk != "" && req.PSK != psk) {
		res.Failed = res.Total
		return writeResult(conn, res)
	}
	now := time.Now()
	for _, item := range req.Data {
		v, err := strconv.ParseFloat(strings.TrimSpace(item.Value), 64)
		if err != nil || !validToken(item.Host) || !validToken(item.Key) || reg == nil {
			res.Failed++
			continue
		}
		id := "trapper." + sanitize(item.Host) + "." + sanitize(item.Key)
		if _, ok := reg.Chart(id); !ok {
			reg.AddChart(&registry.Chart{ID: id, Context: "trapper.item", Title: item.Host + " " + item.Key, Units: "value",
				Family: "trapper", Plugin: "trapper", Module: "trapper", Priority: 56800,
				Labels:     map[string]string{"host": item.Host, "key": item.Key},
				Dimensions: []*registry.Dimension{{ID: "value"}}})
		}
		at := now
		if item.Clock > 0 {
			at = time.Unix(item.Clock, 0)
		}
		if err := reg.Collect(id, at, map[string]float64{"value": v}); err != nil {
			res.Failed++
			continue
		}
		res.Processed++
	}
	return writeResult(conn, res)
}

func writeResult(w io.Writer, res Result) error {
	info := "processed: " + strconv.Itoa(res.Processed) + "; failed: " + strconv.Itoa(res.Failed) + "; total: " + strconv.Itoa(res.Total) + "; seconds spent: 0.000000"
	resp := "success"
	if res.Processed == 0 && res.Total > 0 {
		resp = "failed"
	}
	body, err := json.Marshal(map[string]string{"response": resp, "info": info})
	if err != nil {
		return err
	}
	var nbuf [8]byte
	binary.LittleEndian.PutUint64(nbuf[:], uint64(len(body)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if _, err := w.Write(nbuf[:]); err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func validToken(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' && r != '.' {
			return false
		}
	}
	return true
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, s)
}
