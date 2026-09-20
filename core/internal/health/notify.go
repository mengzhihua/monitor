package health

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/smtp"
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
	if resp.StatusCode >= 300 {
		return fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return nil
}

// EmailNotifier sends plain-text mail via SMTP (STARTTLS when the server
// offers it, implicit TLS on port 465). Credentials are only sent over TLS
// unless Insecure is set.
type EmailNotifier struct {
	Server   string // host:port
	From     string
	To       []string
	Username string
	Password string
	Insecure bool // skip certificate verification and allow plaintext auth
}

// headerSafe strips CR/LF so values interpolated into mail headers cannot
// inject additional headers or recipients.
func headerSafe(s string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(s)
}

func (n *EmailNotifier) Name() string { return "email" }

func (n *EmailNotifier) Notify(ctx context.Context, e LogEntry) error {
	host, port, err := net.SplitHostPort(n.Server)
	if err != nil {
		return fmt.Errorf("email server %q: %w", n.Server, err)
	}
	for _, addr := range append([]string{n.From}, n.To...) {
		if strings.ContainsAny(addr, "\r\n") {
			return fmt.Errorf("email address %q contains a line break", addr)
		}
	}
	subject := fmt.Sprintf("[%s] %s: %s is %s", e.Hostname, e.Status, e.Name, formatValue(e.Value)+" "+e.Units)
	var msg bytes.Buffer
	fmt.Fprintf(&msg, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nDate: %s\r\n\r\n",
		n.From, strings.Join(n.To, ", "), headerSafe(subject), time.Unix(e.When, 0).Format(time.RFC1123Z))
	msg.WriteString(strings.NewReplacer("*", "", "`", "").Replace(Summarize(e)))
	msg.WriteString("\r\n")

	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tlsCfg := &tls.Config{ServerName: host, InsecureSkipVerify: n.Insecure}
	var conn net.Conn
	if port == "465" {
		conn, err = tls.DialWithDialer(dialer, "tcp", n.Server, tlsCfg)
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
	encrypted := port == "465"
	if !encrypted {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return err
			}
			encrypted = true
		}
	}
	if n.Username != "" && !encrypted && !n.Insecure {
		return fmt.Errorf("smtp %s: server does not offer STARTTLS; refusing to send credentials in plaintext (set insecure: true to override)", n.Server)
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
	return c.Quit()
}
