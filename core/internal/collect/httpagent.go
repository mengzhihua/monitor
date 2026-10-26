package collect

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

type httpagentConfig struct {
	Timeout time.Duration  `yaml:"timeout"`
	Jobs    []httpagentJob `yaml:"jobs"`
}

type httpagentJob struct {
	Name            string            `yaml:"name"`
	URL             string            `yaml:"url"`
	Method          string            `yaml:"method"`
	Body            string            `yaml:"body"`
	Headers         map[string]string `yaml:"headers"`
	Steps           []preprocess.Step `yaml:"preprocess"`
	StatusCodes     []string          `yaml:"status_codes,omitempty"`
	Retrieve        string            `yaml:"retrieve,omitempty"`
	FollowRedirects *bool             `yaml:"follow_redirects,omitempty"`
	spans           []statusSpan
}

type statusSpan struct {
	lo, hi int
}

type httpagentCollector struct {
	cfg    httpagentConfig
	client *http.Client
	prev   map[string]float64
}

func init() { Register("httpagent", func() Collector { return &httpagentCollector{} }) }

func (h *httpagentCollector) Name() string { return "httpagent" }

func (h *httpagentCollector) Configure(decode func(v any) error) error {
	if err := decode(&h.cfg); err != nil {
		return err
	}
	if h.cfg.Timeout <= 0 {
		h.cfg.Timeout = 5 * time.Second
	}
	for i := range h.cfg.Jobs {
		j := &h.cfg.Jobs[i]
		if !validItemName(j.Name) || j.URL == "" {
			return fmt.Errorf("httpagent: job %q needs a name and url", j.Name)
		}
		spans, err := parseStatusCodes(j.StatusCodes)
		if err != nil {
			return fmt.Errorf("httpagent: job %q: %w", j.Name, err)
		}
		j.spans = spans
		switch strings.ToLower(strings.TrimSpace(j.Retrieve)) {
		case "", "body":
			j.Retrieve = "body"
		case "headers", "status":
			j.Retrieve = strings.ToLower(strings.TrimSpace(j.Retrieve))
		default:
			return fmt.Errorf("httpagent: job %q retrieve %q must be body, headers, or status", j.Name, j.Retrieve)
		}
	}
	return nil
}

func (h *httpagentCollector) Init(reg *registry.Registry) error {
	if len(h.cfg.Jobs) == 0 {
		return fmt.Errorf("httpagent: no jobs")
	}
	h.client = &http.Client{Timeout: h.cfg.Timeout}
	h.prev = map[string]float64{}
	for _, j := range h.cfg.Jobs {
		reg.AddChart(&registry.Chart{ID: "httpagent." + sanitizeID(j.Name), Context: "httpagent.value", Title: "HTTP agent " + j.Name,
			Units: "value", Family: "http", Plugin: "httpagent", Module: "httpagent", Priority: 56400,
			Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	return nil
}

func (h *httpagentCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, j := range h.cfg.Jobs {
		client := h.client
		if j.FollowRedirects != nil && !*j.FollowRedirects {
			clone := *h.client
			clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			client = &clone
		}
		res, err := httpExchange(ctx, client, j.Method, j.URL, j.Headers, j.Body, 1<<20)
		prev := math.NaN()
		if v, ok := h.prev[j.Name]; ok {
			prev = v
		}
		id := "httpagent." + sanitizeID(j.Name)
		if err != nil {
			continue
		}
		if !statusAllowed(j.spans, res.Code) {
			_ = noteUnsupported(reg, id, fmt.Errorf("%w: HTTP %d", preprocess.ErrNotSupported, res.Code))
			continue
		}
		text := res.Body
		switch j.Retrieve {
		case "status":
			text = strconv.Itoa(res.Code)
		case "headers":
			text = headerBlock(res.Header)
		}
		value, next, perr := preprocess.Apply(j.Steps, text, prev)
		if noteUnsupported(reg, id, perr) {
			continue
		}
		h.prev[j.Name] = next
		_ = reg.Collect(id, now, map[string]float64{"value": value})
	}
	return nil
}

func headerBlock(h http.Header) string {
	if len(h) == 0 {
		return ""
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		for _, v := range h[k] {
			b.WriteString(k)
			b.WriteString(": ")
			b.WriteString(v)
			b.WriteByte('\n')
		}
	}
	return b.String()
}
