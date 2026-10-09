package aianalysis

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

const goodResponse = `{"model":"served-model","choices":[{"message":{"role":"assistant","content":"事实：CPU 利用率为 25%。\n推测：负载稳定。\n建议：继续观察。"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}`

func TestCompleteProtocolAndSecretIsolation(t *testing.T) {
	key := "test-private-secret"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer "+key || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect provider request method, path or headers")
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if strings.Contains(string(data), key) {
			t.Error("secret included in prompt body")
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(data, &body); err != nil || len(body) != 2 || string(body["model"]) != `"test-model"` {
			t.Errorf("unexpected provider payload: %v", err)
		}
		var messages []struct{ Role, Content string }
		if err := json.Unmarshal(body["messages"], &messages); err != nil || len(messages) != 2 || messages[0].Role != "system" || messages[0].Content != systemPrompt || messages[1].Role != "user" || messages[1].Content != "CPU: 25%" {
			t.Errorf("incorrect analysis messages: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, goodResponse)
	}))
	defer server.Close()
	s := mustStore(t, t.TempDir())
	owner := ownerFor("alice")
	c, err := s.Save(owner, Update{BaseURL: server.URL + "/v1", Model: "test-model", APIKey: &key, AllowPrivate: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Complete(context.Background(), owner, c.Revision, "CPU: 25%")
	if err != nil || !strings.Contains(result.Text, "25%") || result.Model != "served-model" || result.Truncated || result.Usage == nil || result.Usage.TotalTokens != 30 {
		t.Fatalf("completion: %+v %v", result, err)
	}
	if _, err := s.Complete(context.Background(), ownerFor("bob"), c.Revision, "CPU: 25%"); !errors.Is(err, ErrConflict) {
		t.Fatalf("other identity used config: %v", err)
	}
	if _, err := s.Complete(context.Background(), owner, 0, "CPU: 25%"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale revision used config: %v", err)
	}
	if requests.Load() != 1 {
		t.Fatal("rejected request reached provider")
	}
}

func TestKeylessProviderAndNoEnvironmentProxy(t *testing.T) {
	var proxyCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("ALL_PROXY", proxy.URL)
	t.Setenv("NO_PROXY", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("empty API key generated authentication header")
		}
		io.WriteString(w, goodResponse)
	}))
	defer server.Close()
	s := mustStore(t, "")
	owner := ownerFor("alice")
	// Use a hostname rather than localhost: ProxyFromEnvironment itself
	// bypasses localhost, which would otherwise hide a proxy regression.
	s.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	baseURL := strings.Replace(server.URL, "127.0.0.1", "private-provider.example", 1)
	c := mustSave(t, s, owner, baseURL+"/v1", true)
	if _, err := s.Complete(context.Background(), owner, c.Revision, "data"); err != nil {
		t.Fatal(err)
	}
	if proxyCalls.Load() != 0 {
		t.Fatal("request used environment proxy")
	}
}

func TestRedirectDoesNotForwardCredentials(t *testing.T) {
	var leaked atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leaked.Add(1)
		io.WriteString(w, goodResponse)
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	s := mustStore(t, "")
	owner := ownerFor("alice")
	key := "must-not-be-forwarded"
	c, err := s.Save(owner, Update{BaseURL: server.URL, Model: "model", APIKey: &key, AllowPrivate: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Complete(context.Background(), owner, c.Revision, "data")
	var status *HTTPError
	if !errors.Is(err, ErrProvider) || !errors.As(err, &status) || status.StatusCode != http.StatusTemporaryRedirect || leaked.Load() != 0 {
		t.Fatalf("redirect handling: %v target calls %d", err, leaked.Load())
	}
}

func TestProviderFailuresAreBoundedAndSafe(t *testing.T) {
	secret := "sensitive-provider-secret"
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rate_limit", 429, secret}, {"server_error", 500, secret}, {"empty", 200, ""}, {"malformed", 200, "{" + secret},
		{"empty_choices", 200, `{"choices":[]}`}, {"missing_content", 200, `{"choices":[{"message":{}}]}`},
		{"blank_content", 200, `{"choices":[{"message":{"content":"   "}}]}`},
		{"array_content", 200, `{"choices":[{"message":{"content":[{"text":"hello"}]}}]}`},
		{"tool_calls", 200, `{"choices":[{"message":{"content":"hello","tool_calls":[{"id":"tool"}]}}]}`},
		{"function_call", 200, `{"choices":[{"message":{"content":"hello","function_call":{"name":"tool"}}}]}`},
		{"tool_finish", 200, `{"choices":[{"message":{"content":"hello"},"finish_reason":"tool_calls"}]}`},
		{"oversized", 200, strings.Repeat("x", maxResponseSize+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(tc.status)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			s := mustStore(t, "")
			owner := ownerFor("alice")
			c, err := s.Save(owner, Update{BaseURL: server.URL, Model: "model", APIKey: &secret, AllowPrivate: true}, true)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.Complete(context.Background(), owner, c.Revision, "data")
			if !errors.Is(err, ErrProvider) || strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), server.URL) || calls.Load() != 1 {
				t.Fatalf("unsafe or retried failure: %v (%d calls)", err, calls.Load())
			}
		})
	}
}

func TestTextTruncationAndSecretRedaction(t *testing.T) {
	for _, content := range []string{strings.Repeat("界", maxTextSize/3+10), "API key: private-secret-value"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(map[string]any{
				"model":   "private-secret-value",
				"choices": []any{map[string]any{"message": map[string]any{"content": content}, "finish_reason": "length"}},
			})
		}))
		s := mustStore(t, "")
		owner := ownerFor("alice")
		key := "private-secret-value"
		c, err := s.Save(owner, Update{BaseURL: server.URL, Model: "model", APIKey: &key, AllowPrivate: true}, true)
		if err != nil {
			t.Fatal(err)
		}
		result, err := s.Complete(context.Background(), owner, c.Revision, "data")
		server.Close()
		if err != nil || !result.Truncated || !utf8.ValidString(result.Text) || len(result.Text) > maxTextSize || strings.Contains(result.Text, key) || strings.Contains(result.Model, key) {
			t.Fatalf("invalid truncation/redaction: bytes=%d %v", len(result.Text), err)
		}
	}
}

func TestConcurrencyAndCancellation(t *testing.T) {
	entered := make(chan struct{}, 4)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-release:
			io.WriteString(w, goodResponse)
		}
	}))
	defer server.Close()
	defer close(release)
	s := mustStore(t, "")
	owners := []string{ownerFor("alice"), ownerFor("bob"), ownerFor("charlie")}
	for _, owner := range owners {
		mustSave(t, s, owner, server.URL, true)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errorsSeen := make(chan error, 2)
	go func() { _, err := s.Complete(ctx, owners[0], 1, "data"); errorsSeen <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first request did not start")
	}
	if _, err := s.Complete(ctx, owners[0], 1, "data"); !errors.Is(err, ErrBusy) {
		t.Fatalf("same-identity concurrency accepted: %v", err)
	}
	go func() { _, err := s.Complete(ctx, owners[1], 1, "data"); errorsSeen <- err }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("second request did not start")
	}
	if _, err := s.Complete(ctx, owners[2], 1, "data"); !errors.Is(err, ErrBusy) {
		t.Fatalf("global concurrency accepted: %v", err)
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errorsSeen:
			if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrProvider) {
				t.Fatalf("cancellation not preserved: %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("request ignored cancellation")
		}
	}
	s.mu.Lock()
	if s.inFlight != 0 || len(s.active) != 0 {
		t.Error("canceled calls retained concurrency slots")
	}
	s.mu.Unlock()
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return f(ctx, network, host)
}

type dialerFunc func(context.Context, string, string) (net.Conn, error)

func (f dialerFunc) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return f(ctx, network, address)
}

func TestSafeDialValidatesAllDNSAnswersAndPinsIP(t *testing.T) {
	cases := []struct {
		name     string
		ips      []string
		private  bool
		scheme   string
		wantDial bool
	}{
		{"public_https", []string{"8.8.8.8", "1.1.1.1"}, false, "https", true},
		{"mixed_public_private", []string{"8.8.8.8", "10.0.0.1"}, false, "https", false},
		{"private_https", []string{"127.0.0.1"}, false, "https", false},
		{"private_http_opt_in", []string{"127.0.0.1"}, true, "http", true},
		{"public_http_forbidden", []string{"8.8.8.8"}, true, "http", false},
		{"mixed_http_forbidden", []string{"127.0.0.1", "8.8.8.8"}, true, "http", false},
		{"metadata", []string{"169.254.169.254"}, true, "http", false},
		{"mapped_metadata", []string{"::ffff:169.254.169.254"}, true, "https", false},
		{"link_local_v6", []string{"fe80::1"}, true, "https", false},
		{"multicast", []string{"224.0.0.1"}, true, "https", false},
		{"unspecified", []string{"::"}, true, "https", false},
		{"translation_to_private", []string{"64:ff9b::a9fe:a9fe"}, true, "https", false},
		{"reserved", []string{"198.18.0.1"}, false, "https", false},
		{"empty", nil, false, "https", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := mustStore(t, "")
			resolved, dialed := 0, 0
			s.resolver = resolverFunc(func(ctx context.Context, network, host string) ([]netip.Addr, error) {
				resolved++
				if host != "provider.example" || network != "ip" {
					t.Error("unexpected resolution")
				}
				var ips []netip.Addr
				for _, raw := range tc.ips {
					ips = append(ips, netip.MustParseAddr(raw))
				}
				return ips, nil
			})
			s.dialer = dialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
				dialed++
				host, port, err := net.SplitHostPort(address)
				if err != nil || port != "443" || host != netip.MustParseAddr(tc.ips[0]).Unmap().String() {
					t.Errorf("dial did not pin validated literal: %q %v", address, err)
				}
				return nil, nil
			})
			_, err := s.safeDial(tc.private, tc.scheme)(context.Background(), "tcp", "provider.example:443")
			if resolved != 1 || (dialed != 0) != tc.wantDial || (err == nil) != tc.wantDial {
				t.Fatalf("resolution=%d dial=%d error=%v", resolved, dialed, err)
			}
		})
	}
}

func TestUnconfiguredAndInvalidPrompt(t *testing.T) {
	s := mustStore(t, "")
	owner := ownerFor("alice")
	if _, err := s.Complete(context.Background(), owner, 0, "data"); !errors.Is(err, ErrUnconfigured) {
		t.Fatalf("unconfigured call: %v", err)
	}
	for _, prompt := range []string{"", " \n ", strings.Repeat("x", maxPromptSize+1)} {
		if _, err := s.Complete(context.Background(), owner, 0, prompt); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid prompt: %v", err)
		}
	}
}

func TestExistingSettingsPermissionsTightened(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits")
	}
	dir := t.TempDir()
	s := mustStore(t, dir)
	mustSave(t, s, ownerFor("alice"), "https://example.com", false)
	if err := os.Chmod(s.path, 0644); err != nil {
		t.Fatal(err)
	}
	mustStore(t, dir)
	info, err := os.Stat(s.path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("existing secret file permissions not tightened: %v %v", info, err)
	}
}
