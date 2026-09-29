package collect

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

type httpagentConfig struct {
	Timeout time.Duration  `yaml:"timeout"`
	Jobs    []httpagentJob `yaml:"jobs"`
}

type httpagentJob struct {
	Name    string            `yaml:"name"`
	URL     string            `yaml:"url"`
	Method  string            `yaml:"method"`
	Body    string            `yaml:"body"`
	Headers map[string]string `yaml:"headers"`
	Steps   []preprocess.Step `yaml:"preprocess"`
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
	for _, j := range h.cfg.Jobs {
		if !validItemName(j.Name) || j.URL == "" {
			return fmt.Errorf("httpagent: job %q needs a name and url", j.Name)
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
		_, body, _, err := httpText(ctx, h.client, j.Method, j.URL, j.Headers, j.Body, 1<<20)
		prev := math.NaN()
		if v, ok := h.prev[j.Name]; ok {
			prev = v
		}
		value, next, perr := preprocess.Apply(j.Steps, body, prev)
		if err != nil || perr != nil {
			continue
		}
		h.prev[j.Name] = next
		_ = reg.Collect("httpagent."+sanitizeID(j.Name), now, map[string]float64{"value": value})
	}
	return nil
}
