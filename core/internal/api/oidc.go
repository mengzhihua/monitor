package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// OIDCConfig is web.oidc in monitor.yaml.
type OIDCConfig struct {
	Issuer       string `yaml:"issuer" json:"issuer"`
	ClientID     string `yaml:"client_id" json:"client_id"`
	ClientSecret string `yaml:"client_secret" json:"-"`
	RedirectURL  string `yaml:"redirect_url" json:"redirect_url"`
	Role         string `yaml:"role" json:"role"`
	// Optional overrides when discovery is skipped (tests).
	AuthURL     string `yaml:"auth_url" json:"-"`
	TokenURL    string `yaml:"token_url" json:"-"`
	UserInfoURL string `yaml:"userinfo_url" json:"-"`
}

type oidcState struct {
	cfg  OIDCConfig
	http *http.Client

	mu       sync.Mutex
	sessions map[string]User // minted API token → user
	pending  map[string]time.Time
}

func newOIDC(cfg *OIDCConfig) *oidcState {
	if cfg == nil || cfg.Issuer == "" && cfg.AuthURL == "" {
		return nil
	}
	role := cfg.Role
	if role == "" {
		role = string(RoleViewer)
	}
	c := *cfg
	c.Role = role
	return &oidcState{cfg: c, http: &http.Client{Timeout: 10 * time.Second},
		sessions: map[string]User{}, pending: map[string]time.Time{}}
}

func (o *oidcState) session(tok string) (User, bool) {
	if o == nil || tok == "" {
		return User{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	u, ok := o.sessions[tok]
	return u, ok
}

func (o *oidcState) discover() error {
	if o.cfg.AuthURL != "" && o.cfg.TokenURL != "" && o.cfg.UserInfoURL != "" {
		return nil
	}
	if o.cfg.Issuer == "" {
		return fmt.Errorf("oidc issuer required")
	}
	u := strings.TrimRight(o.cfg.Issuer, "/") + "/.well-known/openid-configuration"
	resp, err := o.http.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("oidc discovery HTTP %s", resp.Status)
	}
	var doc struct {
		Auth     string `json:"authorization_endpoint"`
		Token    string `json:"token_endpoint"`
		UserInfo string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return err
	}
	if o.cfg.AuthURL == "" {
		o.cfg.AuthURL = doc.Auth
	}
	if o.cfg.TokenURL == "" {
		o.cfg.TokenURL = doc.Token
	}
	if o.cfg.UserInfoURL == "" {
		o.cfg.UserInfoURL = doc.UserInfo
	}
	return nil
}

func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	if err := s.oidc.discover(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	state := randomToken()
	s.oidc.mu.Lock()
	s.oidc.pending[state] = time.Now().Add(10 * time.Minute)
	s.oidc.mu.Unlock()
	q := url.Values{
		"response_type": {"code"},
		"client_id":     {s.oidc.cfg.ClientID},
		"redirect_uri":  {s.oidc.cfg.RedirectURL},
		"scope":         {"openid profile email"},
		"state":         {state},
	}
	http.Redirect(w, r, s.oidc.cfg.AuthURL+"?"+q.Encode(), http.StatusFound)
}

func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.Error(w, "oidc not configured", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	state, code := q.Get("state"), q.Get("code")
	s.oidc.mu.Lock()
	exp, ok := s.oidc.pending[state]
	if ok {
		delete(s.oidc.pending, state)
	}
	s.oidc.mu.Unlock()
	if !ok || time.Now().After(exp) || code == "" {
		http.Error(w, "invalid oidc state", http.StatusBadRequest)
		return
	}
	if err := s.oidc.discover(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.oidc.cfg.RedirectURL},
		"client_id":     {s.oidc.cfg.ClientID},
		"client_secret": {s.oidc.cfg.ClientSecret},
	}
	resp, err := s.oidc.http.Post(s.oidc.cfg.TokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	resp.Body.Close()
	if err != nil || tok.AccessToken == "" || resp.StatusCode >= 300 {
		http.Error(w, "oidc token exchange failed", http.StatusBadGateway)
		return
	}
	req, _ := http.NewRequest(http.MethodGet, s.oidc.cfg.UserInfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	ui, err := s.oidc.http.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	var info struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	err = json.NewDecoder(io.LimitReader(ui.Body, 1<<20)).Decode(&info)
	status := ui.StatusCode
	ui.Body.Close()
	if err != nil || status >= 300 {
		http.Error(w, "oidc userinfo failed", http.StatusBadGateway)
		return
	}
	name := info.Email
	if name == "" {
		name = info.Name
	}
	if name == "" {
		name = info.Sub
	}
	session := randomToken()
	s.oidc.mu.Lock()
	s.oidc.sessions[session] = User{Name: name, Token: session, Role: Role(s.oidc.cfg.Role)}
	s.oidc.mu.Unlock()
	if strings.Contains(r.Header.Get("Accept"), "text/html") {
		http.Redirect(w, r, "/?token="+url.QueryEscape(session), http.StatusFound)
		return
	}
	writeJSON(w, map[string]any{"token": session, "name": name, "role": s.oidc.cfg.Role})
}

func randomToken() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("tok-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}
