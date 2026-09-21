package collect

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestM10CollectorsRegistered(t *testing.T) {
	for _, name := range []string{
		"postfix", "exim", "dovecot", "fail2ban", "weblog", "squidlog",
		"openldap", "wireguard", "samba", "freeradius", "tor",
	} {
		found := false
		for _, n := range Available() {
			if n == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("collector %s not registered", name)
		}
	}
}

func TestParsePostqueue(t *testing.T) {
	empty, err := parsePostqueue([]byte("Mail queue is empty\n"))
	if err != nil || empty.emails != 0 {
		t.Fatalf("%v %v", empty, err)
	}
	q, err := parsePostqueue([]byte("-Queue ID-  --Size-- ----Arrival Time---- -Sender/Recipient-------\nABCDEF  1024 Tue Sep 21 00:00:00  a@b\n\n-- 3 Kbytes in 3 Requests.\n"))
	if err != nil || q.emails != 3 || q.sizeKiB != 3 {
		t.Fatalf("%v %v", q, err)
	}
	p := &postfixCollector{
		cfg: postfixConfig{Command: "postqueue", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("Mail queue is empty\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := p.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := p.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestEximQueue(t *testing.T) {
	e := &eximCollector{
		cfg: eximConfig{Command: "exim", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte("4\n"), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := e.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := e.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestDovecotExport(t *testing.T) {
	body := "num_connected_sessions num_logins auth_successes auth_failures num_cmds min_faults maj_faults disk_input disk_output read_bytes write_bytes mail_cache_hits auth_cache_hits auth_cache_misses\n3 10 8 2 20 1 0 100 200 1000 2000 9 4 1\n"
	m, err := parseDovecotExport(body)
	if err != nil || m["num_logins"] != 10 || m["auth_failures"] != 2 {
		t.Fatalf("%v %v", m, err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 32)
				_, _ = c.Read(buf)
				_, _ = io.WriteString(c, body)
			}(c)
		}
	}()
	d := &dovecotCollector{cfg: dovecotConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := d.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := d.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestFail2banParse(t *testing.T) {
	jails, err := parseFail2banStatus([]byte("Status\n|- Number of jail:\t2\n`- Jail list:\tsshd, apache\n"))
	if err != nil || len(jails) != 2 || jails[0] != "sshd" {
		t.Fatalf("%v %v", jails, err)
	}
	st, err := parseFail2banJailStatus([]byte("Status for the jail: sshd\n|- Currently failed:\t1\n`- Currently banned:\t2\n"))
	if err != nil || st.failed != 1 || st.banned != 2 {
		t.Fatalf("%v %v", st, err)
	}
	f := &fail2banCollector{
		cfg: fail2banConfig{Command: "fail2ban-client", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) == 1 {
				return []byte("`- Jail list:\tsshd\n"), nil
			}
			return []byte("Currently failed: 1\nCurrently banned: 2\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := f.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := f.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("fail2ban.jail_banned_ips.sshd"); !ok {
		t.Fatal("missing jail chart")
	}
}

func TestWeblogCombined(t *testing.T) {
	method, code, size, ok := parseCombinedLog(`127.0.0.1 - - [21/Sep/2026:00:00:00 +0000] "GET /index.html HTTP/1.1" 200 1234`)
	if !ok || method != "GET" || code != "200" || size != "1234" {
		t.Fatalf("%s %s %s %v", method, code, size, ok)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	w := &weblogCollector{cfg: weblogConfig{Path: path}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := w.Init(reg); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`127.0.0.1 - - [21/Sep/2026:00:00:00 +0000] "GET / HTTP/1.1" 200 100` + "\n")
	_, _ = f.WriteString(`127.0.0.1 - - [21/Sep/2026:00:00:01 +0000] "POST /x HTTP/1.1" 500 20` + "\n")
	_ = f.Close()
	if err := w.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if w.totals.requests != 2 || w.totals.c5 != 1 || w.totals.get != 1 {
		t.Fatalf("%+v", w.totals)
	}
}

func TestSquidLogParse(t *testing.T) {
	code, status, size, method, ok := parseSquidLog(`1690000000.123  12 10.0.0.1 TCP_MISS/200 345 GET http://example.com/ - HIER_DIRECT/1.2.3.4 text/html`)
	if !ok || code != "TCP_MISS" || status != "200" || size != "345" || method != "GET" {
		t.Fatalf("%s %s %s %s %v", code, status, size, method, ok)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "access.log")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := &squidlogCollector{cfg: squidlogConfig{Path: path}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`1690000000.123  12 10.0.0.1 TCP_HIT/200 100 GET http://e/ - HIER_NONE/- text/html`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// file was empty at Init; writing replaces the file so cursor off may exceed size → rotation reset.
	s.cursor.off = 0
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if s.totals.hit != 1 || s.totals.requests != 1 {
		t.Fatalf("%+v", s.totals)
	}
}

func TestParseOpenLDAPMonitor(t *testing.T) {
	ldif := "dn: cn=Current,cn=Connections,cn=Monitor\nmonitorCounter: 4\n\n" +
		"dn: cn=Total,cn=Connections,cn=Monitor\nmonitorCounter: 20\n\n" +
		"dn: cn=Bytes,cn=Statistics,cn=Monitor\nmonitorCounter: 1000\n\n" +
		"dn: cn=Operations,cn=Monitor\nmonitorOpInitiated: 50\nmonitorOpCompleted: 48\n\n" +
		"dn: cn=Bind,cn=Operations,cn=Monitor\nmonitorOpCompleted: 10\n\n" +
		"dn: cn=Search,cn=Operations,cn=Monitor\nmonitorOpCompleted: 30\n\n"
	m := parseOpenLDAPMonitor([]byte(ldif))
	if m["current_connections"] != 4 || m["total_connections"] != 20 || m["completed_bind_operations"] != 10 || m["completed_operations"] != 48 {
		t.Fatalf("%v", m)
	}
	o := &openldapCollector{
		cfg: openldapConfig{Command: "ldapsearch", URL: "ldap://127.0.0.1:389", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(ldif), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := o.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := o.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseWGDump(t *testing.T) {
	raw := "wg0\toff\tpubkey\t51820\toff\n" +
		"wg0\tpeerkey\t(none)\t1.2.3.4:51820\t10.0.0.2/32\t100\t1000\t2000\t25\n"
	devs := parseWGDump(raw, time.Unix(200, 0))
	if len(devs) != 1 || devs[0].name != "wg0" || len(devs[0].peers) != 1 || devs[0].rx != 1000 {
		t.Fatalf("%+v", devs)
	}
	w := &wireguardCollector{
		cfg: wireguardConfig{Command: "wg", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := w.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := w.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseSambaProfile(t *testing.T) {
	raw := "syscall_asys_pread_count: 10\nsyscall_asys_pread_bytes: 4096\nsmb2_read_count: 5\nsmb2_read_inbytes: 8\nsmb2_read_outbytes: 4096\n"
	m := parseSambaProfile([]byte(raw))
	if m["syscall_asys_pread_count"] != 10 || m["smb2_read_outbytes"] != 4096 {
		t.Fatalf("%v", m)
	}
	s := &sambaCollector{
		cfg: sambaConfig{Command: "smbstatus", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := s.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseFreeRADIUSStatus(t *testing.T) {
	raw := "FreeRADIUS-Total-Access-Requests = 10\nFreeRADIUS-Total-Access-Accepts = 8\nFreeRADIUS-Total-Access-Rejects = 2\nFreeRADIUS-Total-Auth-Responses = 10\nFreeRADIUS-Total-Accounting-Requests = 1\nFreeRADIUS-Total-Accounting-Responses = 1\n"
	m := parseFreeRADIUSStatus([]byte(raw))
	if m["access-requests"] != 10 || m["access-rejects"] != 2 {
		t.Fatalf("%v", m)
	}
	f := &freeradiusCollector{
		cfg: freeradiusConfig{Command: "radclient", Address: "127.0.0.1:18121", Secret: "adminsecret", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil },
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := f.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := f.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestTorControl(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				br := make([]byte, 256)
				n, _ := c.Read(br)
				if !strings.Contains(string(br[:n]), "AUTHENTICATE") {
					return
				}
				_, _ = io.WriteString(c, "250 OK\n")
				n, _ = c.Read(br)
				if !strings.Contains(string(br[:n]), "GETINFO") {
					return
				}
				_, _ = io.WriteString(c, "250-traffic/read=1000\n250-traffic/written=2000\n250-uptime=42\n250 OK\n")
			}(c)
		}
	}()
	tr := &torCollector{cfg: torConfig{Address: ln.Addr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := tr.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := tr.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestM10AutoDisable(t *testing.T) {
	p := &postfixCollector{cfg: postfixConfig{Command: "postqueue-missing", Timeout: 50 * time.Millisecond}}
	if err := p.Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected postfix disable")
	}
	w := &weblogCollector{cfg: weblogConfig{Path: "/no/such/access.log"}}
	if err := w.Init(registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)); err == nil {
		t.Fatal("expected weblog disable")
	}
}
