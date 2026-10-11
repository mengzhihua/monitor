package api

import (
	"encoding/json"
	"net/http"

	"github.com/mengzhihua/monitor/core/internal/aianalysis"
)

func aiPlanMatchesSnapshot(p aianalysis.Plan, snapshot aiSnapshot) bool {
	for _, step := range p.Steps {
		if step.Chart == "" {
			if step.Dimension != "" {
				return false
			}
			continue
		}
		found := false
		for _, chart := range snapshot.Charts {
			if chart.ID != step.Chart {
				continue
			}
			if step.Dimension == "" {
				found = true
			}
			for _, dim := range chart.Dimensions {
				if dim.ID == step.Dimension {
					found = true
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}
func (s *Server) aiResolutionCapacity(w http.ResponseWriter, owner string) bool {
	err := s.analysis.resolutions.CanCreate(owner)
	if err != nil {
		aiError(w, err)
		return false
	}
	return true
}
func (s *Server) aiResolution(w http.ResponseWriter, u User, id string) (aianalysis.Resolution, bool) {
	r, err := s.analysis.resolutions.Get(u.principal, id)
	if err != nil {
		aiError(w, err)
		return r, false
	}
	if !s.roomAllowed(u, r.Node) {
		http.Error(w, "forbidden: room", 403)
		return r, false
	}
	return r, true
}
func (s *Server) handleAIResolutions(w http.ResponseWriter, r *http.Request) {
	u, ok := aiAccount(w, r)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		if id := r.URL.Query().Get("id"); id != "" {
			if record, ok := s.aiResolution(w, u, id); ok {
				writeJSON(w, record)
			}
			return
		}
		list, err := s.analysis.resolutions.List(u.principal)
		if err != nil {
			aiError(w, err)
			return
		}
		visible := []aianalysis.ResolutionSummary{}
		for _, record := range list {
			if !s.roomAllowed(u, record.Node) {
				continue
			}
			if node, set := r.URL.Query()["node"]; set && len(node) > 0 {
				selected := node[0]
				if selected == "local" {
					selected = ""
				}
				if view, ok := s.resolve(selected); ok && view.node == nil {
					selected = ""
				}
				if record.Node != selected {
					continue
				}
			}
			visible = append(visible, record)
		}
		writeJSON(w, struct {
			Records    []aianalysis.ResolutionSummary `json:"records"`
			Limit      int                            `json:"limit"`
			Persistent bool                           `json:"persistent"`
		}{visible, aianalysis.ResolutionLimit, s.analysis.resolutions.Persistent()})
		return
	}
	var body struct {
		ID       string  `json:"id"`
		Revision *uint64 `json:"revision"`
		StepID   string  `json:"step_id"`
		Status   string  `json:"status"`
		Note     string  `json:"note"`
	}
	if !aiDecode(w, r, &body) {
		return
	}
	if body.Revision == nil {
		http.Error(w, "revision required", 400)
		return
	}
	if _, ok := s.aiResolution(w, u, body.ID); !ok {
		return
	}
	if r.Method == http.MethodDelete {
		if body.StepID != "" || body.Status != "" || body.Note != "" {
			http.Error(w, "invalid delete request", 400)
			return
		}
		if err := s.analysis.resolutions.Delete(u.principal, body.ID, *body.Revision); err != nil {
			aiError(w, err)
			return
		}
		writeJSON(w, map[string]bool{"deleted": true})
		return
	}
	record, err := s.analysis.resolutions.Update(u.principal, body.ID, *body.Revision, body.StepID, body.Status, body.Note, nil)
	if err != nil {
		aiError(w, err)
		return
	}
	writeJSON(w, record)
}

type aiComparison struct {
	Chart     string   `json:"chart"`
	Dimension string   `json:"dimension"`
	Units     string   `json:"units"`
	Before    *float64 `json:"before"`
	After     *float64 `json:"after"`
	Delta     *float64 `json:"delta"`
}
type aiReview struct {
	At          int64          `json:"at"`
	Outcome     string         `json:"outcome"`
	Snapshot    aiSnapshot     `json:"snapshot"`
	Comparisons []aiComparison `json:"comparisons"`
	Notes       []string       `json:"notes"`
}

func compareAISnapshots(baseline, current aiSnapshot) aiReview {
	review := aiReview{At: current.GeneratedAt, Outcome: "observed", Snapshot: current, Comparisons: []aiComparison{}, Notes: []string{
		"前后数值是相同长度窗口的桶均值平均，窗口可能重叠；变化不代表修复操作具有因果效果。",
		"勾选步骤只记录人工处理状态，不修改真实告警；告警恢复也不保证根因已解决。",
	}}
	insufficient, awaiting := false, false
	for _, old := range baseline.Charts {
		var fresh *aiChart
		for i := range current.Charts {
			if current.Charts[i].ID == old.ID {
				fresh = &current.Charts[i]
				break
			}
		}
		if fresh == nil || fresh.State != "fresh" || fresh.Units != old.Units {
			insufficient = true
			continue
		}
		if fresh.LastCollectedAt <= baseline.Before {
			awaiting = true
		}
		valid := false
		for _, dim := range old.Dimensions {
			row := aiComparison{Chart: old.ID, Dimension: dim.ID, Units: old.Units, Before: dim.Average}
			for _, next := range fresh.Dimensions {
				if next.ID != dim.ID {
					continue
				}
				row.After = next.Average
				if next.Count > 0 {
					valid = true
				}
				if row.Before != nil && row.After != nil {
					row.Delta = finiteValue(*row.After - *row.Before)
				}
			}
			review.Comparisons = append(review.Comparisons, row)
		}
		if !valid {
			insufficient = true
		}
	}
	if baseline.Problem != nil {
		review.Outcome = "insufficient_data"
		for _, alarm := range current.Alarms {
			if alarm.Chart != baseline.Problem.Chart || alarm.Name != baseline.Problem.Name {
				continue
			}
			if alarm.State != "fresh" {
				insufficient = true
				break
			}
			if alarm.UpdatedAt <= baseline.Before {
				awaiting = true
			}
			switch alarm.Status {
			case "CLEAR":
				review.Outcome = "alarm_recovered"
			case "WARNING", "CRITICAL":
				review.Outcome = "still_active"
				if alarm.ProblemID != baseline.Problem.ProblemID {
					review.Outcome = "episode_changed"
				}
			}
			break
		}
	}
	if awaiting {
		review.Outcome = "awaiting_new_data"
	}
	if insufficient {
		review.Outcome = "insufficient_data"
	}
	return review
}
func (s *Server) handleAIResolutionReview(w http.ResponseWriter, r *http.Request) {
	u, ok := aiAccount(w, r)
	if !ok {
		return
	}
	var body struct {
		ID       string  `json:"id"`
		Revision *uint64 `json:"revision"`
	}
	if !aiDecode(w, r, &body) {
		return
	}
	if body.Revision == nil {
		http.Error(w, "revision required", 400)
		return
	}
	record, ok := s.aiResolution(w, u, body.ID)
	if !ok {
		return
	}
	if record.Revision != *body.Revision {
		aiError(w, aianalysis.ErrConflict)
		return
	}
	var baseline aiSnapshot
	if json.Unmarshal(record.Baseline, &baseline) != nil {
		aiError(w, aianalysis.ErrUnavailable)
		return
	}
	ids := []string{}
	for _, chart := range baseline.Charts {
		ids = append(ids, chart.ID)
	}
	current, err := s.aiSnapshotFor(r.Context(), u, record.Node, ids, baseline.Before-baseline.After)
	if err != nil {
		aiSnapshotErrorResponse(w, err)
		return
	}
	if baseline.HostID != current.HostID {
		http.Error(w, "node identity changed; generate a new plan", 409)
		return
	}
	review := compareAISnapshots(baseline, current)
	data, err := json.Marshal(review)
	if err != nil {
		aiError(w, aianalysis.ErrUnavailable)
		return
	}
	if r.Context().Err() != nil {
		return
	}
	record, err = s.analysis.resolutions.Update(u.principal, record.ID, *body.Revision, "", "", "", data)
	if err != nil {
		aiError(w, err)
		return
	}
	writeJSON(w, record)
}
