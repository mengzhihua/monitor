package health

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// WebhookNotifier POSTs the LogEntry as JSON to a URL.
type WebhookNotifier struct {
	URL     string
	Headers map[string]string
	Client  *http.Client
}

func (n *WebhookNotifier) Name() string { return "webhook" }

func (n *WebhookNotifier) Notify(ctx context.Context, e LogEntry) error {
	body, err := json.Marshal(e)
	if err != nil {
		return err
	}
	return postJSON(ctx, n.client(), n.URL, body, n.Headers)
}

func (n *WebhookNotifier) client() *http.Client {
	if n.Client != nil {
		return n.Client
	}
	return http.DefaultClient
}

// SlackNotifier posts to a Slack (or Mattermost/Rocket.Chat compatible)
// incoming webhook.
type SlackNotifier struct {
	WebhookURL string
	Channel    string
	Client     *http.Client
}

func (n *SlackNotifier) Name() string { return "slack" }

func (n *SlackNotifier) Notify(ctx context.Context, e LogEntry) error {
	color := map[Status]string{StatusClear: "good", StatusWarning: "warning", StatusCritical: "danger"}[e.Status]
	if color == "" {
		color = "#808080"
	}
	msg := map[string]any{
		"text": fmt.Sprintf("%s %s on %s", statusEmoji(e.Status), e.Name, e.Hostname),
		"attachments": []map[string]any{{
			"color":     color,
			"title":     fmt.Sprintf("%s → %s", e.OldStatus, e.Status),
			"text":      Summarize(e),
			"footer":    "monitor",
			"ts":        e.When,
			"mrkdwn_in": []string{"text"},
		}},
	}
	if n.Channel != "" {
		msg["channel"] = n.Channel
	}
	body, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	return postJSON(ctx, c, n.WebhookURL, body, nil)
}

func statusEmoji(s Status) string {
	switch s {
	case StatusCritical:
		return ":red_circle:"
	case StatusWarning:
		return ":large_orange_circle:"
	case StatusClear:
		return ":large_green_circle:"
	}
	return ":white_circle:"
}

// Summarize renders a human-readable one-paragraph description of an entry.
func Summarize(e LogEntry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "*%s* on chart `%s` (%s) is *%s*", e.Name, e.Chart, e.Family, e.Status)
	if e.OldStatus > StatusUndefined {
		fmt.Fprintf(&b, " (was %s)", e.OldStatus)
	}
	fmt.Fprintf(&b, ": %s %s", formatValue(e.Value), e.Units)
	if e.Info != "" {
		fmt.Fprintf(&b, "\n%s", e.Info)
	}
	if e.Repeat {
		b.WriteString("\n(repeat notification)")
	}
	return b.String()
}

func formatValue(v float64) string {
	s := fmt.Sprintf("%.3f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

func postJSON(ctx context.Context, c *http.Client, url string, body []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "monitor-health/1")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return &notificationHTTPError{status: resp.StatusCode}
	}
	return nil
}

// EmailNotifier sends MIME text mail via SMTP. Auto mode negotiates STARTTLS
// or implicit TLS on port 465; explicit modes require TLS. Credentials are only
// sent over TLS unless Insecure is set.
type EmailNotifier struct {
	Server   string // host:port
	From     string
	To       []string
	Username string
	Password string
	Insecure bool   // skip certificate verification and allow plaintext auth
	TLSMode  string // auto (465 implicit TLS, otherwise opportunistic STARTTLS), starttls, tls
}

// Validate rejects incomplete SMTP settings before any connection is opened.
func (n *EmailNotifier) Validate() error {
	host, port, err := net.SplitHostPort(n.Server)
	p, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || p < 1 || p > 65535 {
		return fmt.Errorf("email: server must be host:port")
	}
	if n.TLSMode != "" && n.TLSMode != "auto" && n.TLSMode != "starttls" && n.TLSMode != "tls" {
		return fmt.Errorf("email: tls_mode must be auto, starttls or tls")
	}
	if len(n.To) == 0 {
		return fmt.Errorf("email: at least one recipient is required")
	}
	for _, addr := range append([]string{n.From}, n.To...) {
		parsed, err := mail.ParseAddress(addr)
		if err != nil || strings.ContainsAny(addr, "\r\n") || parsed.Address != addr {
			return fmt.Errorf("email: from and to must contain plain mailbox addresses")
		}
	}
	return nil
}

// headerSafe strips CR/LF so values interpolated into mail headers cannot
// inject additional headers or recipients.
func headerSafe(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func (n *EmailNotifier) Name() string { return "email" }

func (n *EmailNotifier) Notify(ctx context.Context, e LogEntry) error {
	if err := n.Validate(); err != nil {
		return err
	}
	host, port, _ := net.SplitHostPort(n.Server)
	var err error
	subject := fmt.Sprintf("[%s] %s: %s is %s", e.Hostname, e.Status, e.Name, formatValue(e.Value)+" "+e.Units)
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\nDate: %s\r\n\r\n",
		n.From, strings.Join(n.To, ", "), mime.QEncoding.Encode("UTF-8", headerSafe(subject)), time.Unix(e.When, 0).Format(time.RFC1123Z))
	writer := quotedprintable.NewWriter(&msg)
	_, _ = writer.Write([]byte(strings.NewReplacer("*", "", "`", "").Replace(Summarize(e)) + "\r\n"))
	_ = writer.Close()

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: n.Insecure, MinVersion: tls.VersionTLS12}
	var conn net.Conn
	implicitTLS := n.TLSMode == "tls" || ((n.TLSMode == "" || n.TLSMode == "auto") && port == "465")
	if implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsCfg}).DialContext(ctx, "tcp", n.Server)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", n.Server)
	}
	if err != nil {
		return err
	}
	// Bound the whole SMTP session: the dialer timeout only covers connect.
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	sessionDone := make(chan struct{})
	defer close(sessionDone)
	go func() {
		select {
		case <-ctx.Done():
			conn.Close()
		case <-sessionDone:
		}
	}()
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()
	encrypted := implicitTLS
	if !encrypted {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return err
			}
			encrypted = true
		}
	}
	if n.TLSMode == "starttls" && !encrypted {
		return fmt.Errorf("email: server does not offer required STARTTLS")
	}
	if n.Username != "" && !encrypted && !n.Insecure {
		return fmt.Errorf("smtp %s: server does not offer STARTTLS; refusing to send credentials in plaintext (insecure_skip_verify permits plaintext credentials)", n.Server)
	}
	if n.Username != "" {
		if err := c.Auth(smtp.PlainAuth("", n.Username, n.Password, host)); err != nil {
			return err
		}
	}
	if err := c.Mail(n.From); err != nil {
		return err
	}
	for _, rcpt := range n.To {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg.Bytes()); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	// DATA's final 250 is the acceptance boundary. A disconnect during QUIT
	// must not turn an accepted message into a failed delivery/repeat.
	_ = c.Quit()
	return nil
}

// ChatNotifier posts a markdown payload understood by DingTalk, WeCom or Feishu.
type ChatNotifier struct {
	Kind       string // dingtalk | wecom | feishu
	WebhookURL string
	Secret     string // optional Feishu custom-bot signing key
	Client     *http.Client
}

func (n *ChatNotifier) Name() string {
	if n.Kind == "" {
		return "chat"
	}
	return n.Kind
}

func (n *ChatNotifier) Notify(ctx context.Context, e LogEntry) error {
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	title := fmt.Sprintf("[%s] %s on %s", e.Status, e.Name, e.Hostname)
	text := strings.NewReplacer("*", "**", "`", "`").Replace(Summarize(e))
	var body []byte
	var err error
	switch n.Kind {
	case "wecom":
		body, err = json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"content": title + "\n" + text}})
	case "feishu":
		payload := map[string]any{"msg_type": "text", "content": map[string]string{"text": title + "\n" + strings.NewReplacer("*", "", "`", "").Replace(Summarize(e))}}
		if n.Secret != "" {
			ts := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, []byte(ts+"\n"+n.Secret))
			payload["timestamp"], payload["sign"] = ts, base64.StdEncoding.EncodeToString(mac.Sum(nil))
		}
		body, err = json.Marshal(payload)
	default: // dingtalk
		body, err = json.Marshal(map[string]any{"msgtype": "markdown", "markdown": map[string]string{"title": title, "text": title + "\n\n" + text}})
	}
	if err != nil {
		return err
	}
	return postChatJSON(ctx, c, n.WebhookURL, body, n.Kind)
}

// Chat APIs may reject a message with HTTP 200. Never retain their response
// text, which can echo private URLs, tokens, recipients or message content.
func postChatJSON(ctx context.Context, c *http.Client, url string, body []byte, kind string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "monitor-health/1")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &notificationHTTPError{status: resp.StatusCode}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16385))
	if err != nil {
		return err
	}
	var result struct {
		Code       *int `json:"code"`
		LegacyCode *int `json:"StatusCode"`
		ErrCode    *int `json:"errcode"`
	}
	if len(b) > 16384 || json.Unmarshal(b, &result) != nil {
		return fmt.Errorf("chat: invalid response")
	}
	code := result.ErrCode
	if kind == "feishu" {
		code = result.Code
		if code == nil {
			code = result.LegacyCode
		}
	}
	if code == nil {
		return fmt.Errorf("chat: missing response code")
	}
	if *code != 0 {
		return fmt.Errorf("chat: provider rejected message (code %d)", *code)
	}
	return nil
}

// TelegramNotifier sends a Markdown message via Bot API sendMessage.
type TelegramNotifier struct {
	Token    string
	ChatID   string
	Client   *http.Client
	Endpoint string // default https://api.telegram.org
}

func (n *TelegramNotifier) Name() string { return "telegram" }

func (n *TelegramNotifier) Notify(ctx context.Context, e LogEntry) error {
	ep := n.Endpoint
	if ep == "" {
		ep = "https://api.telegram.org"
	}
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	text := fmt.Sprintf("*%s* `%s` on `%s`\n%s", e.Status, e.Name, e.Hostname, strings.NewReplacer("*", "", "`", "").Replace(Summarize(e)))
	body, err := json.Marshal(map[string]any{"chat_id": n.ChatID, "text": text, "parse_mode": "Markdown"})
	if err != nil {
		return err
	}
	return postJSON(ctx, c, strings.TrimRight(ep, "/")+"/bot"+n.Token+"/sendMessage", body, nil)
}

// DiscordNotifier posts plain text to a Discord incoming webhook.
type DiscordNotifier struct {
	WebhookURL string
	Client     *http.Client
}

func (n *DiscordNotifier) Name() string { return "discord" }

func (n *DiscordNotifier) Notify(ctx context.Context, e LogEntry) error {
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	body, err := json.Marshal(map[string]any{"content": fmt.Sprintf("[%s] %s on %s\n%s", e.Status, e.Name, e.Hostname, strings.NewReplacer("*", "", "`", "").Replace(Summarize(e)))})
	if err != nil {
		return err
	}
	return postJSON(ctx, c, n.WebhookURL, body, nil)
}

// PagerDutyNotifier sends Events API v2 trigger/resolve payloads.
type PagerDutyNotifier struct {
	RoutingKey string
	Client     *http.Client
	Endpoint   string // default https://events.pagerduty.com/v2/enqueue
}

func (n *PagerDutyNotifier) Name() string { return "pagerduty" }

func (n *PagerDutyNotifier) Notify(ctx context.Context, e LogEntry) error {
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	ep := n.Endpoint
	if ep == "" {
		ep = "https://events.pagerduty.com/v2/enqueue"
	}
	action := "trigger"
	sev := "warning"
	switch e.Status {
	case StatusClear:
		action = "resolve"
		sev = "info"
	case StatusCritical:
		sev = "critical"
	}
	body, err := json.Marshal(map[string]any{
		"routing_key":  n.RoutingKey,
		"event_action": action,
		"dedup_key":    e.Hostname + "/" + e.Chart + "/" + e.Name,
		"payload": map[string]any{
			"summary":   fmt.Sprintf("%s is %s on %s", e.Name, e.Status, e.Hostname),
			"source":    e.Hostname,
			"severity":  sev,
			"component": e.Chart,
			"custom_details": map[string]any{
				"value": e.Value, "units": e.Units, "info": e.Info,
			},
		},
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, c, ep, body, nil)
}

// PushNotifier is a mobile / webhook push gateway (same JSON payload as webhook).
type PushNotifier struct {
	URL     string
	Headers map[string]string
	Client  *http.Client
}

func (n *PushNotifier) Name() string { return "push" }

func (n *PushNotifier) Notify(ctx context.Context, e LogEntry) error {
	return (&WebhookNotifier{URL: n.URL, Headers: n.Headers, Client: n.Client}).Notify(ctx, e)
}
