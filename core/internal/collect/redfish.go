package collect

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// redfishConfig keeps the original single-BMC configuration and adds independent
// targets. A target's timeout bounds the complete Systems walk, not each page.
type redfishConfig struct {
	TLS      CollectorTLS    `yaml:"tls"`
	URL      string          `yaml:"url"`
	User     string          `yaml:"user"`
	Password string          `yaml:"password"`
	Timeout  time.Duration   `yaml:"timeout"`
	Targets  []redfishTarget `yaml:"targets"`
}

type redfishTarget struct {
	Name     string        `yaml:"name"`
	URL      string        `yaml:"url"`
	User     string        `yaml:"user"`
	Password string        `yaml:"password"`
	Timeout  time.Duration `yaml:"timeout"`
	TLS      CollectorTLS  `yaml:"tls"`
}

type redfishEndpoint struct {
	cfg    redfishTarget
	client *http.Client
	base   *url.URL
}

type redfishCollector struct {
	cfg     redfishConfig
	targets []*redfishEndpoint
	seen    map[string]bool
}

const (
	redfishMaxTargets = 64
	redfishMaxPages   = 8
	redfishMaxSystems = 256
	redfishMaxBody    = 8 << 20
)

func init()                              { Register("redfish", func() Collector { return &redfishCollector{} }) }
func (r *redfishCollector) Name() string { return "redfish" }

func (r *redfishCollector) Configure(decode func(v any) error) error {
	if err := decode(&r.cfg); err != nil {
		return err
	}
	if r.cfg.Timeout <= 0 {
		r.cfg.Timeout = 5 * time.Second
	}
	if r.cfg.Targets != nil && r.cfg.URL != "" {
		return errors.New("redfish: configure either url or targets, not both")
	}
	if len(r.cfg.Targets) > redfishMaxTargets {
		return fmt.Errorf("redfish: at most %d targets", redfishMaxTargets)
	}
	names := map[string]bool{}
	for i := range r.cfg.Targets {
		t := &r.cfg.Targets[i]
		if !validRedfishName(t.Name) || names[t.Name] {
			return fmt.Errorf("redfish: target names must be unique letters, digits, underscores or hyphens (1-48 characters)")
		}
		names[t.Name] = true
		if _, err := redfishBaseURL(t.URL); err != nil {
			return fmt.Errorf("redfish target %s: %w", t.Name, err)
		}
		if t.Timeout == 0 {
			t.Timeout = r.cfg.Timeout
		}
		if t.Timeout < 0 || t.Timeout > time.Minute {
			return fmt.Errorf("redfish target %s: timeout must be positive and at most 1m", t.Name)
		}
	}
	return nil
}

func validRedfishName(s string) bool {
	if len(s) < 1 || len(s) > 48 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

func redfishBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimRight(raw, "/"))
	if len(raw) > 2048 || err != nil || u == nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("URL must be an HTTP(S) service root without embedded credentials, query or fragment")
	}
	return u, nil
}

func (r *redfishCollector) Init(reg *registry.Registry) error {
	if err := r.Configure(func(any) error { return nil }); err != nil {
		return err
	}
	r.Stop()
	r.targets = nil
	r.seen = map[string]bool{}
	cfgs := r.cfg.Targets
	if cfgs == nil {
		raw := r.cfg.URL
		if raw == "" {
			raw = "https://127.0.0.1/redfish/v1"
		}
		cfgs = []redfishTarget{{URL: raw, User: r.cfg.User, Password: r.cfg.Password, Timeout: r.cfg.Timeout, TLS: r.cfg.TLS}}
	}
	for _, cfg := range cfgs {
		base, err := redfishBaseURL(cfg.URL)
		if err != nil {
			r.Stop()
			return err
		}
		client, err := collectorHTTPClient(cfg.Timeout, cfg.TLS)
		if err != nil {
			r.Stop()
			return err
		}
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("redfish: too many redirects")
			}
			if !redfishSameOrigin(base, req.URL) || req.URL.User != nil {
				return errors.New("redfish: cross-origin redirect rejected")
			}
			return nil
		}
		ep := &redfishEndpoint{cfg: cfg, client: client, base: base}
		r.targets = append(r.targets, ep)
		if cfg.Name != "" {
			r.addChart(reg, "redfish.target_status."+cfg.Name, "redfish.target_status", "Redfish target collection availability", "state", map[string]string{"device": cfg.Name, "endpoint": base.Host}, "up", "down")
		}
	}
	// Explicit fleets must start even when one BMC is temporarily unavailable.
	// The legacy default retains its autodetection behaviour.
	if r.cfg.Targets == nil {
		_, err := r.targets[0].systems(context.Background())
		if err != nil {
			r.Stop()
			return err
		}
	}
	return nil
}

func (r *redfishCollector) Stop() {
	for _, ep := range r.targets {
		ep.client.CloseIdleConnections()
	}
}

func (r *redfishCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	type result struct {
		systems   []redfishSys
		err       error
		completed bool
	}
	results := make([]result, len(r.targets))
	jobs := make(chan int)
	var wg sync.WaitGroup
	for n := 0; n < min(4, len(r.targets)); n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results[i].systems, results[i].err = r.targets[i].systems(ctx)
				results[i].completed = ctx.Err() == nil
			}
		}()
	}
enqueue:
	for i := range r.targets {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break enqueue
		}
	}
	close(jobs)
	wg.Wait()
	var errs []error
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	for i, result := range results {
		// Keep completed targets even if another target consumes the parent
		// deadline. Unstarted or interrupted targets publish no false failure.
		if !result.completed {
			continue
		}
		ep := r.targets[i]
		if result.err != nil {
			errs = append(errs, fmt.Errorf("redfish target %s: %w", ep.cfg.Name, result.err))
		}
		if ep.cfg.Name != "" {
			_ = reg.Collect("redfish.target_status."+ep.cfg.Name, now, map[string]float64{"up": bool01(result.err == nil), "down": bool01(result.err != nil)})
		}
		for _, s := range result.systems {
			r.publish(reg, ep, s, now)
		}
	}
	return errors.Join(errs...)
}

type redfishSys struct {
	id, source, health, power, manufacturer, model string
	processors, memory                             *float64
}

func (r *redfishCollector) addChart(reg *registry.Registry, id, context, title, units string, labels map[string]string, dims ...string) {
	if r.seen[id] {
		return
	}
	r.seen[id] = true
	ch := &registry.Chart{ID: id, Context: context, Title: title, Units: units, Priority: 57500, Labels: labels, Family: "redfish", Plugin: "redfish", Module: "redfish"}
	for _, dim := range dims {
		ch.Dimensions = append(ch.Dimensions, &registry.Dimension{ID: dim})
	}
	reg.AddChart(ch)
}

func (r *redfishCollector) publish(reg *registry.Registry, ep *redfishEndpoint, s redfishSys, now time.Time) {
	id := sanitizeID(s.id)
	labels := map[string]string{"system": s.id, "endpoint": ep.base.Host}
	if s.manufacturer != "" {
		labels["manufacturer"] = s.manufacturer
	}
	if s.model != "" {
		labels["model"] = s.model
	}
	if ep.cfg.Name != "" {
		// Resource identity distinguishes separate systems with duplicate display IDs.
		sum := sha256.Sum256([]byte(s.source))
		id = ep.cfg.Name + "." + id + "_" + hex.EncodeToString(sum[:8])
		labels["device"] = ep.cfg.Name
	}
	chartID := "redfish.system_health_state." + id
	r.addChart(reg, chartID, "redfish.system_health_state", "System health", "state", labels, "ok", "warning", "critical", "unknown")
	h := strings.ToLower(s.health)
	_ = reg.Collect(chartID, now, map[string]float64{"ok": bool01(h == "ok"), "warning": bool01(h == "warning"), "critical": bool01(h == "critical"), "unknown": bool01(h != "ok" && h != "warning" && h != "critical")})
	if s.power != "" {
		powerID := "redfish.system_power_state." + id
		r.addChart(reg, powerID, "redfish.system_power_state", "System power state", "state", labels, "on", "off", "powering_on", "powering_off", "paused", "unknown")
		states := map[string]string{"On": "on", "Off": "off", "PoweringOn": "powering_on", "PoweringOff": "powering_off", "Paused": "paused"}
		value := states[s.power]
		if value == "" {
			value = "unknown"
		}
		values := map[string]float64{"on": 0, "off": 0, "powering_on": 0, "powering_off": 0, "paused": 0, "unknown": 0}
		values[value] = 1
		_ = reg.Collect(powerID, now, values)
	}
	for _, metric := range []struct {
		name, title, units string
		value              *float64
	}{{"processors", "System processor count", "processors", s.processors}, {"memory", "System total memory", "GiB", s.memory}} {
		if metric.value == nil {
			continue
		}
		chartID := "redfish.system_" + metric.name + "." + id
		r.addChart(reg, chartID, "redfish.system_"+metric.name, metric.title, metric.units, labels, "value")
		_ = reg.Collect(chartID, now, map[string]float64{"value": *metric.value})
	}
}

func redfishSameOrigin(a, b *url.URL) bool {
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		if u.Scheme == "https" {
			return "443"
		}
		return "80"
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

func (ep *redfishEndpoint) resolve(from *url.URL, ref string) (*url.URL, error) {
	u, err := url.Parse(ref)
	if err != nil || ref == "" || len(ref) > 2048 {
		return nil, errors.New("invalid resource link")
	}
	u = from.ResolveReference(u)
	if !redfishSameOrigin(ep.base, u) || u.User != nil || u.Fragment != "" {
		return nil, errors.New("cross-origin or invalid resource link rejected")
	}
	return u, nil
}

func (ep *redfishEndpoint) get(ctx context.Context, u *url.URL) (map[string]any, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, 0, errors.New("invalid resource request")
	}
	if ep.cfg.User != "" {
		req.SetBasicAuth(ep.cfg.User, ep.cfg.Password)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := ep.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		// Transport URL errors may contain malicious redirect userinfo. Do not log it.
		return nil, 0, errors.New("request failed (network, TLS or redirect)")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, redfishMaxBody+1))
	if err != nil {
		return nil, resp.StatusCode, errors.New("response read failed")
	}
	if len(body) > redfishMaxBody {
		return nil, resp.StatusCode, errors.New("response exceeds 8 MiB")
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return nil, resp.StatusCode, errors.New("invalid JSON object")
	}
	return obj, resp.StatusCode, nil
}

func (ep *redfishEndpoint) systems(parent context.Context) ([]redfishSys, error) {
	ctx, cancel := context.WithTimeout(parent, ep.cfg.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	first := *ep.base
	first.Path = strings.TrimRight(first.Path, "/") + "/Systems"
	first.RawPath = ""
	current := &first
	var out []redfishSys
	var errs []error
	pages, resources := map[string]bool{}, map[string]bool{}
	for page := 0; page < redfishMaxPages; page++ {
		if pages[current.String()] {
			return out, errors.Join(append(errs, errors.New("pagination cycle detected"))...)
		}
		pages[current.String()] = true
		obj, status, err := ep.get(ctx, current)
		if err != nil && page == 0 && (status == 404 || status == 405) {
			single := *current
			single.Path += "/1"
			obj, _, err = ep.get(ctx, &single)
			if err != nil {
				return out, err
			}
			if !validRedfishIdentity(obj) {
				return out, errors.New("system response has no identity")
			}
			return []redfishSys{parseRedfishSystem(obj, single.RequestURI())}, nil
		}
		if err != nil {
			return out, errors.Join(append(errs, err)...)
		}
		rawMembers, collection := obj["Members"]
		if !collection {
			if page == 0 && nestString(obj, "Id") != "" && validRedfishIdentity(obj) {
				return []redfishSys{parseRedfishSystem(obj, current.RequestURI())}, nil
			}
			return out, errors.Join(append(errs, errors.New("Systems response has no Members array"))...)
		}
		members, ok := rawMembers.([]any)
		if !ok {
			return out, errors.Join(append(errs, errors.New("invalid Members array"))...)
		}
		for _, raw := range members {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			if len(resources) >= redfishMaxSystems {
				return out, errors.Join(append(errs, errors.New("system limit exceeded"))...)
			}
			member, ok := raw.(map[string]any)
			if !ok {
				errs = append(errs, errors.New("invalid system member"))
				continue
			}
			source := ""
			if ref := nestString(member, "@odata.id"); ref != "" {
				resource, err := ep.resolve(current, ref)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				source = resource.RequestURI()
				if resources[source] {
					continue
				}
				resources[source] = true
				if _, expanded := member["Status"]; !expanded {
					member, _, err = ep.get(ctx, resource)
					if err != nil {
						errs = append(errs, err)
						continue
					}
				}
			} else {
				id := nestString(member, "Id")
				if id == "" {
					errs = append(errs, errors.New("system member has no identity"))
					continue
				}
				source = first.RequestURI() + "#" + id
				if resources[source] {
					continue
				}
				resources[source] = true
			}
			if !validRedfishIdentity(member) {
				errs = append(errs, errors.New("system response has no identity"))
				continue
			}
			out = append(out, parseRedfishSystem(member, source))
		}
		next := nestString(obj, "Members@odata.nextLink")
		if next == "" {
			next = nestString(obj, "@odata.nextLink")
		}
		if next == "" {
			if len(out) == 0 {
				errs = append(errs, errors.New("no systems"))
			}
			return out, errors.Join(errs...)
		}
		current, err = ep.resolve(current, next)
		if err != nil {
			return out, errors.Join(append(errs, err)...)
		}
	}
	return out, errors.Join(append(errs, errors.New("pagination limit exceeded"))...)
}

func parseRedfishSystem(obj map[string]any, source string) redfishSys {
	id := nestString(obj, "Id")
	if id == "" {
		id = nestString(obj, "Name")
	}
	if id == "" {
		id = source
	}
	h := redfishText(nestString(obj, "Status", "HealthRollup"))
	if h == "" {
		h = redfishText(nestString(obj, "Status", "Health"))
	}
	processors := redfishNumber(obj, "ProcessorSummary", "Count")
	if processors != nil && math.Trunc(*processors) != *processors {
		processors = nil
	}
	return redfishSys{id: id, source: source, health: h, power: redfishText(nestString(obj, "PowerState")), manufacturer: redfishText(nestString(obj, "Manufacturer")), model: redfishText(nestString(obj, "Model")), processors: processors, memory: redfishNumber(obj, "MemorySummary", "TotalSystemMemoryGiB")}
}

func redfishNumber(obj map[string]any, path ...string) *float64 {
	var value any = obj
	for _, part := range path {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[part]
	}
	n, ok := value.(float64)
	if !ok || n < 0 || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil
	}
	return &n
}

// Bound retained metadata independently of the JSON response limit.
func redfishText(value string) string {
	if len(value) > 256 {
		return strings.Clone(value[:256])
	}
	return value
}

func validRedfishIdentity(obj map[string]any) bool {
	id := nestString(obj, "Id")
	if id == "" {
		id = nestString(obj, "Name")
	}
	return id != "" && len(id) <= 256
}
