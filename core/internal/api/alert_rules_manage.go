package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/health"
)

type managedAlertRuleRequest struct {
	Revision string           `json:"revision"`
	Action   string           `json:"action"`
	Config   *health.RuleSpec `json:"config"`
	Name     string           `json:"name"`
}

type alertRuleMatch struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Context string `json:"context"`
	Family  string `json:"family"`
}

func (s *Server) handleManagedAlertRules(w http.ResponseWriter, r *http.Request) {
	s.manageAlertRule(w, r, false)
}
func (s *Server) handlePreviewAlertRule(w http.ResponseWriter, r *http.Request) {
	s.manageAlertRule(w, r, true)
}

func (s *Server) manageAlertRule(w http.ResponseWriter, r *http.Request, preview bool) {
	if !s.allowAlertConfig(w, r) {
		return
	}
	u := userOf(r)
	if u.Role != RoleAdmin || u.principal == "" {
		http.Error(w, "alert rule management requires an authenticated admin", http.StatusForbidden)
		return
	}
	body, ok := readAlertConfigBody(w, r)
	if !ok {
		return
	}
	var request managedAlertRuleRequest
	if err := decodeAlertJSON(body, &request); err != nil || strings.TrimSpace(request.Revision) == "" || len(request.Revision) > 128 {
		http.Error(w, "invalid alert rule request or missing revision", http.StatusBadRequest)
		return
	}
	snapshot := s.opt.Health.RulesConfig()
	if request.Revision != snapshot.Revision {
		s.alertMutationError(w, health.ErrRuleConflict)
		return
	}
	mutation, spec, err := managedAlertMutation(request, snapshot)
	if err != nil {
		s.alertMutationError(w, err)
		return
	}
	if preview {
		if err := s.opt.Health.ValidateRuleMutation(&request.Revision, mutation); err != nil {
			s.alertMutationError(w, err)
			return
		}
		charts := s.reg.Charts()
		sort.Slice(charts, func(i, j int) bool { return charts[i].ID < charts[j].ID })
		matches := make([]alertRuleMatch, 0)
		count := 0
		const limit = 50
		for _, chart := range charts {
			if r.Context().Err() != nil {
				return
			}
			if spec.On != "" && health.MatchesRuleChart(spec, chart) {
				count++
				if len(matches) < limit {
					matches = append(matches, alertRuleMatch{ID: chart.ID, Title: chart.Title, Context: chart.Context, Family: chart.Family})
				}
			}
		}
		writeJSON(w, map[string]any{"revision": request.Revision, "action": request.Action, "name": spec.Name, "valid": true,
			"persistent": snapshot.Persistent, "matched": count, "charts": matches, "truncated": count > limit, "limit": limit, "disabled": spec.Disabled,
			"notice": "仅校验规则语法与本机图表匹配范围；不模拟阈值求值、不修改规则、不发送通知。图表范围可能随采集变化。"})
		return
	}
	if r.Context().Err() != nil {
		return
	}
	updated, err := s.opt.Health.MutateRules(&request.Revision, mutation)
	if err != nil {
		s.alertMutationError(w, err)
		return
	}
	writeJSON(w, s.alertConfigResponse(r, updated, 1))
}

func managedAlertMutation(request managedAlertRuleRequest, snapshot health.RuleConfigSnapshot) (health.RuleMutation, health.RuleSpec, error) {
	var mutation health.RuleMutation
	var spec health.RuleSpec
	invalid := func() (health.RuleMutation, health.RuleSpec, error) {
		return mutation, spec, fmt.Errorf("%w: action requires config (create/update) or name (delete/reset)", health.ErrRuleInvalid)
	}
	switch request.Action {
	case "create", "update":
		if request.Config == nil || request.Name != "" {
			return invalid()
		}
		spec = *request.Config
		exists := false
		for _, rule := range snapshot.Rules {
			if rule.Spec.Name == spec.Name {
				exists = true
				break
			}
		}
		for _, name := range snapshot.Removed {
			if name == spec.Name {
				exists = true
				break
			}
		}
		if request.Action == "create" && exists {
			return mutation, spec, health.ErrRuleConflict
		}
		if request.Action == "update" && !exists {
			return mutation, spec, health.ErrRuleNotFound
		}
		mutation.Upserts = []health.RuleSpec{spec}
		mutation.CreateOnly = request.Action == "create"
	case "delete", "reset":
		if request.Config != nil || request.Name == "" {
			return invalid()
		}
		rules := snapshot.Rules
		if request.Action == "reset" {
			rules = snapshot.Base
		}
		for _, rule := range rules {
			if rule.Spec.Name == request.Name {
				spec = rule.Spec
				break
			}
		}
		if request.Action == "reset" && spec.Name == "" {
			for _, name := range snapshot.Removed {
				if name == request.Name {
					spec.Name = name
					break
				}
			}
		}
		if spec.Name == "" {
			return mutation, spec, health.ErrRuleNotFound
		}
		if request.Action == "reset" {
			mutation.Restore = []string{request.Name}
		} else {
			mutation.Delete = []string{request.Name}
		}
	default:
		return invalid()
	}
	return mutation, spec, nil
}
