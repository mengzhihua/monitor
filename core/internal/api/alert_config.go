package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/health"
	"gopkg.in/yaml.v3"
)

const alertConfigMax = 1 << 20

type alertConfigItem struct {
	Hash    string          `json:"hash"`
	Name    string          `json:"name"`
	On      string          `json:"on"`
	Source  string          `json:"source"`
	Every   float64         `json:"every"`
	Config  health.RuleSpec `json:"config"`
	Origin  string          `json:"origin"`
	HasBase bool            `json:"has_base"`
	Deleted bool            `json:"deleted"`
}

type alertConfigResponse struct {
	API        int               `json:"api"`
	Revision   string            `json:"revision"`
	Persistent bool              `json:"persistent"`
	Scope      string            `json:"scope"`
	Hostname   string            `json:"hostname"`
	CanManage  bool              `json:"can_manage"`
	Configs    []alertConfigItem `json:"configs"`
	Count      int               `json:"count"`
}

func alertConfigJSON(rule *health.Rule) alertConfigItem {
	return alertConfigItem{Hash: rule.Hash(), Name: rule.Spec.Name, On: rule.Spec.On,
		Source: rule.Source, Every: rule.Every.Seconds(), Config: rule.Spec}
}

func (s *Server) alertConfigResponse(r *http.Request, snapshot health.RuleConfigSnapshot, apiVer int) alertConfigResponse {
	u := userOf(r)
	out := alertConfigResponse{API: apiVer, Revision: snapshot.Revision, Persistent: snapshot.Persistent,
		Scope: "local", Hostname: s.reg.Host.Hostname, CanManage: u.Role == RoleAdmin && u.principal != "",
		Configs: make([]alertConfigItem, 0, len(snapshot.Rules)+len(snapshot.Removed)+len(snapshot.InvalidRules))}
	base := make(map[string]*health.Rule, len(snapshot.Base))
	for _, rule := range snapshot.Base {
		base[rule.Spec.Name] = rule
	}
	overridden := make(map[string]bool, len(snapshot.Overridden))
	for _, name := range snapshot.Overridden {
		overridden[name] = true
	}
	for _, rule := range snapshot.Rules {
		item := alertConfigJSON(rule)
		item.HasBase = base[item.Name] != nil
		switch {
		case !item.HasBase:
			item.Origin = "custom"
		case overridden[item.Name]:
			item.Origin = "override"
		default:
			item.Origin = "base"
		}
		out.Configs = append(out.Configs, item)
	}
	for _, name := range snapshot.Removed {
		item := alertConfigItem{Name: name, Source: "api", Config: health.RuleSpec{Name: name}, Deleted: true, Origin: "deleted"}
		if rule := base[name]; rule != nil {
			item = alertConfigJSON(rule)
			item.HasBase, item.Deleted, item.Origin = true, true, "deleted"
		}
		out.Configs = append(out.Configs, item)
	}
	for _, spec := range snapshot.InvalidRules {
		out.Configs = append(out.Configs, alertConfigItem{Name: spec.Name, On: spec.On, Source: "api",
			HasBase: base[spec.Name] != nil, Config: spec, Origin: "invalid"})
	}
	sort.Slice(out.Configs, func(i, j int) bool { return out.Configs[i].Name < out.Configs[j].Name })
	out.Count = len(out.Configs)
	return out
}

func (s *Server) allowAlertConfig(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "no-store")
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "alert rules are local only", http.StatusBadRequest)
		return false
	}
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return false
	}
	return true
}

// Legacy v1/v3 endpoints share the same durable, atomic rule mutations as the
// workbench. Revision remains optional for existing API clients.
func (s *Server) handleAlertConfig(w http.ResponseWriter, r *http.Request) {
	if !s.allowAlertConfig(w, r) {
		return
	}
	apiVer := 1
	if strings.Contains(r.URL.Path, "/v3/") {
		apiVer = 3
	}
	if r.Method == http.MethodGet {
		out := s.alertConfigResponse(r, s.opt.Health.RulesConfig(), apiVer)
		name, hash := r.URL.Query().Get("name"), r.URL.Query().Get("hash")
		if name == "" && hash == "" {
			writeJSON(w, out)
			return
		}
		for _, item := range out.Configs {
			if (name != "" && item.Name == name) || (hash != "" && item.Hash == hash) {
				writeJSON(w, map[string]any{"api": apiVer, "config": item, "revision": out.Revision, "persistent": out.Persistent})
				return
			}
		}
		http.Error(w, "alert not found", http.StatusNotFound)
		return
	}
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" {
		http.Error(w, "alert rule management requires an authenticated admin", http.StatusForbidden)
		return
	}
	var revision *string
	if values, ok := r.URL.Query()["revision"]; ok {
		if len(values) != 1 || values[0] == "" || len(values[0]) > 128 {
			http.Error(w, "invalid revision", http.StatusBadRequest)
			return
		}
		revision = &values[0]
	}
	mutation := health.RuleMutation{CreateOnly: r.Method == http.MethodPost}
	switch r.Method {
	case http.MethodPut, http.MethodPost:
		body, ok := readAlertConfigBody(w, r)
		if !ok {
			return
		}
		specs, err := decodeAlertSpecs(body, r.Header.Get("Content-Type"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mutation.Upserts = specs
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		mutation.Delete = []string{name}
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	snapshot, err := s.opt.Health.MutateRules(revision, mutation)
	if err != nil {
		s.alertMutationError(w, err)
		return
	}
	if r.Method == http.MethodDelete {
		writeJSON(w, map[string]any{"api": apiVer, "deleted": mutation.Delete[0], "revision": snapshot.Revision, "persistent": snapshot.Persistent})
		return
	}
	out := s.alertConfigResponse(r, snapshot, apiVer)
	submitted := make(map[string]bool, len(mutation.Upserts))
	for _, spec := range mutation.Upserts {
		submitted[spec.Name] = true
	}
	filtered := make([]alertConfigItem, 0, len(submitted))
	for _, item := range out.Configs {
		if submitted[item.Name] {
			filtered = append(filtered, item)
		}
	}
	out.Configs, out.Count = filtered, len(filtered)
	writeJSON(w, out)
}

func readAlertConfigBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, alertConfigMax))
	if err != nil {
		code := http.StatusBadRequest
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			code = http.StatusRequestEntityTooLarge
		}
		http.Error(w, "invalid or oversized alert rule body", code)
		return nil, false
	}
	return body, true
}

func decodeAlertJSON(body []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("exactly one JSON document required")
	}
	return nil
}

func decodeAlertSpecs(body []byte, contentType string) ([]health.RuleSpec, error) {
	body = bytes.TrimSpace(body)
	var specs []health.RuleSpec
	if strings.Contains(contentType, "json") || (len(body) > 0 && (body[0] == '{' || body[0] == '[')) {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(body, &keys); err != nil {
			return nil, fmt.Errorf("invalid JSON: %w", err)
		}
		_, config := keys["config"]
		_, alarms := keys["alarms"]
		if config && alarms {
			return nil, errors.New("choose config or alarms, not both")
		}
		if config || alarms {
			var envelope struct {
				Config *health.RuleSpec  `json:"config"`
				Alarms []health.RuleSpec `json:"alarms"`
			}
			if err := decodeAlertJSON(body, &envelope); err != nil {
				return nil, err
			}
			specs = envelope.Alarms
			if envelope.Config != nil {
				specs = []health.RuleSpec{*envelope.Config}
			}
		} else {
			var spec health.RuleSpec
			if err := decodeAlertJSON(body, &spec); err != nil {
				return nil, err
			}
			specs = []health.RuleSpec{spec}
		}
	} else {
		var envelope struct {
			Alarms []health.RuleSpec `yaml:"alarms"`
		}
		d := yaml.NewDecoder(bytes.NewReader(body))
		d.KnownFields(true)
		if err := d.Decode(&envelope); err != nil {
			return nil, err
		}
		if d.Decode(new(any)) != io.EOF {
			return nil, errors.New("exactly one YAML document required")
		}
		specs = envelope.Alarms
	}
	if len(specs) == 0 {
		return nil, errors.New("no alarm spec in body")
	}
	return specs, nil
}

func (s *Server) alertMutationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, health.ErrRuleConflict):
		http.Error(w, "alert rules changed or name exists; reload and review before saving", http.StatusConflict)
	case errors.Is(err, health.ErrRuleNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
	case errors.Is(err, health.ErrRuleInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, health.ErrRuleCapacity):
		http.Error(w, err.Error(), http.StatusUnprocessableEntity)
	default:
		s.log.Error("save alert rules failed", "error", err)
		http.Error(w, "unable to persist alert rules", http.StatusServiceUnavailable)
	}
}
