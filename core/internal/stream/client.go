package stream

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
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
	// ConfigPath is this agent's monitor.yaml; when set the client reports
	// the file content to the hub after every (re)connect.
	ConfigPath string
	// OnConfigFile validates and applies a hub-pushed full config
	// replacement. A nil error means the file was written and the process
	// should restart into it.
	OnConfigFile func(yamlText string, rev int64) error
	Timeout      time.Duration // dial / handshake / write timeout
	// Replicate bounds how much history is re-sent after a (re)connect.
	Replicate time.Duration
	Version   string
	// Protocol is "mqtt" / "aclk" for MQTT-over-WebSocket (PathACLK), or
	// empty/"stream"/"json" for the JSON Frame path (Path).
	Protocol string
	// Functions lists the collector functions the hub may call (optional).
	Functions func() []collect.Function
	// Alarms returns the current alarm state; it is sent as a snapshot after
	// every (re)connect so transitions that happened offline reach the hub.
	Alarms func() []health.Alarm
	Logger *slog.Logger
	// ClaimToken is redeemed once against POST /api/v1/claim for a stream API key.
	ClaimToken string
	// OnConfig applies a hub-pushed overlay (disabled collector names).
	OnConfig func(disabled []string)
}

// ClientStatus is exposed through /api/v1/info.
type ClientStatus struct {
	Enabled        bool     `json:"enabled"`
	Connected      bool     `json:"connected"`
	Destination    string   `json:"destination,omitempty"`
	Since          int64    `json:"since,omitempty"`
	LastError      string   `json:"last_error,omitempty"`
	Sent           uint64   `json:"sent"`
	Replicated     uint64   `json:"replicated"`
	Dropped        uint64   `json:"dropped"`
	Reconnects     uint64   `json:"reconnects"`
	Protocol       string   `json:"protocol,omitempty"`
	Claimed        bool     `json:"claimed,omitempty"`
	NextConnection int64    `json:"next_connection,omitempty"`
	Capabilities   []string `json:"capabilities,omitempty"`
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
	c.st.Protocol = c.protocolName()
	c.st.Claimed = opt.ClaimToken != "" || opt.APIKey != ""
	c.st.Capabilities = append([]string{}, ACLKCapabilities...)
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
		connected, err := c.session(ctx)
		if ctx.Err() != nil {
			return
		}
		if connected {
			backoff = time.Second // only consecutive failures grow the delay
		}
		if err != nil {
			c.mu.Lock()
			c.st.LastError = err.Error()
			c.st.NextConnection = time.Now().Add(backoff).Unix()
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

func (c *Client) useMQTT() bool {
	p := strings.ToLower(strings.TrimSpace(c.opt.Protocol))
	return p == "mqtt" || p == "aclk"
}

func (c *Client) protocolName() string {
	if c.useMQTT() {
		return "mqtt"
	}
	return "stream"
}

func (c *Client) defaultPath() string {
	if c.useMQTT() {
		return PathACLK
	}
	return Path
}

// destURL normalizes a destination to a full WebSocket URL.
func destURL(d string) (string, error) {
	return destURLPath(d, Path)
}

func destURLPath(d, path string) (string, error) {
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
		u.Path = path
	}
	return u.String(), nil
}

func (c *Client) dial(ctx context.Context) (*websocket.Conn, string, error) {
	dialer := websocket.Dialer{HandshakeTimeout: c.opt.Timeout, TLSClientConfig: &tls.Config{InsecureSkipVerify: c.opt.InsecureSkipVerify}} //nolint:gosec // operator opt-in
	var lastErr error
	for _, d := range c.opt.Destinations {
		if c.opt.ClaimToken != "" && c.opt.APIKey == "" {
			if err := c.redeemClaim(ctx, d); err != nil {
				lastErr = err
				continue
			}
		}
		hdr := http.Header{}
		if c.opt.APIKey != "" {
			hdr.Set("Authorization", "Bearer "+c.opt.APIKey)
		}
		u, err := destURLPath(d, c.defaultPath())
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

func httpOrigin(dest string) (string, error) {
	u, err := destURL(dest)
	if err != nil {
		return "", err
	}
	p, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	switch p.Scheme {
	case "ws":
		p.Scheme = "http"
	case "wss":
		p.Scheme = "https"
	}
	p.Path = ""
	p.RawQuery = ""
	return strings.TrimRight(p.String(), "/"), nil
}

func (c *Client) redeemClaim(ctx context.Context, dest string) error {
	base, err := httpOrigin(dest)
	if err != nil {
		return err
	}
	nodeID := ""
	if c.reg != nil && c.reg.Host != nil {
		nodeID = c.reg.Host.ID
		if nodeID == "" {
			nodeID = c.reg.Host.Hostname
		}
	}
	payload, _ := json.Marshal(map[string]string{"token": c.opt.ClaimToken, "node_id": nodeID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/claim", strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	cli := &http.Client{Timeout: c.opt.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: c.opt.InsecureSkipVerify}}} //nolint:gosec
	resp, err := cli.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("claim: HTTP %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		APIKey string `json:"api_key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return err
	}
	if out.APIKey == "" {
		return errors.New("claim: empty api_key")
	}
	c.opt.APIKey = out.APIKey
	return nil
}

// fetchConfig pulls the hub's desired node config over HTTP and applies it:
// a non-empty yaml is a full replacement handled by OnConfigFile (acked with a
// config_state frame over sess), otherwise Disabled is the hot overlay.
func (c *Client) fetchConfig(ctx context.Context, dest string, sess *clientSession) {
	if (c.opt.OnConfig == nil && c.opt.OnConfigFile == nil) || c.opt.APIKey == "" {
		return
	}
	base, err := httpOrigin(dest)
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/api/v1/agent/config", nil)
	if err != nil {
		return
	}
	req.Header.Set("Authorization", "Bearer "+c.opt.APIKey)
	cli := &http.Client{Timeout: c.opt.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: c.opt.InsecureSkipVerify}}} //nolint:gosec
	resp, err := cli.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return
	}
	var cfg struct {
		Disabled []string `json:"disabled"`
		YAML     string   `json:"yaml"`
		Updated  int64    `json:"updated"`
	}
	if json.NewDecoder(resp.Body).Decode(&cfg) != nil {
		return
	}
	if cfg.YAML != "" && c.opt.OnConfigFile != nil {
		c.applyPushed(sess, cfg.YAML, cfg.Updated)
		return
	}
	if c.opt.OnConfig != nil {
		c.opt.OnConfig(cfg.Disabled)
	}
}

// applyPushed applies a hub-pushed full config replacement through
// OnConfigFile and acks the outcome with a config_state frame. A failed ack
// send is only logged: the hub re-pushes on the next connect anyway.
func (c *Client) applyPushed(sess *clientSession, yamlText string, rev int64) {
	if c.opt.OnConfigFile == nil {
		return
	}
	state, msg := "applied", ""
	if err := c.opt.OnConfigFile(yamlText, rev); err != nil {
		state, msg = "rejected", err.Error()
		// The stream package cannot import cmd/monitord (where
		// ErrApplyDeferred lives), so deferral is signalled by convention:
		// an error whose message contains "deferred" means the new config
		// was staged and takes effect on the next restart, not rejected.
		if strings.Contains(msg, "deferred") {
			state = "deferred"
		}
	}
	if err := sess.send(Frame{Type: TypeConfigState, ConfigRev: rev, ApplyState: state, ApplyError: msg}); err != nil {
		c.log.Debug("stream: config apply ack failed", "err", err, "rev", rev)
	}
}

// maxConfigReportSize bounds the monitor.yaml content reported to the hub; a
// file larger than this is almost certainly not a config file.
const maxConfigReportSize = 256 * 1024

// reportConfig sends the agent's live monitor.yaml to the hub right after a
// successful connect so the hub can display and diff it against the desired
// state.
func (c *Client) reportConfig(sess *clientSession) {
	if c.opt.ConfigPath == "" {
		return
	}
	b, err := os.ReadFile(c.opt.ConfigPath)
	if err != nil {
		c.log.Debug("stream: config report read failed", "err", err, "path", c.opt.ConfigPath)
		return
	}
	if len(b) > maxConfigReportSize {
		c.log.Warn("stream: config file too large to report", "path", c.opt.ConfigPath, "size", len(b))
		return
	}
	if err := sess.send(Frame{Type: TypeConfigState, ConfigYAML: string(b), ConfigPath: c.opt.ConfigPath}); err != nil {
		c.log.Debug("stream: config report send failed", "err", err)
	}
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
// connected reports whether the welcome handshake completed.
func (c *Client) session(ctx context.Context) (connected bool, err error) {
	// Drop whatever the previous session left behind (it is covered by
	// replication), then start queueing before dialing so nothing collected
	// during the handshake is lost. Producers only enqueue while accepting,
	// so draining first cannot discard samples of this session.
	for len(c.queue) > 0 {
		<-c.queue
	}
	c.mu.Lock()
	c.accepting = true
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.accepting = false
		c.st.Connected, c.st.Since = false, 0
		c.mu.Unlock()
	}()

	ws, dest, err := c.dial(ctx)
	if err != nil {
		return false, err
	}
	defer ws.Close()
	sess := &clientSession{c: c, ws: ws, out: make(chan []byte, 1024), done: make(chan struct{}), errc: make(chan error, 1), sentDefs: map[string]string{}}
	defer sess.close()

	if c.useMQTT() {
		if err := sess.mqttHandshake(); err != nil {
			return false, err
		}
	}
	go sess.writer()

	if err := sess.send(Frame{Type: TypeHello, Host: c.reg.Host, Version: c.opt.Version, Functions: c.functions(),
		Capabilities: ACLKCapabilities, Protocol: c.protocolName(), Claimed: c.opt.ClaimToken != "" || c.opt.APIKey != ""}); err != nil {
		return false, err
	}
	_ = ws.SetReadDeadline(time.Now().Add(c.opt.Timeout))
	welcome, err := sess.readFrame()
	if err != nil {
		return false, fmt.Errorf("waiting for welcome: %w", err)
	}
	if welcome.Type == TypeError {
		return false, fmt.Errorf("hub rejected: %s", welcome.Error)
	}
	if welcome.Type != TypeWelcome {
		return false, fmt.Errorf("unexpected frame %q before welcome", welcome.Type)
	}
	c.mu.Lock()
	c.st.Connected, c.st.Destination, c.st.Since, c.st.LastError = true, dest, time.Now().Unix(), ""
	c.st.Protocol = c.protocolName()
	c.st.Claimed = c.opt.ClaimToken != "" || c.opt.APIKey != ""
	c.st.NextConnection = 0
	c.mu.Unlock()
	c.log.Info("stream: connected", "hub", dest)
	c.fetchConfig(ctx, dest, sess)
	c.reportConfig(sess)

	go sess.reader(ctx)

	// Everything up to the previous second comes from the local TSDB; the
	// current second may still be collecting, so it flows through the live
	// queue (the hub drops any duplicate the replay already covered).
	cutoff := time.Now().Unix() - 1
	if err := sess.replicate(welcome, cutoff); err != nil {
		return true, err
	}
	if err := sess.sendAlarmSnapshot(); err != nil {
		return true, err
	}

	sweep := time.NewTicker(30 * time.Second)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return true, nil
		case err := <-sess.errc:
			return true, err
		case f := <-c.queue:
			if f.Type == TypeData {
				if f.T <= cutoff { // covered by replication
					continue
				}
				if err := sess.ensureDef(f.ChartID); err != nil {
					return true, err
				}
			}
			if err := sess.send(f); err != nil {
				return true, err
			}
			c.sent.Add(1)
		case <-sweep.C:
			if err := sess.sweepDefs(); err != nil {
				return true, err
			}
		}
	}
}

// sendAlarmSnapshot mirrors the agent's whole alarm state; the hub replaces
// what it holds for this node, so alarms that changed (or vanished) while
// disconnected converge.
func (s *clientSession) sendAlarmSnapshot() error {
	if s.c.opt.Alarms == nil {
		return nil
	}
	f := Frame{Type: TypeAlarms, Alarms: []health.LogEntry{}}
	for _, a := range s.c.opt.Alarms() {
		f.Alarms = append(f.Alarms, SnapshotEntry(a, s.c.reg.Host.Hostname))
	}
	return s.send(f)
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

func (s *clientSession) mqttHandshake() error {
	cid := ""
	if s.c.reg != nil && s.c.reg.Host != nil {
		cid = s.c.reg.Host.ID
		if cid == "" {
			cid = s.c.reg.Host.Hostname
		}
	}
	if cid == "" {
		cid = "monitord"
	}
	_ = s.ws.SetWriteDeadline(time.Now().Add(s.c.opt.Timeout))
	if err := s.ws.WriteMessage(websocket.BinaryMessage, EncodeMQTTConnect(MQTTConnect{
		ClientID: cid, Username: s.c.opt.APIKey, Password: s.c.opt.APIKey, KeepAlive: 60,
	})); err != nil {
		return err
	}
	_ = s.ws.SetReadDeadline(time.Now().Add(s.c.opt.Timeout))
	_, raw, err := s.ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("mqtt connack: %w", err)
	}
	pkt, err := ParseMQTTPacket(raw)
	if err != nil {
		return err
	}
	if pkt.Type != mqttConnack || len(pkt.Payload) < 2 || pkt.Payload[1] != 0 {
		return fmt.Errorf("mqtt: connect refused")
	}
	_ = s.ws.SetWriteDeadline(time.Now().Add(s.c.opt.Timeout))
	if err := s.ws.WriteMessage(websocket.BinaryMessage, EncodeMQTTSubscribe(1, "agent/+/from-cloud")); err != nil {
		return err
	}
	_ = s.ws.SetReadDeadline(time.Now().Add(s.c.opt.Timeout))
	_, raw, err = s.ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("mqtt suback: %w", err)
	}
	ack, err := ParseMQTTPacket(raw)
	if err != nil {
		return err
	}
	if ack.Type != mqttSuback {
		return fmt.Errorf("mqtt: expected SUBACK, got %d", ack.Type)
	}
	return nil
}

func (s *clientSession) pubTopic() string {
	id := "agent"
	if s.c.reg != nil && s.c.reg.Host != nil {
		id = s.c.reg.Host.ID
		if id == "" {
			id = s.c.reg.Host.Hostname
		}
	}
	return "agent/" + id + "/from-agent"
}

func (s *clientSession) send(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil {
		return err
	}
	if s.c.useMQTT() {
		b = EncodeMQTTPublish(s.pubTopic(), b)
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
	mt := websocket.TextMessage
	if s.c.useMQTT() {
		mt = websocket.BinaryMessage
	}
	for {
		select {
		case <-s.done:
			return
		case b := <-s.out:
			_ = s.ws.SetWriteDeadline(time.Now().Add(s.c.opt.Timeout))
			if err := s.ws.WriteMessage(mt, b); err != nil {
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
func (s *clientSession) readFrame() (Frame, error) {
	if !s.c.useMQTT() {
		var f Frame
		if err := s.ws.ReadJSON(&f); err != nil {
			return Frame{}, err
		}
		return f, nil
	}
	for {
		_, raw, err := s.ws.ReadMessage()
		if err != nil {
			return Frame{}, err
		}
		pkt, err := ParseMQTTPacket(raw)
		if err != nil {
			return Frame{}, err
		}
		switch pkt.Type {
		case mqttPingreq:
			select {
			case s.out <- EncodeMQTTPingresp():
			case <-s.done:
				return Frame{}, errors.New("session closed")
			}
			continue
		case mqttPingresp, mqttSuback, mqttConnack:
			continue
		case mqttPublish:
			_, payload, err := DecodeMQTTPublish(pkt)
			if err != nil {
				return Frame{}, err
			}
			var f Frame
			if err := json.Unmarshal(payload, &f); err != nil {
				return Frame{}, err
			}
			return f, nil
		case mqttDisconnect:
			return Frame{}, errors.New("mqtt disconnect")
		default:
			continue
		}
	}
}

func (s *clientSession) reader(ctx context.Context) {
	s.ws.SetReadLimit(1 << 20)
	refresh := func() error { return s.ws.SetReadDeadline(time.Now().Add(90 * time.Second)) }
	_ = refresh()
	s.ws.SetPingHandler(func(data string) error {
		_ = refresh()
		return s.ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(s.c.opt.Timeout))
	})
	for {
		f, err := s.readFrame()
		if err != nil {
			s.fail(err)
			return
		}
		_ = refresh()
		switch f.Type {
		case TypeFuncCall:
			go s.runFunction(ctx, f)
		case TypeQuery:
			go s.runQuery(f)
		case TypeConfig:
			if f.ConfigYAML != "" && s.c.opt.OnConfigFile != nil {
				s.c.applyPushed(s, f.ConfigYAML, f.ConfigRev)
			} else if s.c.opt.OnConfig != nil {
				s.c.opt.OnConfig(f.Disabled)
			}
		case TypeError:
			s.fail(fmt.Errorf("hub: %s", f.Error))
			return
		}
	}
}

func (s *clientSession) runQuery(f Frame) {
	res := Frame{Type: TypeQueryResult, CallID: f.CallID, Name: f.Name}
	chartID := ""
	if f.Args != nil {
		chartID = f.Args["chart"]
	}
	if chartID == "" {
		chartID = f.ChartID
	}
	ch, ok := s.c.reg.Chart(chartID)
	if !ok || s.c.db == nil {
		res.Error = "chart not found"
		_ = s.send(res)
		return
	}
	now := time.Now().Unix()
	before := now
	after := now - 60
	if f.Args != nil {
		if n, err := strconv.ParseInt(f.Args["after"], 10, 64); err == nil && n != 0 {
			if n < 0 {
				after = before + n
			} else {
				after = n
			}
		}
		if n, err := strconv.ParseInt(f.Args["before"], 10, 64); err == nil && n > 0 {
			before = n
		}
	}
	dimIDs := make([]string, 0, len(ch.Dims()))
	rows := map[int64]map[string]float64{}
	for _, d := range ch.Dims() {
		dimIDs = append(dimIDs, d.ID)
		pts, err := s.c.db.Query(registry.SeriesID(ch.ID, d.ID), after, before)
		if err != nil {
			continue
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
	ts := make([]int64, 0, len(rows))
	for t := range rows {
		ts = append(ts, t)
	}
	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	data := make([][]any, 0, len(ts))
	for _, t := range ts {
		row := make([]any, 1, 1+len(dimIDs))
		row[0] = t
		for _, id := range dimIDs {
			if v, ok := rows[t][id]; ok {
				row = append(row, v)
			} else {
				row = append(row, nil)
			}
		}
		data = append(data, row)
	}
	b, _ := json.Marshal(map[string]any{"id": ch.ID, "dimension_ids": dimIDs, "result": map[string]any{"data": data}})
	res.Result = b
	if err := s.send(res); err != nil {
		s.fail(err)
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
