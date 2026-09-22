package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
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
	JWKSURL     string `yaml:"jwks_url" json:"-"`
}

type oidcSession struct {
	User    User
	Expires time.Time
}
type oidcPending struct {
	Expires  time.Time
	Verifier string
}

const oidcCookie = "monitor_oidc_state"

type oidcState struct {
	discoveryMu sync.Mutex
	cfg         OIDCConfig
	http        *http.Client

	mu       sync.Mutex
	sessions map[string]oidcSession // minted API token → user
	pending  map[string]oidcPending
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
		sessions: map[string]oidcSession{}, pending: map[string]oidcPending{}}
}

func (o *oidcState) session(tok string) (User, bool) {
	if o == nil || tok == "" {
		return User{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	u, ok := o.sessions[tok]
	if !ok || !time.Now().Before(u.Expires) {
		delete(o.sessions, tok)
		return User{}, false
	}
	return u.User, true
}

func (o *oidcState) discover() error {
	o.discoveryMu.Lock()
	defer o.discoveryMu.Unlock()
	if o.cfg.AuthURL != "" && o.cfg.TokenURL != "" && o.cfg.UserInfoURL != "" && o.cfg.JWKSURL != "" {
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
		Issuer   string `json:"issuer"`
		JWKS     string `json:"jwks_uri"`
		Auth     string `json:"authorization_endpoint"`
		Token    string `json:"token_endpoint"`
		UserInfo string `json:"userinfo_endpoint"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return err
	}
	if doc.Issuer != o.cfg.Issuer || doc.JWKS == "" {
		return fmt.Errorf("oidc discovery issuer or JWKS mismatch")
	}
	if o.cfg.JWKSURL == "" {
		o.cfg.JWKSURL = doc.JWKS
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
	verifier := randomToken() + randomToken()
	challenge := sha256.Sum256([]byte(verifier))
	s.oidc.mu.Lock()
	for key, p := range s.oidc.pending {
		if time.Now().After(p.Expires) {
			delete(s.oidc.pending, key)
		}
	}
	for key, session := range s.oidc.sessions {
		if time.Now().After(session.Expires) {
			delete(s.oidc.sessions, key)
		}
	}
	if len(s.oidc.pending) >= 1024 {
		s.oidc.mu.Unlock()
		http.Error(w, "too many pending logins", http.StatusTooManyRequests)
		return
	}
	s.oidc.pending[state] = oidcPending{Expires: time.Now().Add(10 * time.Minute), Verifier: verifier}
	s.oidc.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: state, Path: "/api/v1/auth/oidc/", HttpOnly: true, Secure: strings.HasPrefix(s.oidc.cfg.RedirectURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: 600})
	q := url.Values{
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"response_type":         {"code"},
		"client_id":             {s.oidc.cfg.ClientID},
		"redirect_uri":          {s.oidc.cfg.RedirectURL},
		"nonce":                 {state},
		"scope":                 {"openid profile email"},
		"state":                 {state},
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
	cookie, cookieErr := r.Cookie(oidcCookie)
	if cookieErr != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		http.Error(w, "invalid oidc browser state", http.StatusBadRequest)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: "/api/v1/auth/oidc/", HttpOnly: true, Secure: strings.HasPrefix(s.oidc.cfg.RedirectURL, "https://"), SameSite: http.SameSiteLaxMode, MaxAge: -1})
	s.oidc.mu.Lock()
	exp, ok := s.oidc.pending[state]
	if ok {
		delete(s.oidc.pending, state)
	}
	s.oidc.mu.Unlock()
	if !ok || time.Now().After(exp.Expires) || code == "" {
		http.Error(w, "invalid oidc state", http.StatusBadRequest)
		return
	}
	if err := s.oidc.discover(); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code_verifier": {exp.Verifier},
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
		IDToken     string `json:"id_token"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok)
	resp.Body.Close()
	if err != nil || tok.AccessToken == "" || resp.StatusCode >= 300 {
		http.Error(w, "oidc token exchange failed", http.StatusBadGateway)
		return
	}
	verifyCtx := oidc.ClientContext(r.Context(), s.oidc.http)
	verifier := oidc.NewVerifier(s.oidc.cfg.Issuer, oidc.NewRemoteKeySet(oidc.ClientContext(context.Background(), s.oidc.http), s.oidc.cfg.JWKSURL), &oidc.Config{ClientID: s.oidc.cfg.ClientID})
	idToken, verifyErr := verifier.Verify(verifyCtx, tok.IDToken)
	if verifyErr != nil || idToken.Subject == "" || subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(state)) != 1 {
		http.Error(w, "invalid oidc ID token", http.StatusUnauthorized)
		return
	}
	req, reqErr := http.NewRequestWithContext(r.Context(), http.MethodGet, s.oidc.cfg.UserInfoURL, nil)
	if reqErr != nil {
		http.Error(w, "invalid oidc userinfo endpoint", http.StatusBadGateway)
		return
	}
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
	if err != nil || status >= 300 || info.Sub != idToken.Subject {
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
	expires := time.Now().Add(8 * time.Hour)
	if idToken.Expiry.Before(expires) {
		expires = idToken.Expiry
	}
	session := randomToken()
	s.oidc.mu.Lock()
	s.oidc.sessions[session] = oidcSession{User: User{Name: name, Token: session, Role: Role(s.oidc.cfg.Role)}, Expires: expires}
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
		panic("secure random source unavailable")
	}
	return hex.EncodeToString(b[:])
}

// Logout revokes the caller's login/share session; static API keys are unaffected.
func (s *Server) handleOIDCLogout(w http.ResponseWriter, r *http.Request) {
	if s.oidc != nil {
		s.oidc.mu.Lock()
		delete(s.oidc.sessions, requestToken(r))
		s.oidc.mu.Unlock()
	}
	if s.shares != nil {
		s.shares.mu.Lock()
		delete(s.shares.toks, requestToken(r))
		s.shares.mu.Unlock()
	}
	w.WriteHeader(http.StatusNoContent)
}
