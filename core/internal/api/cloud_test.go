package api

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/stream"
	"github.com/mengzhihua/monitor/core/internal/tsdb"
)

func newHubOrg(t *testing.T) (*httptest.Server, *hub.Nodes, *hub.Org) {
	t.Helper()
	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	org, err := hub.OpenOrg(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sp, _ := org.CreateSpace("prod")
	_, _ = org.CreateRoom(sp.ID, "edge")
	var srv *Server
	nodes, err := hub.Open(db, t.TempDir(), hub.Options{Keys: []string{testKey}, ExtraKeys: org.Keys,
		OnSample: func(n, c string, ts int64, v map[string]float64) { srv.PublishNodeSample(n, c, ts, v) }})
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{ID: "hub-id", Hostname: "hub", OS: "linux", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	srv, err = New(reg, db, sched, Options{Mode: "hub", Nodes: nodes, Org: org, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts, nodes, org
}

func TestHubClaimAndConfig(t *testing.T) {
	hs, _, org := newHubOrg(t)
	spaces := org.Spaces()
	rooms := org.Rooms(spaces[0].ID)

	resp, err := http.Post(hs.URL+"/api/v1/hub/claim-tokens", "application/json",
		strings.NewReader(fmt.Sprintf(`{"space_id":%q,"room_id":%q,"ttl":"1h"}`, spaces[0].ID, rooms[0].ID)))
	if err != nil {
		t.Fatal(err)
	}
	var cl hub.Claim
	if err := json.NewDecoder(resp.Body).Decode(&cl); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cl.Token == "" {
		t.Fatalf("claim = %+v", cl)
	}

	resp, err = http.Post(hs.URL+"/api/v1/claim", "application/json",
		strings.NewReader(fmt.Sprintf(`{"token":%q,"node_id":"box-1"}`, cl.Token)))
	if err != nil {
		t.Fatal(err)
	}
	var redeemed struct {
		APIKey  string `json:"api_key"`
		NodeID  string `json:"node_id"`
		SpaceID string `json:"space_id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&redeemed)
	resp.Body.Close()
	if redeemed.APIKey == "" || redeemed.NodeID != "box-1" {
		t.Fatalf("redeem = %+v status=%d", redeemed, resp.StatusCode)
	}

	body, _ := json.Marshal(hub.NodeConfig{NodeID: "box-1", Disabled: []string{"nvidia"}})
	req, _ := http.NewRequest(http.MethodPut, hs.URL+"/api/v1/hub/config?node=box-1", bytes.NewReader(body))
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("put config %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodGet, hs.URL+"/api/v1/agent/config", nil)
	req.Header.Set("Authorization", "Bearer "+redeemed.APIKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var cfg hub.NodeConfig
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if len(cfg.Disabled) != 1 || cfg.Disabled[0] != "nvidia" {
		t.Fatalf("agent config = %+v", cfg)
	}
}

func TestHubRingReplica(t *testing.T) {
	hs, nodes, _ := newHubOrg(t)
	payload := hub.RingPayload{
		Host: registry.Host{ID: "peer-agent", Hostname: "peer-agent", OS: "linux", UpdateEvery: 1},
		Charts: []*stream.ChartDef{{ID: "system.ram", Context: "system.ram", Units: "MiB",
			Dimensions: []stream.DimDef{{ID: "used"}, {ID: "free"}}}},
		Samples: []hub.ReplicaSample{{Chart: "system.ram", T: time.Now().Unix(), V: map[string]float64{"used": 11, "free": 22}}},
	}
	b, _ := json.Marshal(payload)
	resp, err := http.Post(hs.URL+"/api/v1/hub/ring", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("ring %d %s", resp.StatusCode, body)
	}
	n, ok := nodes.Get("peer-agent")
	if !ok {
		t.Fatal("replica node missing")
	}
	inf := n.Info(time.Now())
	if !inf.Replica || inf.ChartsCount != 1 {
		t.Fatalf("info = %+v", inf)
	}
}

func TestOIDCLogin(t *testing.T) {
	for _, mode := range []string{"valid", "issuer", "audience", "expired", "nonce", "signature", "subject", "missing"} {
		t.Run(mode, func(t *testing.T) { testOIDCLogin(t, mode) })
	}
}
func testOIDCLogin(t *testing.T, mode string) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var nonce, challenge string
	idp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 "http://" + r.Host,
				"jwks_uri":               "http://" + r.Host + "/keys",
				"authorization_endpoint": "http://" + r.Host + "/authorize",
				"token_endpoint":         "http://" + r.Host + "/token",
				"userinfo_endpoint":      "http://" + r.Host + "/userinfo",
			})
		case "/keys":
			_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			nonce = r.URL.Query().Get("nonce")
			challenge = r.URL.Query().Get("code_challenge")
			http.Redirect(w, r, r.URL.Query().Get("redirect_uri")+"?code=abc&state="+r.URL.Query().Get("state"), http.StatusFound)
		case "/token":
			digest := sha256.Sum256([]byte(r.FormValue("code_verifier")))
			if r.FormValue("code_verifier") == "" || base64.RawURLEncoding.EncodeToString(digest[:]) != challenge {
				http.Error(w, "PKCE missing", 400)
				return
			}
			claims := map[string]any{"iss": "http://" + r.Host, "sub": "u1", "aud": "cid", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "nonce": nonce}
			switch mode {
			case "issuer":
				claims["iss"] = "https://other.example"
			case "audience":
				claims["aud"] = "other"
			case "expired":
				claims["exp"] = time.Now().Add(-time.Hour).Unix()
			case "nonce":
				claims["nonce"] = "wrong"
			case "subject":
				claims["sub"] = "other"
			}
			signing := signer
			if mode == "signature" {
				other, _ := rsa.GenerateKey(rand.Reader, 2048)
				signing, _ = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: other}, nil)
			}
			raw, _ := jwt.Signed(signing).Claims(claims).Serialize()
			if mode == "missing" {
				raw = ""
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "at-1", "id_token": raw})
		case "/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": "u1", "email": "ops@example.com"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(idp.Close)

	db, err := tsdb.Open(tsdb.Options{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	reg := registry.New(&registry.Host{ID: "id", Hostname: "h", UpdateEvery: 1}, db)
	sched := collect.NewScheduler(reg, nil, collect.Options{Names: []string{"none"}})
	srv, err := New(reg, db, sched, Options{StartedAt: time.Now(),
		OIDC: &OIDCConfig{Issuer: idp.URL, ClientID: "cid", ClientSecret: "sec",
			RedirectURL: "http://hub.example/api/v1/auth/oidc/callback", Role: "viewer"}})
	if err != nil {
		t.Fatal(err)
	}
	// rewrite redirect_url to the test server after start
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	srv.oidc.cfg.RedirectURL = ts.URL + "/api/v1/auth/oidc/callback"

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(ts.URL + "/api/v1/auth/oidc/login")
	if err != nil {
		t.Fatal(err)
	}
	loc := resp.Header.Get("Location")
	resp.Body.Close()
	if loc == "" {
		t.Fatal("no authorize redirect")
	}
	resp, err = client.Get(loc)
	if err != nil {
		t.Fatal(err)
	}
	cb := resp.Header.Get("Location")
	resp.Body.Close()
	if cb == "" {
		t.Fatal("idp did not bounce to callback")
	}
	resp, err = client.Get(cb)
	if err != nil {
		t.Fatal(err)
	}
	if mode != "valid" {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized && resp.StatusCode != http.StatusBadGateway {
			t.Fatalf("invalid %s accepted: %d", mode, resp.StatusCode)
		}
		if len(srv.oidc.sessions) != 0 {
			t.Fatal("invalid login minted a session")
		}
		return
	}
	var out struct {
		Token string `json:"token"`
		Name  string `json:"name"`
		Role  string `json:"role"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	resp.Body.Close()
	if out.Token == "" || out.Name != "ops@example.com" || out.Role != "viewer" {
		t.Fatalf("oidc session = %+v", out)
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/info", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session info %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/oidc/logout", nil)
	req.Header.Set("Authorization", "Bearer "+out.Token)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("logout %d", resp.StatusCode)
	}
	if _, ok := srv.oidc.session(out.Token); ok {
		t.Fatal("logout did not revoke session")
	}
}

func TestHubConsole(t *testing.T) {
	hs, _, org := newHubOrg(t)
	spaces := org.Spaces()
	if len(spaces) == 0 {
		t.Fatal("no spaces")
	}
	var cons struct {
		Spaces []struct {
			Name  string `json:"name"`
			Rooms []struct {
				Name  string `json:"name"`
				Nodes []any  `json:"nodes"`
			} `json:"rooms"`
		} `json:"spaces"`
		ACLK struct {
			Available bool   `json:"available"`
			Protocol  string `json:"protocol"`
		} `json:"aclk"`
		Routing struct {
			Channels []any `json:"channels"`
		} `json:"routing"`
	}
	getJSON(t, hs.URL+"/api/v1/hub/console", &cons)
	if !cons.ACLK.Available || cons.ACLK.Protocol != "stream+mqtt" || len(cons.Spaces) == 0 || cons.Spaces[0].Name != "prod" {
		t.Fatalf("console = %+v", cons)
	}
	if len(cons.Spaces[0].Rooms) == 0 || cons.Spaces[0].Rooms[0].Name != "edge" {
		t.Fatalf("rooms = %+v", cons.Spaces[0].Rooms)
	}
}
