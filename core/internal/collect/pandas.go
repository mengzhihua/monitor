package collect

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// pandasConfig is collectors.modules.pandas (python.d pandas without eval).
// Each job fetches JSON/CSV and turns the first row of numeric columns into dimensions.
type pandasConfig struct {
	Jobs    []pandasJob   `yaml:"jobs"`
	Timeout time.Duration `yaml:"timeout"`
}

type pandasJob struct {
	Name   string `yaml:"name"`
	URL    string `yaml:"url"`
	Format string `yaml:"format"` // json (default) or csv
	Title  string `yaml:"title"`
	Units  string `yaml:"units"`
	Family string `yaml:"family"`
}

type pandasCollector struct {
	cfg    pandasConfig
	client *http.Client
	get    func(ctx context.Context, url string) ([]byte, error)
}

func init() {
	Register("pandas", func() Collector { return &pandasCollector{} })
}

func (p *pandasCollector) Name() string { return "pandas" }

func (p *pandasCollector) Configure(decode func(v any) error) error {
	if err := decode(&p.cfg); err != nil {
		return err
	}
	if p.cfg.Timeout <= 0 {
		p.cfg.Timeout = 5 * time.Second
	}
	return nil
}

func (p *pandasCollector) Init(reg *registry.Registry) error {
	if p.cfg.Timeout == 0 && len(p.cfg.Jobs) == 0 {
		if err := p.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if len(p.cfg.Jobs) == 0 {
		return fmt.Errorf("pandas: no jobs")
	}
	if p.get == nil {
		p.client = &http.Client{Timeout: p.cfg.Timeout}
		p.get = p.httpGet
	}
	for i := range p.cfg.Jobs {
		j := &p.cfg.Jobs[i]
		if j.Name == "" {
			j.Name = fmt.Sprintf("job%d", i)
		}
		if j.URL == "" {
			return fmt.Errorf("pandas job %q: empty url", j.Name)
		}
		if j.Format == "" {
			j.Format = "json"
		}
		if j.Title == "" {
			j.Title = "pandas " + j.Name
		}
		if j.Units == "" {
			j.Units = "value"
		}
		if j.Family == "" {
			j.Family = "pandas"
		}
		row, err := p.row(context.Background(), j)
		if err != nil {
			return err
		}
		if len(row) == 0 {
			return fmt.Errorf("pandas job %q: no numeric columns", j.Name)
		}
		dims := make([]*registry.Dimension, 0, len(row))
		for k := range row {
			dims = append(dims, &registry.Dimension{ID: sanitizeID(k)})
		}
		id := "pandas." + sanitizeID(j.Name)
		reg.AddChart(&registry.Chart{
			ID: id, Context: id, Title: j.Title, Units: j.Units, Family: j.Family,
			Priority: 64500 + i, Plugin: "python.d", Module: "pandas", Dimensions: dims,
		})
	}
	return nil
}

func (p *pandasCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for i := range p.cfg.Jobs {
		j := &p.cfg.Jobs[i]
		row, err := p.row(ctx, j)
		if err != nil {
			return err
		}
		vals := map[string]float64{}
		for k, v := range row {
			vals[sanitizeID(k)] = v
		}
		_ = reg.Collect("pandas."+sanitizeID(j.Name), now, vals)
	}
	return nil
}

func (p *pandasCollector) httpGet(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 2<<20))
}

func (p *pandasCollector) row(ctx context.Context, j *pandasJob) (map[string]float64, error) {
	b, err := p.get(ctx, j.URL)
	if err != nil {
		return nil, fmt.Errorf("pandas %s: %w", j.Name, err)
	}
	switch strings.ToLower(j.Format) {
	case "csv":
		return parsePandasCSV(b)
	default:
		return parsePandasJSON(b)
	}
}

func parsePandasJSON(b []byte) (map[string]float64, error) {
	s := strings.TrimSpace(string(b))
	if strings.HasPrefix(s, "[") {
		var arr []map[string]any
		if err := json.Unmarshal(b, &arr); err != nil {
			return nil, err
		}
		if len(arr) == 0 {
			return nil, fmt.Errorf("empty array")
		}
		return numericMap(arr[0]), nil
	}
	var obj map[string]any
	if err := json.Unmarshal(b, &obj); err != nil {
		return nil, err
	}
	out := numericMap(obj)
	if len(out) == 0 {
		return nil, fmt.Errorf("no numeric fields")
	}
	return out, nil
}

func numericMap(obj map[string]any) map[string]float64 {
	out := map[string]float64{}
	for k, v := range obj {
		switch t := v.(type) {
		case float64:
			out[k] = t
		case json.Number:
			f, _ := t.Float64()
			out[k] = f
		case string:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
				out[k] = f
			}
		}
	}
	return out
}

func parsePandasCSV(b []byte) (map[string]float64, error) {
	r := csv.NewReader(strings.NewReader(string(b)))
	rows, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 || len(rows[0]) == 0 {
		return nil, fmt.Errorf("csv needs header and one data row")
	}
	out := map[string]float64{}
	hdr := rows[0]
	data := rows[1]
	for i, h := range hdr {
		if i >= len(data) {
			break
		}
		f, err := strconv.ParseFloat(strings.TrimSpace(data[i]), 64)
		if err != nil {
			continue
		}
		out[strings.TrimSpace(h)] = f
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no numeric csv columns")
	}
	return out, nil
}
