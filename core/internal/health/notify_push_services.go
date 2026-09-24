package health

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// These endpoints are explicit publish URLs, including any reverse-proxy path.
// No public provider is selected by default and credentials stay out of URLs.
func validatePushURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return fmt.Errorf("notification: configure an HTTP(S) publish URL without credentials, query or fragment")
	}
	return nil
}

func pushText(e LogEntry) (string, string) {
	return fmt.Sprintf("[%s] %s on %s", e.Status, e.Name, e.Hostname), strings.NewReplacer("*", "", "`", "").Replace(Summarize(e))
}

// postPushServiceJSON checks a bounded JSON response. Redirects are rejected:
// custom auth headers and device keys must not follow a moved/login endpoint.
func postPushServiceJSON(ctx context.Context, client *http.Client, endpoint string, payload any, headers map[string]string, result any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("notification: invalid payload")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("notification: invalid request")
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "monitor-health/1")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	if client == nil {
		client = http.DefaultClient
	}
	bounded := *client
	if bounded.Timeout == 0 {
		bounded.Timeout = 30 * time.Second
	}
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := bounded.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &notificationHTTPError{status: resp.StatusCode}
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, 65537))
	if err != nil {
		return err
	}
	if len(body) > 65536 || json.Unmarshal(body, result) != nil {
		return fmt.Errorf("notification: invalid provider response")
	}
	return nil
}

// NtfyNotifier publishes JSON to an ntfy server root, with optional token auth.
type NtfyNotifier struct {
	URL, Topic, Token string
	Client            *http.Client
}

func (*NtfyNotifier) Name() string { return "ntfy" }
func (n *NtfyNotifier) Validate() error {
	if err := validatePushURL(n.URL); err != nil {
		return err
	}
	if len(n.Topic) < 1 || len(n.Topic) > 64 {
		return fmt.Errorf("ntfy: topic must contain 1-64 ASCII letters, digits, underscores or hyphens")
	}
	for _, c := range n.Topic {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return fmt.Errorf("ntfy: invalid topic characters")
		}
	}
	if strings.ContainsAny(n.Token, "\r\n") {
		return fmt.Errorf("ntfy: invalid token")
	}
	return nil
}
func (n *NtfyNotifier) Notify(ctx context.Context, e LogEntry) error {
	if err := n.Validate(); err != nil {
		return err
	}
	title, message := pushText(e)
	priority := 3
	if e.Status == StatusCritical {
		priority = 5
	} else if e.Status == StatusWarning {
		priority = 4
	}
	headers := map[string]string{}
	if n.Token != "" {
		headers["Authorization"] = "Bearer " + n.Token
	}
	var result struct {
		ID    string `json:"id"`
		Event string `json:"event"`
		Topic string `json:"topic"`
	}
	err := postPushServiceJSON(ctx, n.Client, n.URL, map[string]any{"topic": n.Topic, "title": title, "message": message, "priority": priority}, headers, &result)
	if err != nil {
		return err
	}
	if result.ID == "" || result.Event != "message" || result.Topic != n.Topic {
		return fmt.Errorf("ntfy: missing or mismatched message acknowledgement")
	}
	return nil
}

// GotifyNotifier authenticates with an application token, not a client token.
type GotifyNotifier struct {
	URL, Token string
	Client     *http.Client
}

func (*GotifyNotifier) Name() string { return "gotify" }
func (n *GotifyNotifier) Validate() error {
	if err := validatePushURL(n.URL); err != nil {
		return err
	}
	if strings.TrimSpace(n.Token) == "" || strings.ContainsAny(n.Token, "\r\n") {
		return fmt.Errorf("gotify: an application token is required")
	}
	return nil
}
func (n *GotifyNotifier) Notify(ctx context.Context, e LogEntry) error {
	if err := n.Validate(); err != nil {
		return err
	}
	title, message := pushText(e)
	priority := 2
	if e.Status == StatusCritical {
		priority = 8
	} else if e.Status == StatusWarning {
		priority = 5
	}
	var result struct {
		ID int64 `json:"id"`
	}
	err := postPushServiceJSON(ctx, n.Client, n.URL, map[string]any{"title": title, "message": message, "priority": priority}, map[string]string{"X-Gotify-Key": n.Token}, &result)
	if err != nil {
		return err
	}
	if result.ID <= 0 {
		return fmt.Errorf("gotify: missing message acknowledgement")
	}
	return nil
}

// BarkNotifier keeps the device key in the JSON body instead of the URL path.
type BarkNotifier struct {
	URL, DeviceKey, Group string
	Client                *http.Client
}

func (*BarkNotifier) Name() string { return "bark" }
func (n *BarkNotifier) Validate() error {
	if err := validatePushURL(n.URL); err != nil {
		return err
	}
	if strings.TrimSpace(n.DeviceKey) == "" {
		return fmt.Errorf("bark: a device key is required")
	}
	return nil
}
func (n *BarkNotifier) Notify(ctx context.Context, e LogEntry) error {
	if err := n.Validate(); err != nil {
		return err
	}
	title, message := pushText(e)
	group := n.Group
	if group == "" {
		group = "Monitor"
	}
	var result struct {
		Code *int `json:"code"`
	}
	err := postPushServiceJSON(ctx, n.Client, n.URL, map[string]any{"device_key": n.DeviceKey, "title": title, "body": message, "group": group}, nil, &result)
	if err != nil {
		return err
	}
	if result.Code == nil || *result.Code != 200 {
		return fmt.Errorf("bark: provider rejected message")
	}
	return nil
}
