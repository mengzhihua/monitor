package api

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

func (s *Server) handleManageHealth(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, s.opt.Health.ManageInfo())
		return
	}
	var body struct {
		Enabled     *bool  `json:"enabled"`
		Silent      *bool  `json:"silent"`
		All         *bool  `json:"all"`
		Alarm       string `json:"alarm"`
		Until       int64  `json:"until"`
		Clear       bool   `json:"clear"`
		Maintenance int64  `json:"maintenance"` // unix, 0 clear, <0 seconds from now
		Cmd         string `json:"cmd"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	q := r.URL.Query()
	if q.Get("cmd") != "" {
		body.Cmd = q.Get("cmd")
	}
	if q.Get("alarm") != "" {
		body.Alarm = q.Get("alarm")
	}
	cmd := strings.ToUpper(strings.TrimSpace(body.Cmd))
	switch cmd {
	case "DISABLE ALL", "DISABLE":
		if body.Alarm != "" {
			s.opt.Health.ApplySilence(nil, body.Alarm, body.Until, false)
		} else {
			t := true
			s.opt.Health.ApplySilence(&t, "", body.Until, false)
			s.opt.Health.SetEnabled(false)
		}
	case "ENABLE ALL", "ENABLE":
		if body.Alarm != "" {
			s.opt.Health.ApplySilence(nil, body.Alarm, 0, true)
		} else {
			t := true
			s.opt.Health.ApplySilence(&t, "", 0, true)
			s.opt.Health.SetEnabled(true)
		}
	case "RESET":
		t := true
		s.opt.Health.ApplySilence(&t, "", 0, true)
		s.opt.Health.SetEnabled(true)
		s.opt.Health.SetMaintenanceUntil(0)
	}
	if body.Enabled != nil {
		s.opt.Health.SetEnabled(*body.Enabled)
	}
	if body.Silent != nil || body.All != nil {
		all := body.Silent
		if all == nil {
			all = body.All
		}
		s.opt.Health.ApplySilence(all, body.Alarm, body.Until, body.Clear)
	} else if body.Alarm != "" && cmd == "" {
		s.opt.Health.ApplySilence(nil, body.Alarm, body.Until, body.Clear)
	}
	if q.Get("maintenance") != "" {
		body.Maintenance, _ = strconv.ParseInt(q.Get("maintenance"), 10, 64)
	}
	if r.URL.Query().Has("maintenance") || body.Maintenance != 0 {
		s.opt.Health.SetMaintenanceUntil(body.Maintenance)
	}
	writeJSON(w, s.opt.Health.ManageInfo())
}

func (s *Server) handleAlarmSummary(w http.ResponseWriter, r *http.Request) {
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	if v.node != nil {
		status := map[string]int{}
		for _, e := range v.node.Alarms() {
			status[strings.ToUpper(e.Status.String())]++
		}
		writeJSON(w, map[string]any{"node": v.id, "status": status})
		return
	}
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	out := s.opt.Health.AlarmSummary()
	out["node"] = v.id
	writeJSON(w, out)
}

type shareStore struct {
	mu   sync.Mutex
	toks map[string]shareTok
}

type shareTok struct {
	principal string
	Token     string
	Role      Role
	Node      string
	Name      string
	Until     int64
}

func newShareStore() *shareStore { return &shareStore{toks: map[string]shareTok{}} }

func (s *shareStore) issue(node string, ttl time.Duration) shareTok {
	tok := randomToken()
	until := time.Now().Add(ttl).Unix()
	st := shareTok{Token: tok, Role: RoleViewer, Node: node, Name: "share", Until: until}
	s.mu.Lock()
	s.toks[tok] = st
	s.mu.Unlock()
	return st
}

func (s *shareStore) user(tok string) (User, bool) {
	if s == nil || tok == "" {
		return User{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.toks[tok]
	if !ok || (st.Until > 0 && time.Now().Unix() >= st.Until) {
		if ok {
			delete(s.toks, tok)
		}
		return User{}, false
	}
	return User{Name: st.Name, Role: st.Role, Token: tok, principal: st.principal}, true
}

func (s *shareStore) issueUser(name string, role Role, ttl time.Duration, principal string) shareTok {
	tok := randomToken()
	until := time.Now().Add(ttl).Unix()
	if role == "" {
		role = RoleViewer
	}
	st := shareTok{Token: tok, Role: role, Name: name, Until: until, principal: principal}
	s.mu.Lock()
	s.toks[tok] = st
	s.mu.Unlock()
	return st
}

func (s *Server) handleShare(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		tok := r.URL.Query().Get("token")
		if tok == "" {
			tok = requestToken(r)
		}
		u, ok := s.shares.user(tok)
		if !ok {
			http.Error(w, "unknown share token", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"ok": true, "role": u.Role, "name": u.Name})
		return
	}
	var body struct {
		TTL  string `json:"ttl"`
		Node string `json:"node"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body)
	}
	ttl := 24 * time.Hour
	if body.TTL != "" {
		if d, err := time.ParseDuration(body.TTL); err == nil {
			ttl = d
		}
	}
	st := s.shares.issue(body.Node, ttl)
	writeJSON(w, map[string]any{
		"token": st.Token, "until": st.Until, "role": st.Role, "node": st.Node,
		"url": "/?token=" + st.Token,
	})
}

func (s *Server) handlePushTest(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "channels": []string{"webhook", "push"}, "info": "alarm transitions already fan out to configured notifiers"})
}

// LDAPConfig is web.ldap in monitor.yaml.
type LDAPConfig struct {
	URL      string `yaml:"url" json:"url"`
	UserDN   string `yaml:"user_dn" json:"user_dn"` // uid=%s,ou=people,dc=example
	BindDN   string `yaml:"bind_dn" json:"-"`
	BindPass string `yaml:"bind_password" json:"-"`
	Role     string `yaml:"role" json:"role"`
	// Bind is injected in tests; nil = TCP simple bind.
	Bind func(user, password string) error `json:"-"`
}

func (s *Server) handleLDAP(w http.ResponseWriter, r *http.Request) {
	if s.ldap == nil || (s.ldap.URL == "" && s.ldap.Bind == nil) {
		http.Error(w, "ldap not configured", http.StatusNotFound)
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.User == "" || body.Password == "" {
		http.Error(w, "user and password required", http.StatusBadRequest)
		return
	}
	bind := s.ldap.Bind
	if bind == nil {
		bind = func(user, pass string) error { return ldapSimpleBind(s.ldap, user, pass) }
	}
	if err := bind(body.User, body.Password); err != nil {
		http.Error(w, "ldap bind failed", http.StatusUnauthorized)
		return
	}
	role := s.ldap.Role
	if role == "" {
		role = string(RoleViewer)
	}
	st := s.shares.issueUser(body.User, Role(role), 24*time.Hour, viewPrincipal("ldap", s.ldap.URL, s.ldap.UserDN, body.User))
	writeJSON(w, map[string]any{"token": st.Token, "user": body.User, "role": role, "until": st.Until})
}

func ldapSimpleBind(cfg *LDAPConfig, user, password string) error {
	dn := cfg.UserDN
	if strings.Contains(dn, "%s") {
		dn = fmt.Sprintf(dn, user)
	} else if dn == "" {
		dn = user
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "ldap" && u.Scheme != "ldaps") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("invalid ldap URL")
	}
	port := u.Port()
	if port == "" {
		port = "389"
		if u.Scheme == "ldaps" {
			port = "636"
		}
	}
	host := net.JoinHostPort(u.Hostname(), port)
	var c net.Conn
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	if u.Scheme == "ldaps" {
		c, err = tls.DialWithDialer(dialer, "tcp", host, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()})
	} else {
		c, err = dialer.Dial("tcp", host)
	}
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	msg := encodeLDAPBind(1, dn, password)
	if _, err := c.Write(msg); err != nil {
		return err
	}
	buf := make([]byte, 256)
	n, err := c.Read(buf)
	if err != nil {
		return err
	}
	if n < 8 || buf[0] != 0x30 {
		return fmt.Errorf("ldap: bad response")
	}
	// resultCode is the first INTEGER inside BindResponse; 0 = success.
	if !ldapBindOK(buf[:n]) {
		return fmt.Errorf("ldap: bind rejected")
	}
	return nil
}

func encodeLDAPBind(id int, dn, password string) []byte {
	auth := append([]byte{0x80, byte(len(password))}, []byte(password)...)
	name := append([]byte{0x04, byte(len(dn))}, []byte(dn)...)
	ver := []byte{0x02, 0x01, 0x03}
	bind := append([]byte{0x60, byte(len(ver) + len(name) + len(auth))}, ver...)
	bind = append(bind, name...)
	bind = append(bind, auth...)
	msgid := []byte{0x02, 0x01, byte(id)}
	seq := append(msgid, bind...)
	return append([]byte{0x30, byte(len(seq))}, seq...)
}

func ldapBindOK(b []byte) bool {
	// walk BER for APPLICATION 1 (BindResponse) then INTEGER resultCode == 0
	for i := 0; i < len(b)-2; i++ {
		if b[i] == 0x61 { // BindResponse
			// skip length, look for 0x0a (ENUMERATED) or 0x02 INTEGER
			for j := i + 2; j < len(b)-1 && j < i+16; j++ {
				if (b[j] == 0x0a || b[j] == 0x02) && j+2 < len(b) && b[j+1] == 0x01 {
					return b[j+2] == 0
				}
			}
		}
	}
	return false
}
