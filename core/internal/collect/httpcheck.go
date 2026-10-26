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
	Name            string            `yaml:"name"`
	URL             string            `yaml:"url"`
	Timeout         time.Duration     `yaml:"timeout"`
	Method          string            `yaml:"method"`
	Headers         map[string]string `yaml:"headers"`
	StatusOK        []int             `yaml:"status_accepted"` // legacy explicit codes
	StatusCodes     []string          `yaml:"status_codes,omitempty"`
	Contains        string            `yaml:"contains,omitempty"`
	FollowRedirects *bool             `yaml:"follow_redirects,omitempty"`
	spans           []statusSpan
	TLS             struct {
		Insecure bool `yaml:"insecure_skip_verify"`
	} `yaml:"tls"`
}

type httpcheckCollector struct {
	cfg            httpcheckConfig
	client         *http.Client
	insecureClient *http.Client
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
		if len(j.Contains) > 256 {
			return fmt.Errorf("httpcheck job %q: contains must be at most 256 bytes", j.Name)
		}
		spans, err := httpcheckStatusSpans(j)
		if err != nil {
			return fmt.Errorf("httpcheck job %q: %w", j.Name, err)
		}
		j.spans = spans
		h.addCharts(reg, j.Name, strings.HasPrefix(j.URL, "https://"))
	}
	h.Stop()
	h.client = newHTTPCheckClient(false)
	h.insecureClient = newHTTPCheckClient(true)
	return nil
}

func newHTTPCheckClient(insecure bool) *http.Client {
	return &http.Client{
		// Job deadlines are applied to each request's context. Keep separate
		// pools so opting one job out of certificate verification cannot
		// provide an unverified connection to a stricter job.
		Transport: &http.Transport{
			TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: insecure}, //nolint:gosec // operator opt-in
			MaxIdleConns:        32,
			MaxIdleConnsPerHost: 2,
			IdleConnTimeout:     30 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func (h *httpcheckCollector) Stop() {
	if h.client != nil {
		h.client.CloseIdleConnections()
	}
	if h.insecureClient != nil {
		h.insecureClient.CloseIdleConnections()
	}
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
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := h.probe(ctx, reg, now, j); err != nil {
			last = err
		} else {
			ok++
		}
	}
	if err := ctx.Err(); err != nil {
		return err
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
	// A cancelled collection is not evidence that the target is unhealthy.
	// Per-job timeouts still publish failures when the parent remains active.
	if ctx.Err() != nil {
		return ctx.Err()
	}
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
	resp, err := h.clientFor(j).Do(req)
	if err != nil {
		return false, 0, 0, 0, err
	}
	defer resp.Body.Close()
	var (
		body    []byte
		n       int64
		readErr error
	)
	if j.Contains != "" {
		body, readErr = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		n = int64(len(body))
	} else {
		// Only the byte count is used. Stream into Discard's pooled buffer rather
		// than retaining and repeatedly growing a response-sized byte slice.
		n, readErr = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	}
	code = resp.StatusCode
	length = int(n)
	if resp.TLS != nil && len(resp.TLS.PeerCertificates) > 0 {
		exp := resp.TLS.PeerCertificates[0].NotAfter
		expiryDays = exp.Sub(time.Now()).Hours() / 24
	}
	if readErr != nil {
		return false, length, code, expiryDays, fmt.Errorf("httpcheck %s: read response: %w", j.Name, readErr)
	}
	if !statusAllowed(j.spans, code) {
		return false, length, code, expiryDays, fmt.Errorf("httpcheck %s: status %d", j.Name, code)
	}
	if j.Contains != "" && !strings.Contains(string(body), j.Contains) {
		return false, length, code, expiryDays, fmt.Errorf("httpcheck %s: response missing required text", j.Name)
	}
	return true, length, code, expiryDays, nil
}

func httpcheckStatusSpans(j *httpJob) ([]statusSpan, error) {
	if len(j.StatusCodes) > 0 {
		return parseStatusCodes(j.StatusCodes)
	}
	codes := j.StatusOK
	if len(codes) == 0 {
		codes = []int{200, 201, 202, 203, 204, 301, 302, 303, 307, 308}
	}
	spans := make([]statusSpan, 0, len(codes))
	for _, c := range codes {
		if c < 100 || c > 599 {
			return nil, fmt.Errorf("status_accepted code %d out of range", c)
		}
		spans = append(spans, statusSpan{lo: c, hi: c})
	}
	return spans, nil
}

func (h *httpcheckCollector) clientFor(j httpJob) *http.Client {
	base := h.client
	if j.TLS.Insecure {
		base = h.insecureClient
	}
	if j.FollowRedirects == nil || !*j.FollowRedirects {
		return base
	}
	clone := *base
	clone.CheckRedirect = nil
	return &clone
}
