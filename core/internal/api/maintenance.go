package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"

	"github.com/mengzhihua/monitor/core/internal/health"
)

type maintenanceTarget struct {
	Chart string `json:"chart"`
	Alarm string `json:"alarm"`
}

type maintenanceResponse struct {
	health.MaintenancePlanSnapshot
	Available    bool                `json:"available"`
	CanManage    bool                `json:"can_manage"`
	Persistent   bool                `json:"persistent"`
	Scope        string              `json:"scope"`
	Hostname     string              `json:"hostname"`
	Now          int64               `json:"now"`
	PendingLimit int                 `json:"pending_limit"`
	HistoryLimit int                 `json:"history_limit"`
	Targets      []maintenanceTarget `json:"targets"`
}

func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	// Reject accidental use of the chart workspace's remote node selector.
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "maintenance plans are local only", http.StatusBadRequest)
		return
	}
	u := userOf(r)
	canManage := u.Role == RoleAdmin && u.principal != "" && s.opt.Health != nil
	respond := func() {
		out := maintenanceResponse{MaintenancePlanSnapshot: health.MaintenancePlanSnapshot{Plans: []health.MaintenancePlanView{}},
			Scope: "local", CanManage: canManage, PendingLimit: health.MaintenancePlanLimit, HistoryLimit: health.MaintenanceHistoryLimit,
			Hostname: s.reg.Host.Hostname, Targets: []maintenanceTarget{}}
		if e := s.opt.Health; e != nil {
			out.Now = e.Now().Unix()
			out.Available, out.Persistent = true, e.MaintenancePlans().Persistent()
			out.MaintenancePlanSnapshot = e.MaintenancePlans().Snapshot(out.Now)
			for _, a := range e.Alarms() {
				if a.Active {
					out.Targets = append(out.Targets, maintenanceTarget{Chart: a.Chart, Alarm: a.Name})
				}
			}
			sort.Slice(out.Targets, func(i, j int) bool {
				if out.Targets[i].Chart != out.Targets[j].Chart {
					return out.Targets[i].Chart < out.Targets[j].Chart
				}
				return out.Targets[i].Alarm < out.Targets[j].Alarm
			})
		}
		writeJSON(w, out)
	}
	if r.Method == http.MethodGet {
		respond()
		return
	}
	if !canManage {
		http.Error(w, "local maintenance requires an authenticated admin and enabled health engine", http.StatusForbidden)
		return
	}
	var body struct {
		Action   string                  `json:"action"`
		Revision *uint64                 `json:"revision"`
		Plan     *health.MaintenanceSpec `json:"plan"`
		ID       string                  `json:"id"`
		Reason   string                  `json:"reason"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || body.Revision == nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid maintenance request", http.StatusBadRequest)
		return
	}
	e := s.opt.Health
	var err error
	switch {
	case body.Action == "create" && body.Plan != nil && body.ID == "" && body.Reason == "":
		if body.Plan.Scope == "alarm" {
			found := false
			for _, a := range e.Alarms() {
				if a.Active && a.Chart == body.Plan.Chart && a.Name == body.Plan.Alarm {
					found = true
					break
				}
			}
			if !found {
				http.Error(w, "choose a currently bound local alarm", http.StatusBadRequest)
				return
			}
		}
		_, err = e.MaintenancePlans().Create(*body.Revision, *body.Plan, u.Name, e.Now().Unix())
	case body.Action == "cancel" && body.Plan == nil && body.ID != "":
		err = e.MaintenancePlans().Cancel(*body.Revision, body.ID, body.Reason, u.Name, e.Now().Unix())
	default:
		err = health.ErrPlanInvalid
	}
	switch {
	case errors.Is(err, health.ErrPlanInvalid):
		http.Error(w, "invalid plan: title and reason required; duration 1 second to 7 days; start now or within 90 days", http.StatusBadRequest)
	case errors.Is(err, health.ErrPlanConflict):
		http.Error(w, "plans changed or ended; reload and review before retrying", http.StatusConflict)
	case errors.Is(err, health.ErrPlanCapacity):
		http.Error(w, "maintenance capacity reached", http.StatusUnprocessableEntity)
	case err != nil:
		s.log.Error("save maintenance plans failed", "error", err)
		http.Error(w, "maintenance storage unavailable; no change applied", http.StatusServiceUnavailable)
	default:
		respond()
	}
}
