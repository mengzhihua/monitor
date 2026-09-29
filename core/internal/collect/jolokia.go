package collect

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/preprocess"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

type jolokiaConfig struct {
	Timeout time.Duration `yaml:"timeout"`
	Jobs    []jolokiaJob  `yaml:"jobs"`
}

type jolokiaJob struct {
	Name      string            `yaml:"name"`
	URL       string            `yaml:"url"`
	MBean     string            `yaml:"mbean"`
	Attribute string            `yaml:"attribute"`
	Path      string            `yaml:"path"`
	Steps     []preprocess.Step `yaml:"preprocess"`
}

type jolokiaCollector struct {
	cfg    jolokiaConfig
	client *http.Client
	prev   map[string]float64
}

func init() { Register("jolokia", func() Collector { return &jolokiaCollector{} }) }

func (j *jolokiaCollector) Name() string { return "jolokia" }

func (j *jolokiaCollector) Configure(decode func(v any) error) error {
	if err := decode(&j.cfg); err != nil {
		return err
	}
	if j.cfg.Timeout <= 0 {
		j.cfg.Timeout = 5 * time.Second
	}
	for _, job := range j.cfg.Jobs {
		if !validItemName(job.Name) || job.URL == "" || job.MBean == "" || job.Attribute == "" || strings.ContainsAny(job.MBean+job.Attribute+job.Path, "?\n#") {
			return fmt.Errorf("jolokia: job %q needs name, url, mbean, attribute", job.Name)
		}
	}
	return nil
}

func (j *jolokiaCollector) Init(reg *registry.Registry) error {
	if j.cfg.Timeout == 0 && len(j.cfg.Jobs) == 0 {
		if err := j.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(j.cfg.Jobs) == 0 {
		return fmt.Errorf("jolokia: no jobs")
	}
	j.client = &http.Client{Timeout: j.cfg.Timeout}
	j.prev = map[string]float64{}
	for _, job := range j.cfg.Jobs {
		reg.AddChart(&registry.Chart{ID: "jolokia." + sanitizeID(job.Name), Context: "jolokia.value", Title: "Jolokia " + job.Name,
			Units: "value", Family: "jmx", Plugin: "jolokia", Module: "jolokia", Priority: 56300,
			Labels:     map[string]string{"mbean": job.MBean, "attribute": job.Attribute},
			Dimensions: []*registry.Dimension{{ID: "value"}}})
	}
	return nil
}

func (j *jolokiaCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for _, job := range j.cfg.Jobs {
		u := stringsTrimRightSlash(job.URL) + "/read/" + job.MBean + "/" + job.Attribute
		if job.Path != "" {
			u += "/" + job.Path
		}
		_, body, _, err := httpText(ctx, j.client, http.MethodGet, u, nil, "", 1<<20)
		steps := job.Steps
		if len(steps) == 0 {
			steps = []preprocess.Step{{Type: "jsonpath", Path: "$.value"}}
		}
		prev := math.NaN()
		if v, ok := j.prev[job.Name]; ok {
			prev = v
		}
		value, next, perr := preprocess.Apply(steps, body, prev)
		if err != nil || perr != nil {
			continue
		}
		j.prev[job.Name] = next
		_ = reg.Collect("jolokia."+sanitizeID(job.Name), now, map[string]float64{"value": value})
	}
	return nil
}

func stringsTrimRightSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}
