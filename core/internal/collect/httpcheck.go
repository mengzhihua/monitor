package collect

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// httpcheckConfig is collectors.modules.httpcheck (Netdata go.d httpcheck).
type httpcheckConfig struct {
	Jobs    []httpJob     `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type httpJob struct {
	Name     string            `yaml:"name"`
	URL      string            `yaml:"url"`
	Timeout  time.Duration     `yaml:"timeout"`
	Method   string            `yaml:"method"`
	Headers  map[string]string `yaml:"headers"`
	StatusOK []int             `yaml:"status_accepted"` // default [200..399]
	TLS      struct {
		Insecure bool `yaml:"insecure_skip_verify"`
	} `yaml:"tls"`
}

type httpcheckCollector struct {
	cfg    httpcheckConfig
	client *http.Client
}

func init() {
	Register("httpcheck", func() Collector { return &httpcheckCollector{} })
}

func (h *httpcheckCollector) Name() string { return "httpcheck" }

func (h *httpcheckCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (h *httpcheckCollector) Init(reg *registry.Registry) error {
	if h.cfg.Timeout <= 0 {
		if err := h.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(h.cfg.Jobs) == 0 {
		return fmt.Errorf("no jobs configured")
	}
	h.client = &http.Client{Timeout: h.cfg.Timeout, Transport: &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}}
	for i := range h.cfg.Jobs {
		j := &h.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.URL == "" {
			return fmt.Errorf("httpcheck job %q: empty url", j.Name)
		}
		if j.Method == "" {
			j.Method = http.MethodGet
		}
		if j.Timeout <= 0 {
			j.Timeout = h.cfg.Timeout
		}
		if len(j.StatusOK) == 0 {
			j.StatusOK = []int{200, 201, 202, 203, 204, 301, 302, 303, 307, 308}
		}
		h.addCharts(reg, j.Name, strings.HasPrefix(j.URL, "https://"))
	}
	return nil
}

func (h *httpcheckCollector) addCharts(reg *registry.Registry, name string, tls bool) {
	id := sanitizeID(name)
	lbl := map[string]string{"job": name}
	mk := func(suffix, title, units string, prio int, dims ...*registry.Dimension) {
		reg.AddChart(&registry.Chart{ID: "httpcheck." + suffix + "." + id, Context: "httpcheck." + suffix, Family: name,
			Title: title, Units: units, Priority: prio, Plugin: "httpcheck", Module: "httpcheck", Labels: lbl, Dimensions: dims})
	}
	mk("status", "HTTP check status", "boolean", 50000, &registry.Dimension{ID: "success"}, &registry.Dimension{ID: "failure"})
	mk("responsetime", "HTTP check response time", "ms", 50001, &registry.Dimension{ID: "time"})
	mk("response_length", "HTTP check response length", "bytes", 50002, &registry.Dimension{ID: "length"})
	mk("status_code", "HTTP check status code", "code", 50003, &registry.Dimension{ID: "code"})
	if tls {
		mk("cert_expiry", "TLS certificate expiry", "days", 50004, &registry.Dimension{ID: "expiry"})
	}
}

func (h *httpcheckCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	var last error
	ok := 0
	for _, j := range h.cfg.Jobs {
		if err := h.probe(ctx, reg, now, j); err != nil {
			last = err
		} else {
			ok++
		}
	}
	if ok == 0 && last != nil {
		return last
	}
	return nil
}

func (h *httpcheckCollector) probe(ctx context.Context, reg *registry.Registry, now time.Time, j httpJob) error {
	id := sanitizeID(j.Name)
	start := time.Now()
	status, length, code, expiry, err := h.do(ctx, j)
	elapsed := float64(time.Since(start).Microseconds()) / 1000
	success, failure := 0.0, 1.0
	if err == nil && status {
		success, failure = 1, 0
	}
	_ = reg.Collect("httpcheck.status."+id, now, map[string]float64{"success": success, "failure": failure})
	_ = reg.Collect("httpcheck.responsetime."+id, now, map[string]float64{"time": elapsed})
	_ = reg.Collect("httpcheck.response_length."+id, now, map[string]float64{"length": float64(length)})
	_ = reg.Collect("httpcheck.status_code."+id, now, map[string]float64{"code": float64(code)})
	if _, ok := reg.Chart("httpcheck.cert_expiry." + id); ok {
		_ = reg.Collect("httpcheck.cert_expiry."+id, now, map[string]float64{"expiry": expiry})
	}
	return err
}

func (h *httpcheckCollector) do(ctx context.Context, j httpJob) (ok bool, length, code int, expiryDays float64, err error) {
	cctx, cancel := context.WithTimeout(ctx, j.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, j.Method, j.URL, nil)
	if err != nil {
		return false, 0, 0, 0, err
	}
	for k, v := range j.Headers {
		req.Header.Set(k, v)
	}
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: j.TLS.Insecure}} //nolint:gosec // operator opt-in
	client := &http.Client{Timeout: j.Timeout, Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return false, 0, 0, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	code = resp.StatusCode
	length = len(body)
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter
		expiryDays = exp.Sub(time.Now()).Hours() / 24
	}
	for _, s := range j.StatusOK {
		if code == s {
			return true, length, code, expiryDays, nil
		}
	}
	return false, length, code, expiryDays, fmt.Errorf("httpcheck %s: status %d", j.Name, code)
}
