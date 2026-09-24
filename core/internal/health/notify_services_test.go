package health

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestChatProviderResponsesAndFeishuSignature(t *testing.T) {
	for _, tc := range []struct {
		kind, response string
		ok             bool
	}{
		{"feishu", `{"code":0}`, true}, {"feishu", `{"StatusCode":0}`, true},
		{"feishu", `{"code":19021,"StatusCode":0,"msg":"private-secret"}`, false},
		{"feishu", `{}`, false}, {"feishu", ``, false}, {"feishu", `{"code":null}`, false},
		{"feishu", `{"code":0}garbage`, false}, {"feishu", `{"code":0,"padding":"` + strings.Repeat("x", 17000) + `"}`, false},
		{"wecom", `{"errcode":0}`, true}, {"wecom", `{"errcode":93000}`, false},
		{"dingtalk", `{"errcode":0}`, true}, {"dingtalk", `{"errcode":310000}`, false},
	} {
		t.Run(tc.kind+strconv.Itoa(len(tc.response)), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if tc.kind == "feishu" {
					ts, _ := payload["timestamp"].(string)
					sec, err := strconv.ParseInt(ts, 10, 64)
					if err != nil || time.Since(time.Unix(sec, 0)).Abs() > 5*time.Second {
						t.Error("invalid timestamp")
					}
					mac := hmac.New(sha256.New, []byte(ts+"\nfixture-signing-key"))
					if payload["sign"] != base64.StdEncoding.EncodeToString(mac.Sum(nil)) {
						t.Error("invalid signature")
					}
					if payload["msg_type"] != "text" {
						t.Error(payload)
					}
				}
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()
			err := (&ChatNotifier{Kind: tc.kind, WebhookURL: srv.URL, Secret: "fixture-signing-key"}).Notify(context.Background(), LogEntry{Name: "磁盘告警", Status: StatusCritical})
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v want %v: %v", err == nil, tc.ok, err)
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("provider text leaked")
			}
		})
	}
}

func TestEmailChineseMIMEAndRequiredTLS(t *testing.T) {
	addr, got := fakeSMTP(t)
	n := &EmailNotifier{Server: addr, From: "monitor@example.com", To: []string{"ops@example.com", "backup@example.com"}}
	if err := n.Notify(context.Background(), LogEntry{Name: "内存过高", Info: "请检查服务。", Hostname: "监控主机", Status: StatusWarning}); err != nil {
		t.Fatal(err)
	}
	_, data, _ := strings.Cut(got.String(), "DATA\r\n")
	msg, err := mail.ReadMessage(strings.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || !strings.Contains(subject, "内存过高") {
		t.Fatal(subject, err)
	}
	b, err := io.ReadAll(quotedprintable.NewReader(msg.Body))
	if err != nil || !strings.Contains(string(b), "请检查服务。") {
		t.Fatal(string(b), err)
	}
	n.TLSMode = "starttls"
	if err := n.Notify(context.Background(), LogEntry{}); err == nil {
		t.Fatal("required STARTTLS silently downgraded")
	}
	for _, bad := range []EmailNotifier{
		{Server: addr, From: "a@x"}, {Server: addr, From: "", To: []string{"a@x"}},
		{Server: addr, From: "a@x", To: []string{"bad"}}, {Server: addr, From: "a@x", To: []string{"a@x"}, TLSMode: "off"},
		{Server: "smtp.example.com:0", From: "a@x", To: []string{"a@x"}},
	} {
		if bad.Validate() == nil {
			t.Fatal("invalid SMTP settings accepted")
		}
	}
}

func TestEmailTLSHandshakeCancellation(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c, err := ln.Accept()
		if err == nil {
			defer c.Close()
			_, _ = io.Copy(io.Discard, c)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	n := &EmailNotifier{Server: ln.Addr().String(), From: "a@x", To: []string{"b@x"}, TLSMode: "tls"}
	start := time.Now()
	err = n.Notify(ctx, LogEntry{})
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatal("TLS handshake ignored deadline", err)
	}
	_ = ln.Close()
	<-done
}

func TestEmailAcceptedBeforeQuitDisconnect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		fmt.Fprint(c, "220 fake ESMTP\r\n")
		r := bufio.NewReader(c)
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case strings.HasPrefix(line, "DATA"):
				fmt.Fprint(c, "354 go\r\n")
				for {
					line, err = r.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
				}
				fmt.Fprint(c, "250 queued\r\n")
				return
			default:
				fmt.Fprint(c, "250 ok\r\n")
			}
		}
	}()
	if err := (&EmailNotifier{Server: ln.Addr().String(), From: "a@x", To: []string{"b@x"}}).Notify(context.Background(), LogEntry{}); err != nil {
		t.Fatal("accepted DATA marked failed", err)
	}
}

func TestNotificationTestRoutingIsolationAndLimits(t *testing.T) {
	selected, other := 0, 0
	e := diagnosticsEngine(t, Options{SilenceAll: true, Roles: map[string][]string{"email": {"webhook"}}, Notifiers: []Notifier{
		diagnosticNotifier{"email", func() error { selected++; return nil }}, diagnosticNotifier{"webhook", func() error { other++; return nil }},
	}})
	if !errors.Is(e.QueueNotificationTest("missing"), ErrNotificationChannel) {
		t.Fatal("unknown channel")
	}
	if err := e.QueueNotificationTest("email"); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(e.QueueNotificationTest("webhook"), ErrNotificationTestLimit) {
		t.Fatal("test rate limit missing")
	}
	e.startDispatch()
	e.Close()
	d := e.NotificationDiagnostics()
	if selected != 1 || other != 0 || d.Accepted != 1 || !d.Recent[0].Test || d.Recent[0].EventID != 0 || len(e.entries) != 0 || e.Notified() != 0 {
		t.Fatal(selected, other, d)
	}
	if !errors.Is(e.QueueNotificationTest("email"), ErrNotificationUnavailable) {
		t.Fatal("closed engine accepted test")
	}
	full := diagnosticsEngine(t, Options{Notifiers: []Notifier{diagnosticNotifier{"email", func() error { return nil }}}})
	for i := 0; i < cap(full.notifyCh); i++ {
		full.notifyCh <- LogEntry{}
	}
	if !errors.Is(full.QueueNotificationTest("email"), ErrNotificationUnavailable) || !full.lastNotificationTest.IsZero() {
		t.Fatal("full queue consumed test rate limit")
	}
}

// Exercise both TLS modes with a real local SMTP handshake and AUTH, not just
// configuration checks. A test-only certificate remains untrusted by default.
func TestEmailTLSModes(t *testing.T) {
	certServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	cert := certServer.TLS.Certificates[0]
	certServer.Close()
	for _, mode := range []string{"tls", "starttls"} {
		for _, insecure := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/insecure=%v", mode, insecure), func(t *testing.T) {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				if err != nil {
					t.Fatal(err)
				}
				defer ln.Close()
				done := make(chan bool, 1)
				go func() {
					accepted := false
					defer func() { done <- accepted }()
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					defer conn.Close()
					_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
					tlsConfig := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
					encrypted := mode == "tls"
					if encrypted {
						conn = tls.Server(conn, tlsConfig)
					}
					if _, err = fmt.Fprint(conn, "220 fixture ESMTP\r\n"); err != nil {
						return
					}
					reader := bufio.NewReader(conn)
					for {
						line, err := reader.ReadString('\n')
						if err != nil {
							return
						}
						switch {
						case strings.HasPrefix(line, "EHLO"):
							if encrypted {
								fmt.Fprint(conn, "250-fixture\r\n250 AUTH PLAIN\r\n")
							} else {
								fmt.Fprint(conn, "250-fixture\r\n250 STARTTLS\r\n")
							}
						case strings.HasPrefix(line, "STARTTLS"):
							fmt.Fprint(conn, "220 begin TLS\r\n")
							conn = tls.Server(conn, tlsConfig)
							reader = bufio.NewReader(conn)
							encrypted = true
						case strings.HasPrefix(line, "AUTH"):
							if !encrypted {
								return
							}
							fmt.Fprint(conn, "235 authenticated\r\n")
						case strings.HasPrefix(line, "DATA"):
							fmt.Fprint(conn, "354 go\r\n")
							for {
								line, err = reader.ReadString('\n')
								if err != nil {
									return
								}
								if line == ".\r\n" {
									break
								}
							}
							accepted = true
							fmt.Fprint(conn, "250 queued\r\n")
						case strings.HasPrefix(line, "QUIT"):
							fmt.Fprint(conn, "221 bye\r\n")
							return
						default:
							fmt.Fprint(conn, "250 ok\r\n")
						}
					}
				}()
				err = (&EmailNotifier{Server: ln.Addr().String(), From: "a@x", To: []string{"b@x"}, Username: "fixture", Password: "fixture", TLSMode: mode, Insecure: insecure}).Notify(context.Background(), LogEntry{})
				if (err == nil) != insecure {
					t.Fatalf("unexpected certificate result: %v", err)
				}
				_ = ln.Close()
				if accepted := <-done; accepted != insecure {
					t.Fatal("unexpected delivery result", accepted)
				}
			})
		}
	}
}
