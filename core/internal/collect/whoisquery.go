package collect

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// whoisqueryConfig is collectors.modules.whoisquery (Netdata go.d whoisquery).
type whoisqueryConfig struct {
	Jobs    []whoisJob    `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type whoisJob struct {
	Name    string        `yaml:"name"`
	Domain  string        `yaml:"domain"`
	Server  string        `yaml:"server"` // host:43
	Timeout time.Duration `yaml:"timeout"`
}

type whoisqueryCollector struct {
	cfg whoisqueryConfig
}

func init() {
	Register("whoisquery", func() Collector { return &whoisqueryCollector{} })
}

func (w *whoisqueryCollector) Name() string { return "whoisquery" }

func (w *whoisqueryCollector) Configure(decode func(v any) error) error {
	if err := decode(&w.cfg); err != nil {
		return err
	}
	if w.cfg.Timeout <= 0 {
		w.cfg.Timeout = 10 * time.Second
	}
	return nil
}

func (w *whoisqueryCollector) Init(reg *registry.Registry) error {
	if w.cfg.Timeout <= 0 {
		if err := w.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(w.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	for i := range w.cfg.Jobs {
		j := &w.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.Domain == "" {
			return fmt.Errorf("whoisquery job %q: empty domain", j.Name)
		}
		if j.Timeout <= 0 {
			j.Timeout = w.cfg.Timeout
		}
		if j.Server == "" {
			j.Server = whoisServer(j.Domain)
		}
		id := sanitizeID(j.Name)
		reg.AddChart(&registry.Chart{ID: "whoisquery.time_until_expiration." + id, Context: "whoisquery.time_until_expiration",
			Family: "whoisquery", Title: "Domain days until expiration " + j.Name, Units: "days",
			Priority: 49200, Plugin: "whoisquery", Module: "whoisquery", Labels: map[string]string{"domain": j.Domain},
			Dimensions: []*registry.Dimension{{ID: "days"}}})
	}
	return nil
}

func (w *whoisqueryCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var last error
	for _, j := range w.cfg.Jobs {
		days, err := whoisDays(ctx, j)
		id := "whoisquery.time_until_expiration." + sanitizeID(j.Name)
		if err != nil {
			last = err
			_ = reg.Collect(id, now, map[string]float64{"days": 0})
			continue
		}
		_ = reg.Collect(id, now, map[string]float64{"days": days})
	}
	return last
}

func whoisServer(domain string) string {
	tld := domain
	if i := strings.LastIndex(domain, "."); i >= 0 {
		tld = domain[i+1:]
	}
	switch strings.ToLower(tld) {
	case "com", "net":
		return "whois.verisign-grs.com:43"
	case "org":
		return "whois.pir.org:43"
	case "io":
		return "whois.nic.io:43"
	case "cn":
		return "whois.cnnic.cn:43"
	default:
		return "whois.iana.org:43"
	}
}

func whoisDays(ctx context.Context, j whoisJob) (float64, error) {
	body, err := whoisQuery(ctx, j.Server, j.Domain, j.Timeout)
	if err != nil {
		return 0, err
	}
	if referral := whoisReferral(body); referral != "" && !strings.Contains(j.Server, referral) {
		if b2, err2 := whoisQuery(ctx, referral, j.Domain, j.Timeout); err2 == nil {
			body = b2
		}
	}
	exp, err := parseWhoisExpiry(body)
	if err != nil {
		return 0, err
	}
	return time.Until(exp).Hours() / 24, nil
}

func whoisQuery(ctx context.Context, server, domain string, timeout time.Duration) (string, error) {
	if !strings.Contains(server, ":") {
		server += ":43"
	}
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", server)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	if _, err := fmt.Fprintf(conn, "%s\r\n", domain); err != nil {
		return "", err
	}
	var b strings.Builder
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		b.WriteString(sc.Text())
		b.WriteByte('\n')
		if b.Len() > 1<<20 {
			break
		}
	}
	return b.String(), sc.Err()
}

func whoisReferral(s string) string {
	for _, line := range strings.Split(s, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		if strings.HasPrefix(low, "whois:") || strings.HasPrefix(low, "registrar whois server:") {
			_, v, _ := strings.Cut(line, ":")
			v = strings.TrimSpace(v)
			if v != "" {
				if !strings.Contains(v, ":") {
					v += ":43"
				}
				return v
			}
		}
	}
	return ""
}

var whoisExpiryKeys = []string{
	"registry expiry date:",
	"registrar registration expiration date:",
	"expiry date:",
	"expire date:",
	"expiration date:",
	"expires on:",
	"expires:",
	"paid-till:",
	"expire:",
}

func parseWhoisExpiry(s string) (time.Time, error) {
	for _, line := range strings.Split(s, "\n") {
		low := strings.ToLower(strings.TrimSpace(line))
		var rest string
		ok := false
		for _, k := range whoisExpiryKeys {
			if strings.HasPrefix(low, k) {
				rest = strings.TrimSpace(line[len(k):])
				// original line may have different casing; cut on first ':'
				if _, v, cut := strings.Cut(line, ":"); cut {
					rest = strings.TrimSpace(v)
				}
				ok = true
				break
			}
		}
		if !ok || rest == "" {
			continue
		}
		rest = strings.Fields(rest)[0]
		rest = strings.Trim(rest, ".")
		for _, layout := range []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05-0700",
			"2006-01-02 15:04:05",
			"2006-01-02",
			"02-Jan-2006",
			"2006.01.02",
			"02.01.2006",
			"20060102",
		} {
			if t, err := time.Parse(layout, rest); err == nil {
				return t, nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("whois: no expiry date")
}
