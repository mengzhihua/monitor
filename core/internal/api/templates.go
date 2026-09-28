package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"gopkg.in/yaml.v3"
)

const maxTemplateBody = 256 << 10

// nodeScope resolves a node's room membership and labels for template
// matching.
func (s *Server) nodeScope(nodeID string) (roomID string, labels map[string]string) {
	_, roomID = s.opt.Org.Membership(nodeID)
	if s.opt.Nodes != nil {
		if nd, ok := s.opt.Nodes.Get(nodeID); ok {
			labels = nd.Host.Labels
		}
	}
	return roomID, labels
}

// pushTemplates recomputes and pushes the config frame (with health overlay)
// to every connected node. Called after template upsert/delete; a template
// that matches no node simply produces an empty overlay.
func (s *Server) pushTemplates() {
	if s.opt.Nodes == nil {
		return
	}
	for _, nd := range s.opt.Nodes.List() {
		if !nd.Online() {
			continue
		}
		cfg, _ := s.opt.Org.GetConfig(nd.ID)
		nd.PushConfig(cfg)
	}
}

// handleTemplates is GET/PUT/DELETE /api/v1/hub/templates[?id=].
func (s *Server) handleTemplates(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.opt.Templates == nil {
		http.Error(w, "hub templates not enabled", http.StatusNotFound)
		return
	}
	id := r.URL.Query().Get("id")
	switch r.Method {
	case http.MethodGet:
		if id != "" {
			tpl, ok := s.opt.Templates.Get(id)
			if !ok {
				http.Error(w, "unknown template", http.StatusNotFound)
				return
			}
			writeJSON(w, tpl)
			return
		}
		writeJSON(w, map[string]any{"templates": s.opt.Templates.List()})
	case http.MethodPut:
		var body struct {
			hub.Template
			RulesYAML string `json:"rules_yaml"` // YAML doc {alarms: [...]}, same as the rule editor
			IfUpdated *int64 `json:"if_updated"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxTemplateBody))
		if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid template request", http.StatusBadRequest)
			return
		}
		tpl := body.Template
		if body.RulesYAML != "" {
			var doc struct {
				Alarms []health.RuleSpec `yaml:"alarms"`
			}
			if err := yaml.NewDecoder(bytes.NewReader([]byte(body.RulesYAML))).Decode(&doc); err != nil {
				http.Error(w, "rules yaml: "+err.Error(), http.StatusBadRequest)
				return
			}
			tpl.Rules = doc.Alarms
		}
		if id != "" {
			tpl.ID = id
		}
		out, err := s.opt.Templates.Upsert(tpl, body.IfUpdated)
		switch {
		case errors.Is(err, hub.ErrTemplateConflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case errors.Is(err, hub.ErrTemplateInvalid):
			http.Error(w, err.Error(), http.StatusBadRequest)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			s.pushTemplates()
			writeJSON(w, out)
		}
	case http.MethodDelete:
		if id == "" {
			http.Error(w, "id parameter required", http.StatusBadRequest)
			return
		}
		if err := s.opt.Templates.Delete(id); errors.Is(err, hub.ErrTemplateNotFound) {
			http.Error(w, err.Error(), http.StatusNotFound)
		} else if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			s.pushTemplates()
			writeJSON(w, map[string]bool{"deleted": true})
		}
	}
}

// handleTemplateEffective is GET /api/v1/hub/templates/effective?node=<id>.
func (s *Server) handleTemplateEffective(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.opt.Templates == nil {
		http.Error(w, "hub templates not enabled", http.StatusNotFound)
		return
	}
	nodeID := r.URL.Query().Get("node")
	if nodeID == "" {
		http.Error(w, "node parameter required", http.StatusBadRequest)
		return
	}
	roomID, labels := s.nodeScope(nodeID)
	writeJSON(w, s.opt.Templates.Effective(nodeID, roomID, labels))
}

// handleInventory is GET/PUT /api/v1/hub/inventory[?node=<id>]. GET returns
// the merged view {auto, tags, manual, effective, updated} per node — or all
// nodes when node= is omitted.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.opt.Templates == nil {
		http.Error(w, "hub templates not enabled", http.StatusNotFound)
		return
	}
	nodeID := r.URL.Query().Get("node")
	switch r.Method {
	case http.MethodGet:
		if nodeID != "" {
			writeJSON(w, s.inventoryView(nodeID))
			return
		}
		out := map[string]any{}
		if s.opt.Nodes != nil {
			for _, nd := range s.opt.Nodes.List() {
				out[nd.ID] = s.inventoryView(nd.ID)
			}
		}
		writeJSON(w, map[string]any{"inventory": out})
	case http.MethodPut:
		if nodeID == "" {
			http.Error(w, "node parameter required", http.StatusBadRequest)
			return
		}
		var body struct {
			Fields    map[string]string `json:"fields"`
			IfUpdated *int64            `json:"if_updated"`
		}
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
			http.Error(w, "invalid inventory request", http.StatusBadRequest)
			return
		}
		inv, err := s.opt.Templates.SetInventory(nodeID, body.Fields, body.IfUpdated)
		switch {
		case errors.Is(err, hub.ErrTemplateConflict):
			http.Error(w, err.Error(), http.StatusConflict)
		case err != nil:
			http.Error(w, err.Error(), http.StatusInternalServerError)
		default:
			writeJSON(w, s.inventoryView(nodeID))
			_ = inv
		}
	}
}

// inventoryView merges auto host fields, template tags and manual fields;
// manual wins over tags, tags win over auto.
func (s *Server) inventoryView(nodeID string) map[string]any {
	auto := map[string]string{}
	if s.opt.Nodes != nil {
		if nd, ok := s.opt.Nodes.Get(nodeID); ok {
			info := nd.Info(time.Now())
			auto["hostname"] = info.Hostname
			auto["os"] = info.OS
			auto["arch"] = info.Arch
			for k, v := range info.Labels {
				auto["label."+k] = v
			}
		}
	}
	roomID, labels := s.nodeScope(nodeID)
	ov := s.opt.Templates.Effective(nodeID, roomID, labels)
	inv, _ := s.opt.Templates.Inventory(nodeID)
	effective := map[string]string{}
	for k, v := range auto {
		effective[k] = v
	}
	for k, v := range ov.Tags {
		effective[k] = v
	}
	for k, v := range inv.Fields {
		effective[k] = v
	}
	keys := make([]string, 0, len(effective))
	for k := range effective {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return map[string]any{
		"node_id":    nodeID,
		"auto":       auto,
		"tags":       ov.Tags,
		"manual":     inv.Fields,
		"effective":  effective,
		"updated":    inv.Updated,
		"sort_order": keys,
	}
}
