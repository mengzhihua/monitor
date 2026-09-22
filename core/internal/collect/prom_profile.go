package collect

import (
	"sort"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

// PromProfile maps a named Prometheus exporter onto Monitor-native chart IDs
// (Netdata prometheus.profiles idea). Unmatched families stay on prom.*.
type PromProfile struct {
	Name   string
	Title  string
	Match  []string // metric name prefixes that auto-select this profile
	Auto   bool     // eligible for profiles: auto (unique prefixes only)
	Notes  string
	Charts []PromChart
}

// PromChart is one native chart fed by a single Prometheus family.
type PromChart struct {
	ID       string
	Title    string
	Units    string
	Family   string
	Type     registry.ChartType
	Priority int
	Metric   string // exact Prometheus metric name
	DimLabel string // label → dimension id; empty uses DimName or "value"
	DimName  string
	Counter  bool
}

// PromCatalogEntry is the public M26 directory (native vs prom.* vs dedicated).
type PromCatalogEntry struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"` // profile | dedicated | fallback
	Collector string   `json:"collector,omitempty"`
	Match     []string `json:"match,omitempty"`
	Auto      bool     `json:"auto,omitempty"`
	Charts    []string `json:"charts,omitempty"`
	Notes     string   `json:"notes,omitempty"`
}

func LookupPromProfile(name string) *PromProfile {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || name == "off" || name == "none" || name == "prom" {
		return nil
	}
	for i := range stockPromProfiles {
		p := &stockPromProfiles[i]
		if p.Name == name {
			return p
		}
	}
	return nil
}

func PromProfileNames() []string {
	out := make([]string, 0, len(stockPromProfiles))
	for _, p := range stockPromProfiles {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// PromCatalog documents named native-ID wrappers vs dedicated collectors vs
// generic prom.* fallback. 850+ Prometheus integration names stay on fallback
// unless they appear here.
func PromCatalog() map[string]any {
	profiles := make([]PromCatalogEntry, 0, len(stockPromProfiles))
	for _, p := range stockPromProfiles {
		charts := make([]string, 0, len(p.Charts))
		for _, c := range p.Charts {
			charts = append(charts, c.ID)
		}
		profiles = append(profiles, PromCatalogEntry{
			Name: p.Name, Kind: "profile", Collector: "prometheus",
			Match: p.Match, Auto: p.Auto, Charts: charts, Notes: p.Notes,
		})
	}
	dedicated := make([]PromCatalogEntry, 0, len(promDedicated))
	for _, d := range promDedicated {
		dedicated = append(dedicated, d)
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	sort.Slice(dedicated, func(i, j int) bool { return dedicated[i].Name < dedicated[j].Name })
	return map[string]any{
		"policy": "Named jobs (profile: or auto-detected unique prefixes) emit native chart IDs. " +
			"Everything else stays prom.*. Dedicated collectors are not re-wrapped.",
		"profiles":  profiles,
		"dedicated": dedicated,
		"fallback": PromCatalogEntry{
			Kind: "fallback", Notes: "Unset profile and no auto match → chart prefix prom.",
		},
	}
}

func resolvePromProfiles(job prometheusJob, samples []ingest.Sample, mode string) []*PromProfile {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == "" {
		mode = "auto"
	}
	want := strings.ToLower(strings.TrimSpace(job.Profile))
	if want == "off" || want == "none" || (mode == "off" && want == "") {
		return nil
	}
	if want != "" && want != "auto" {
		if p := LookupPromProfile(want); p != nil {
			return []*PromProfile{p}
		}
		return nil
	}
	if mode != "auto" {
		return nil
	}
	var out []*PromProfile
	seen := map[string]bool{}
	for i := range stockPromProfiles {
		p := &stockPromProfiles[i]
		if !p.Auto || seen[p.Name] {
			continue
		}
		if profileMatches(p, samples) {
			out = append(out, p)
			seen[p.Name] = true
		}
	}
	return out
}

func profileMatches(p *PromProfile, samples []ingest.Sample) bool {
	for _, s := range samples {
		if metricMatches(p.Match, s.Name) {
			return true
		}
	}
	return false
}

func metricMatches(prefixes []string, name string) bool {
	for _, pre := range prefixes {
		if pre == "" {
			continue
		}
		if name == pre || strings.HasPrefix(name, pre) {
			return true
		}
	}
	return false
}

func applyPromProfile(reg *registry.Registry, now time.Time, p *PromProfile, job string, samples []ingest.Sample, used map[string]bool) int {
	byName := map[string][]ingest.Sample{}
	for _, s := range samples {
		byName[s.Name] = append(byName[s.Name], s)
	}
	n := 0
	for i, ch := range p.Charts {
		ss := byName[ch.Metric]
		if len(ss) == 0 {
			continue
		}
		used[ch.Metric] = true
		id := nativeChartID(ch.ID, p.Name, job)
		c, ok := reg.Chart(id)
		if !ok {
			prio := ch.Priority
			if prio == 0 {
				prio = 71000 + i*10
			}
			fam := ch.Family
			if fam == "" {
				fam = p.Name
			}
			c = reg.AddChart(&registry.Chart{
				ID: id, Context: ch.ID, Family: fam, Title: ch.Title, Units: ch.Units,
				Type: ch.Type, Priority: prio, Plugin: "prometheus", Module: p.Name,
			})
		}
		vals := map[string]float64{}
		for _, s := range ss {
			did, dname := profileDim(s, ch)
			if c.Dimension(did) == nil {
				algo := registry.Absolute
				if ch.Counter || s.Kind == ingest.KindCounter {
					algo = registry.Incremental
				}
				c.AddDimension(&registry.Dimension{ID: did, Name: dname, Algorithm: algo})
			}
			vals[did] = s.Value
		}
		if len(vals) == 0 {
			continue
		}
		_ = reg.Collect(id, now, vals)
		n += len(vals)
	}
	return n
}

func nativeChartID(base, profile, job string) string {
	job = strings.TrimSpace(job)
	if job == "" || strings.EqualFold(job, profile) {
		return base
	}
	suf := sanitizeID(job)
	if suf == "" {
		return base
	}
	return base + "." + suf
}

func profileDim(s ingest.Sample, ch PromChart) (id, name string) {
	if ch.DimLabel != "" {
		for _, l := range s.Labels {
			if l.Key == ch.DimLabel {
				id = sanitizeID(l.Value)
				if id == "" {
					id = "value"
				}
				return id, l.Value
			}
		}
	}
	if ch.DimName != "" {
		return ch.DimName, ch.DimName
	}
	if len(s.Labels) == 0 {
		return "value", "value"
	}
	id = sanitizeID(s.Labels[0].Value)
	if id == "" {
		id = "value"
	}
	return id, s.Labels[0].Value
}

func leftoverSamples(samples []ingest.Sample, used map[string]bool) []ingest.Sample {
	if len(used) == 0 {
		return samples
	}
	out := make([]ingest.Sample, 0, len(samples))
	for _, s := range samples {
		if used[s.Name] {
			continue
		}
		out = append(out, s)
	}
	return out
}

func jobFallback(j prometheusJob) bool {
	if j.Fallback == nil {
		return true
	}
	return *j.Fallback
}
