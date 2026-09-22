package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/health"
)

// GET/PUT/POST/DELETE /api/v3/alert_config (and /api/v1/alert_config)
//
//	GET                 list compiled rules {configs:[{hash,name,on,source,config}]}
//	GET ?name= / ?hash= one rule
//	PUT/POST            YAML (`alarms:`) or JSON RuleSpec — upsert (PUT) / create (POST)
//	DELETE ?name=       drop a runtime rule
func (s *Server) handleAlertConfig(w http.ResponseWriter, r *http.Request) {
	if s.opt.Health == nil {
		http.Error(w, "health engine disabled", http.StatusNotFound)
		return
	}
	apiVer := 1
	if strings.Contains(r.URL.Path, "/v3/") {
		apiVer = 3
	}
	switch r.Method {
	case http.MethodGet:
		s.getAlertConfig(w, r, apiVer)
	case http.MethodPut, http.MethodPost:
		s.putAlertConfig(w, r, apiVer, r.Method == http.MethodPost)
	case http.MethodDelete:
		name := r.URL.Query().Get("name")
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if !s.opt.Health.RemoveRule(name) {
			http.Error(w, "alert not found", http.StatusNotFound)
			return
		}
		writeJSON(w, map[string]any{"api": apiVer, "deleted": name})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) getAlertConfig(w http.ResponseWriter, r *http.Request, apiVer int) {
	name := r.URL.Query().Get("name")
	hash := r.URL.Query().Get("hash")
	rules := s.opt.Health.Rules()
	if name == "" && hash == "" {
		cfgs := make([]map[string]any, 0, len(rules))
		for _, ru := range rules {
			cfgs = append(cfgs, alertConfigJSON(ru))
		}
		writeJSON(w, map[string]any{"api": apiVer, "configs": cfgs, "count": len(cfgs)})
		return
	}
	for _, ru := range rules {
		if (name != "" && ru.Spec.Name == name) || (hash != "" && ru.Hash() == hash) {
			writeJSON(w, map[string]any{"api": apiVer, "config": alertConfigJSON(ru)})
			return
		}
	}
	http.Error(w, "alert not found", http.StatusNotFound)
}

func (s *Server) putAlertConfig(w http.ResponseWriter, r *http.Request, apiVer int, createOnly bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ct := r.Header.Get("Content-Type")
	isJSON := strings.Contains(ct, "json") || (len(body) > 0 && (body[0] == '{' || body[0] == '['))
	var rules []*health.Rule
	if isJSON {
		var spec health.RuleSpec
		if err := json.Unmarshal(body, &spec); err != nil {
			var wrap struct {
				Alarms []health.RuleSpec `json:"alarms"`
				Config health.RuleSpec   `json:"config"`
			}
			if err2 := json.Unmarshal(body, &wrap); err2 != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			if wrap.Config.Name != "" {
				spec = wrap.Config
			} else if len(wrap.Alarms) > 0 {
				compiled, err := health.CompileAll(wrap.Alarms, "api")
				if err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				rules = compiled
			}
		}
		if spec.Name != "" {
			ru, err := health.Compile(spec, "api")
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			rules = []*health.Rule{ru}
		}
	} else {
		compiled, err := health.ParseRules(body, "api")
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rules = compiled
	}
	if len(rules) == 0 {
		http.Error(w, "no alarm spec in body", http.StatusBadRequest)
		return
	}
	existing := map[string]bool{}
	for _, ru := range s.opt.Health.Rules() {
		existing[ru.Spec.Name] = true
	}
	out := make([]map[string]any, 0, len(rules))
	for _, ru := range rules {
		if createOnly && existing[ru.Spec.Name] {
			http.Error(w, "alert exists: "+ru.Spec.Name, http.StatusConflict)
			return
		}
		s.opt.Health.UpsertRule(ru)
		out = append(out, alertConfigJSON(ru))
	}
	writeJSON(w, map[string]any{"api": apiVer, "configs": out, "count": len(out)})
}

func alertConfigJSON(ru *health.Rule) map[string]any {
	return map[string]any{
		"hash":   ru.Hash(),
		"name":   ru.Spec.Name,
		"on":     ru.Spec.On,
		"source": ru.Source,
		"every":  ru.Every.Seconds(),
		"config": ru.Spec,
	}
}
