package stream

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

// ClientOptions configures the agent side (`stream:` in monitor.yaml).
type ClientOptions struct {
	// Destinations are tried in order; the first that accepts the connection
	// is used until it fails. "host:port" defaults to ws://host:port/api/v1/stream.
	Destinations       []string
	APIKey             string
	InsecureSkipVerify bool
	Timeout            time.Duration // dial / handshake / write timeout
	// Replicate bounds how much history is re-sent after a (re)connect.
	Replicate time.Duration
	Version   string
	// Functions lists the collector functions the hub may call (optional).
	Functions func() []collect.Function
	Logger    *slog.Logger
}

// ClientStatus is exposed through /api/v1/info.
type ClientStatus struct {
	Enabled     bool   `json:"enabled"`
	Connected   bool   `json:"connected"`
	Destination string `json:"destination,omitempty"`
	Since       int64  `json:"since,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	Sent        uint64 `json:"sent"`
	Replicated  uint64 `json:"replicated"`
	Dropped     uint64 `json:"dropped"`
	Reconnects  uint64 `json:"reconnects"`
}

// Client streams a registry (and its TSDB history) to a hub.
type Client struct {
	reg *registry.Registry
	db  *tsdb.Store
	opt ClientOptions
	log *slog.Logger

	queue chan Frame

	mu        sync.Mutex
	accepting bool // a session is (about to be) live; samples are queued
	st        ClientStatus

	sent, replicated, dropped, reconnects atomic.Uint64
}

// NewClient subscribes to reg; nothing is sent until Run.
func NewClient(reg *registry.Registry, db *tsdb.Store, opt ClientOptions) *Client {
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 10 * time.Second
	}
	if opt.Replicate <= 0 {
		opt.Replicate = time.Hour
	}
	c := &Client{reg: reg, db: db, opt: opt, log: opt.Logger, queue: make(chan Frame, 8192)}
	c.st.Enabled = len(opt.Destinations) > 0
	reg.Subscribe(c.onSample)
	return c
}

func (c *Client) onSample(chartID string, ts int64, values map[string]float64) {
	c.mu.Lock()
	ok := c.accepting
	c.mu.Unlock()
	if !ok {
		return
	}
	select {
	case c.queue <- Frame{Type: TypeData, ChartID: chartID, T: ts, V: values}:
	default:
		c.dropped.Add(1)
	}
}

// PublishAlarm forwards a local alarm transition to the hub (wire it via
// health.Engine.SetOnEvent). Dropped when not connected: the hub only mirrors
// live state, the agent keeps the authoritative log.
func (c *Client) PublishAlarm(e health.LogEntry) {
	c.mu.Lock()
	ok := c.accepting
	c.mu.Unlock()
	if !ok {
		return
	}
	e2 := e
	select {
	case c.queue <- Frame{Type: TypeAlarm, Alarm: &e2}:
	default:
		c.dropped.Add(1)
	}
}

// Status returns a snapshot for the info API.
func (c *Client) Status() ClientStatus {
	c.mu.Lock()
	st := c.st
	c.mu.Unlock()
	st.Sent, st.Replicated, st.Dropped, st.Reconnects = c.sent.Load(), c.replicated.Load(), c.dropped.Load(), c.reconnects.Load()
	return st
}

// Run connects, streams and reconnects with backoff until ctx is done.
func (c *Client) Run(ctx context.Context) {
	if len(c.opt.Destinations) == 0 {
		return
	}
	backoff := time.Second
	for {
		err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			c.mu.Lock()
			c.st.LastError = err.Error()
			c.mu.Unlock()
		}
		c.reconnects.Add(1)
		if err != nil {
			c.log.Warn("stream: disconnected", "err", err, "retry_in", backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < time.Minute {
			backoff *= 2
		}
	}
}

// destURL normalizes a destination to a full WebSocket URL.
func destURL(d string) (string, error) {
	if !strings.Contains(d, "://") {
		d = "ws://" + d
	}
	u, err := url.Parse(d)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("stream destination %q: unsupported scheme", d)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = Path
	}
	return u.String(), nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, string, error) {
	dialer := websocket.Dialer{HandshakeTimeout: c.opt.Timeout, TLSClientConfig: &tls.Config{InsecureSkipVerify: c.opt.InsecureSkipVerify}} //nolint:gosec // operator opt-in
	hdr := http.Header{}
	if c.opt.APIKey != "" {
		hdr.Set("Authorization", "Bearer "+c.opt.APIKey)
	}
	var lastErr error
	for _, d := range c.opt.Destinations {
		u, err := destURL(d)
		if err != nil {
			lastErr = err
			continue
		}
		ws, resp, err := dialer.DialContext(ctx, u, hdr)
		if err != nil {
			if resp != nil {
				err = fmt.Errorf("%w (HTTP %s)", err, resp.Status)
			}
			lastErr = fmt.Errorf("%s: %w", u, err)
			continue
		}
		return ws, u, nil
	}
	return nil, "", lastErr
}

func (c *Client) functions() []FunctionInfo {
	if c.opt.Functions == nil {
		return nil
	}
	var out []FunctionInfo
	for _, f := range c.opt.Functions() {
		out = append(out, FunctionInfo{Name: f.Name, Help: f.Help, Timeout: f.Timeout})
	}
	return out
}

// session runs one connection: hello/welcome handshake, replication of the
// gap since the hub's last sample, then live forwarding until an error.
func (c *Client) session(ctx context.Context) error {
	// Start queueing before dialing so nothing collected during the
	// handshake is lost; drain stale content from the previous session.
	c.mu.Lock()
	c.accepting = true
	c.mu.Unlock()
	for len(c.queue) > 0 {
		<-c.queue
	}
	defer func() {
		c.mu.Lock()
		c.accepting = false
		c.st.Connected, c.st.Since = false, 0
		c.mu.Unlock()
	}()

	ws, dest, err := c.dial(ctx)
	if err != nil {
		return err
	}
	defer ws.Close()
	sess := &clientSession{c: c, ws: ws, out: make(chan []byte, 1024), done: make(chan struct{}), errc: make(chan error, 1), sentDefs: map[string]string{}}
	go sess.writer()
	defer sess.close()

	if err := sess.send(Frame{Type: TypeHello, Host: c.reg.Host, Version: c.opt.Version, Functions: c.functions()}); err != nil {
		return err
	}
	_ = ws.SetReadDeadline(time.Now().Add(c.opt.Timeout))
	var welcome Frame
	if err := ws.ReadJSON(&welcome); err != nil {
		return fmt.Errorf("waiting for welcome: %w", err)
	}
	if welcome.Type == TypeError {
		return fmt.Errorf("hub rejected: %s", welcome.Error)
	}
	if welcome.Type != TypeWelcome {
		return fmt.Errorf("unexpected frame %q before welcome", welcome.Type)
	}
	c.mu.Lock()
	c.st.Connected, c.st.Destination, c.st.Since, c.st.LastError = true, dest, time.Now().Unix(), ""
	c.mu.Unlock()
	c.log.Info("stream: connected", "hub", dest)

	go sess.reader(ctx)

	// Everything up to the previous second comes from the local TSDB; the
	// current second may still be collecting, so it flows through the live
	// queue (the hub drops any duplicate the replay already covered).
	cutoff := time.Now().Unix() - 1
	if err := sess.replicate(welcome, cutoff); err != nil {
		return err
	}

	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-sess.errc:
			return err
		case f := <-c.queue:
			if f.Type == TypeData {
				if f.T <= cutoff { // covered by replication
					continue
				}
				if err := sess.ensureDef(f.ChartID); err != nil {
					return err
				}
			}
			if err := sess.send(f); err != nil {
				return err
			}
			c.sent.Add(1)
		case <-sweep.C:
			if err := sess.sweepDefs(); err != nil {
				return err
			}
		}
	}
}

type clientSession struct {
	c    *Client
	ws   *websocket.Conn
	out  chan []byte
	done chan struct{}
	errc chan error

	defMu    sync.Mutex
	sentDefs map[string]string // chart id → fingerprint
	closed   sync.Once
}

func (s *clientSession) close() {
	s.closed.Do(func() { close(s.done) })
}

func (s *clientSession) send(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	select {
	case s.out <- b:
		return nil
	case <-s.done:
		return errors.New("session closed")
	case <-time.After(s.c.opt.Timeout):
		return errors.New("hub not reading (send queue full)")
	}
}

func (s *clientSession) writer() {
	for {
		select {
		case <-s.done:
			return
		case b := <-s.out:
			_ = s.ws.SetWriteDeadline(time.Now().Add(s.c.opt.Timeout))
			if err := s.ws.WriteMessage(websocket.TextMessage, b); err != nil {
				s.fail(err)
				return
			}
		}
	}
}

func (s *clientSession) fail(err error) {
	select {
	case s.errc <- err:
	default:
	}
}

// reader handles hub → agent frames; the hub pings every 30s, so silence for
// 90s means the connection is dead.
func (s *clientSession) reader(ctx context.Context) {
	s.ws.SetReadLimit(1 << 20)
	refresh := func() error { return s.ws.SetReadDeadline(time.Now().Add(90 * time.Second)) }
	_ = refresh()
	s.ws.SetPingHandler(func(data string) error {
		_ = refresh()
		return s.ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(s.c.opt.Timeout))
	})
	for {
		var f Frame
		if err := s.ws.ReadJSON(&f); err != nil {
			s.fail(err)
			return
		}
		_ = refresh()
		switch f.Type {
		case TypeFuncCall:
			go s.runFunction(ctx, f)
		case TypeError:
			s.fail(fmt.Errorf("hub: %s", f.Error))
			return
		}
	}
}

func (s *clientSession) runFunction(ctx context.Context, call Frame) {
	res := Frame{Type: TypeFuncResult, CallID: call.CallID, Name: call.Name}
	var fn *collect.Function
	if s.c.opt.Functions != nil {
		for _, f := range s.c.opt.Functions() {
			if f.Name == call.Name {
				f := f
				fn = &f
				break
			}
		}
	}
	if fn == nil {
		res.Error = "unknown function"
	} else {
		timeout := time.Duration(fn.Timeout) * time.Second
		if timeout <= 0 {
			timeout = 10 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		out, err := fn.Run(cctx, call.Args)
		cancel()
		if err != nil {
			res.Error = err.Error()
		} else if b, err := json.Marshal(out); err != nil {
			res.Error = err.Error()
		} else {
			res.Result = b
		}
	}
	if err := s.send(res); err != nil {
		s.fail(err)
	}
}

// ensureDef sends the chart definition if the hub has not seen this shape.
func (s *clientSession) ensureDef(chartID string) error {
	ch, ok := s.c.reg.Chart(chartID)
	if !ok {
		return nil
	}
	def := DefOf(ch)
	fp := def.Fingerprint()
	s.defMu.Lock()
	same := s.sentDefs[chartID] == fp
	if !same {
		s.sentDefs[chartID] = fp
	}
	s.defMu.Unlock()
	if same {
		return nil
	}
	return s.send(Frame{Type: TypeChart, Chart: def})
}

// sweepDefs tells the hub about charts that disappeared (containers, disks).
func (s *clientSession) sweepDefs() error {
	have := map[string]bool{}
	for _, ch := range s.c.reg.Charts() {
		have[ch.ID] = true
	}
	var gone []string
	s.defMu.Lock()
	for id := range s.sentDefs {
		if !have[id] {
			gone = append(gone, id)
			delete(s.sentDefs, id)
		}
	}
	s.defMu.Unlock()
	for _, id := range gone {
		if err := s.send(Frame{Type: TypeChartDel, ID: id}); err != nil {
			return err
		}
	}
	return nil
}

// replicate sends every chart definition and, for each chart, the tier0
// samples the hub is missing between its last sample and cutoff.
func (s *clientSession) replicate(welcome Frame, cutoff int64) error {
	from := cutoff - int64(s.c.opt.Replicate.Seconds())
	if welcome.ReplicateFrom > from {
		from = welcome.ReplicateFrom
	}
	var total uint64
	for _, ch := range s.c.reg.Charts() {
		if err := s.ensureDef(ch.ID); err != nil {
			return err
		}
		if s.c.db == nil {
			continue
		}
		after := from
		if last, ok := welcome.Last[ch.ID]; ok && last+1 > after {
			after = last + 1
		}
		if after > cutoff {
			continue
		}
		rows := map[int64]map[string]float64{}
		for _, d := range ch.Dims() {
			pts, err := s.c.db.Query(registry.SeriesID(ch.ID, d.ID), after, cutoff)
			if err != nil {
				return err
			}
			for _, p := range pts {
				r := rows[p.TS]
				if r == nil {
					r = map[string]float64{}
					rows[p.TS] = r
				}
				r[d.ID] = p.Value
			}
		}
		if len(rows) == 0 {
			continue
		}
		ts := make([]int64, 0, len(rows))
		for t := range rows {
			ts = append(ts, t)
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
		for _, t := range ts {
			if err := s.send(Frame{Type: TypeData, ChartID: ch.ID, T: t, V: rows[t], Replay: true}); err != nil {
				return err
			}
			total++
		}
	}
	if total > 0 {
		s.c.replicated.Add(total)
		s.c.log.Info("stream: replicated", "samples", total)
	}
	return nil
}
