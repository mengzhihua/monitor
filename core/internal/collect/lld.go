package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// lld turns a Zabbix-style discovery document into charts. Entities are
// {"data":[{"{#MACRO}":"value"}]} or a raw array. Prototypes substitute
// {#MACRO} in the title, key, value, and units. layout "item" makes one chart
// per entity. layout "graph" puts every entity on one chart, which can be a pie.
type lldConfig struct {
	Rules []lldRule `yaml:"rules"`
}

type lldRule struct {
	Name       string         `yaml:"name"`
	URL        string         `yaml:"url"`
	Data       string         `yaml:"data"`
	Prototypes []lldPrototype `yaml:"prototypes"`
	prototypes []lldPrepared  `yaml:"-"`
}

type lldPrototype struct {
	Name        string `yaml:"name"`
	Title       string `yaml:"title"`
	Key         string `yaml:"key"`
	Value       string `yaml:"value"`
	Units       string `yaml:"units"`
	Type        string `yaml:"type"`
	Layout      string `yaml:"layout"`
	FilterMacro string `yaml:"filter_macro"`
	Filter      string `yaml:"filter"`
}

type lldPrepared struct {
	lldPrototype
	filter    *regexp.Regexp
	chartType registry.ChartType
}

type lldCollector struct {
	cfg    lldConfig
	client *http.Client
}

var lldMacro = regexp.MustCompile(`\{#([A-Z0-9_]{1,32})\}`)

func init() { Register("lld", func() Collector { return &lldCollector{} }) }

func (c *lldCollector) Name() string { return "lld" }

func (c *lldCollector) Configure(decode func(v any) error) error {
	if err := decode(&c.cfg); err != nil {
		return err
	}
	if len(c.cfg.Rules) > 16 {
		return fmt.Errorf("lld: at most 16 rules")
	}
	for i := range c.cfg.Rules {
		rule := &c.cfg.Rules[i]
		if !validItemName(rule.Name) {
			return fmt.Errorf("lld: rule name %q is invalid", rule.Name)
		}
		if (rule.URL == "") == (rule.Data == "") {
			return fmt.Errorf("lld: rule %q needs a url or inline data", rule.Name)
		}
		if rule.URL != "" {
			if _, err := lldURL(rule.URL); err != nil {
				return fmt.Errorf("lld: rule %q: %w", rule.Name, err)
			}
		}
		if rule.Data != "" {
			if _, err := parseLLDDocument(rule.Data); err != nil {
				return fmt.Errorf("lld: rule %q: %w", rule.Name, err)
			}
		}
		if len(rule.Prototypes) == 0 || len(rule.Prototypes) > 8 {
			return fmt.Errorf("lld: rule %q needs 1 to 8 prototypes", rule.Name)
		}
		rule.prototypes = nil
		for _, proto := range rule.Prototypes {
			prepared, err := prepareLLDPrototype(proto)
			if err != nil {
				return fmt.Errorf("lld: rule %q: %w", rule.Name, err)
			}
			rule.prototypes = append(rule.prototypes, prepared)
		}
	}
	return nil
}

func (c *lldCollector) Init(_ *registry.Registry) error {
	if len(c.cfg.Rules) == 0 {
		return fmt.Errorf("lld: no rules")
	}
	c.client = &http.Client{Timeout: 5 * time.Second}
	return nil
}

func (c *lldCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	for i := range c.cfg.Rules {
		if ctx.Err() != nil {
			return nil
		}
		rule := &c.cfg.Rules[i]
		text := rule.Data
		if rule.URL != "" {
			_, body, _, err := httpText(ctx, c.client, http.MethodGet, rule.URL, nil, "", 256*1024)
			if err != nil {
				continue
			}
			text = body
		}
		entities, err := parseLLDDocument(text)
		if err != nil {
			continue
		}
		for _, proto := range rule.prototypes {
			c.applyPrototype(reg, now, rule.Name, proto, entities)
		}
	}
	return nil
}

func (c *lldCollector) applyPrototype(reg *registry.Registry, now time.Time, rule string, proto lldPrepared, entities []map[string]string) {
	if proto.Layout == "graph" {
		c.applyGraph(reg, now, rule, proto, entities)
		return
	}
	for _, entity := range entities {
		if !lldAccept(proto, entity) {
			continue
		}
		key, ok := expandLLD(proto.Key, entity)
		title, titleOK := expandLLD(firstNonEmpty(proto.Title, proto.Key), entity)
		valueText, valueOK := expandLLD(proto.Value, entity)
		units, unitsOK := expandLLD(firstNonEmpty(proto.Units, "value"), entity)
		if !ok || !titleOK || !valueOK || !unitsOK || key == "" || len(key) > 128 || len(title) > 128 {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(valueText), 64)
		if err != nil {
			continue
		}
		id := "lld." + sanitizeID(rule) + "." + sanitizeID(proto.Name) + "." + sanitizeID(key)
		ch := reg.AddChart(&registry.Chart{
			ID: id, Context: "lld." + sanitizeID(rule), Title: title, Units: units, Type: proto.chartType,
			Priority: 56600, Family: "lld", Plugin: "lld", Module: "lld",
			Labels: lldLabels(entity), Dimensions: []*registry.Dimension{{ID: "value", Name: title}},
		})
		_ = reg.Collect(ch.ID, now, map[string]float64{"value": value})
	}
}

func (c *lldCollector) applyGraph(reg *registry.Registry, now time.Time, rule string, proto lldPrepared, entities []map[string]string) {
	id := "lld." + sanitizeID(rule) + "." + sanitizeID(proto.Name)
	var ch *registry.Chart
	values := map[string]float64{}
	for _, entity := range entities {
		if !lldAccept(proto, entity) || len(values) >= 64 {
			continue
		}
		key, ok := expandLLD(proto.Key, entity)
		title, titleOK := expandLLD(firstNonEmpty(proto.Title, proto.Key), entity)
		valueText, valueOK := expandLLD(proto.Value, entity)
		units, unitsOK := expandLLD(firstNonEmpty(proto.Units, "value"), entity)
		if !ok || !titleOK || !valueOK || !unitsOK || key == "" || len(key) > 128 {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(valueText), 64)
		if err != nil {
			continue
		}
		dim := sanitizeID(key)
		if dim == "" {
			continue
		}
		if _, exists := values[dim]; exists {
			continue
		}
		if ch == nil {
			ch = reg.AddChart(&registry.Chart{
				ID: id, Context: "lld." + sanitizeID(rule), Title: firstNonEmpty(stripLLD(proto.Title), proto.Name),
				Units: units, Type: proto.chartType, Priority: 56600,
				Family: "lld", Plugin: "lld", Module: "lld", Labels: map[string]string{"rule": rule},
			})
		}
		if ch.Dimension(dim) == nil {
			if len(ch.Dims()) >= 64 {
				continue
			}
			ch.AddDimension(&registry.Dimension{ID: dim, Name: title})
		}
		values[dim] = value
	}
	if ch != nil && len(values) > 0 {
		_ = reg.Collect(ch.ID, now, values)
	}
}

func prepareLLDPrototype(proto lldPrototype) (lldPrepared, error) {
	if !validItemName(proto.Name) || proto.Key == "" || proto.Value == "" {
		return lldPrepared{}, fmt.Errorf("prototype %q needs a name, key, and value", proto.Name)
	}
	layout := proto.Layout
	if layout == "" {
		layout = "item"
	}
	if layout != "item" && layout != "graph" {
		return lldPrepared{}, fmt.Errorf("prototype %q layout must be item or graph", proto.Name)
	}
	proto.Layout = layout
	chartType, err := lldChartType(proto.Type)
	if err != nil {
		return lldPrepared{}, err
	}
	var filter *regexp.Regexp
	if proto.Filter != "" || proto.FilterMacro != "" {
		if !lldMacro.MatchString(proto.FilterMacro) || proto.FilterMacro != lldMacro.FindString(proto.FilterMacro) {
			return lldPrepared{}, fmt.Errorf("prototype %q filter_macro must be one {#MACRO}", proto.Name)
		}
		if len(proto.Filter) > 128 {
			return lldPrepared{}, fmt.Errorf("prototype %q filter is too long", proto.Name)
		}
		filter, err = regexp.Compile(proto.Filter)
		if err != nil {
			return lldPrepared{}, fmt.Errorf("prototype %q filter: %w", proto.Name, err)
		}
	}
	return lldPrepared{lldPrototype: proto, filter: filter, chartType: chartType}, nil
}

func lldChartType(raw string) (registry.ChartType, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "line":
		return registry.Line, nil
	case "area":
		return registry.Area, nil
	case "stacked":
		return registry.Stacked, nil
	case "pie":
		return registry.Pie, nil
	default:
		return "", fmt.Errorf("unknown chart type %q", raw)
	}
}

func lldAccept(proto lldPrepared, entity map[string]string) bool {
	if proto.filter == nil {
		return true
	}
	return proto.filter.MatchString(entity[proto.FilterMacro])
}

func expandLLD(text string, entity map[string]string) (string, bool) {
	ok := true
	out := lldMacro.ReplaceAllStringFunc(text, func(token string) string {
		value, found := entity[token]
		if !found {
			ok = false
			return token
		}
		return value
	})
	return out, ok
}

func stripLLD(text string) string {
	return strings.TrimSpace(lldMacro.ReplaceAllString(text, ""))
}

func lldLabels(entity map[string]string) map[string]string {
	keys := make([]string, 0, len(entity))
	for key := range entity {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	labels := map[string]string{}
	for _, key := range keys {
		if len(labels) >= 8 {
			break
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "{#"), "}")
		if name == "" || len(name) > 32 {
			continue
		}
		value := entity[key]
		if len(value) > 128 {
			value = value[:128]
		}
		labels[strings.ToLower(name)] = value
	}
	return labels
}

func lldURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if len(raw) > 2048 || err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, fmt.Errorf("url must be http(s) without userinfo or fragment")
	}
	return u, nil
}

func parseLLDDocument(text string) ([]map[string]string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("empty discovery document")
	}
	if len(text) > 256*1024 {
		return nil, fmt.Errorf("discovery document exceeds 256KiB")
	}
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var raw any
	if err := dec.Decode(&raw); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("discovery document has trailing data")
	}
	var rows []any
	switch doc := raw.(type) {
	case []any:
		rows = doc
	case map[string]any:
		data, ok := doc["data"].([]any)
		if !ok {
			return nil, fmt.Errorf("discovery document needs a data array")
		}
		rows = data
	default:
		return nil, fmt.Errorf("discovery document must be an array or object")
	}
	if len(rows) > 128 {
		rows = rows[:128]
	}
	out := make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		obj, ok := row.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("discovery entity must be an object")
		}
		entity := make(map[string]string, len(obj))
		for key, value := range obj {
			scalar, ok := lldScalar(value)
			if !ok {
				continue
			}
			entity[key] = scalar
		}
		out = append(out, entity)
	}
	return out, nil
}

func lldScalar(value any) (string, bool) {
	switch n := value.(type) {
	case string:
		return n, true
	case json.Number:
		return n.String(), true
	case bool:
		if n {
			return "1", true
		}
		return "0", true
	default:
		return "", false
	}
}
