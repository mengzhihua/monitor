package api

import (
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/sla"
)

type serviceReportAlarm struct {
	Name    string        `json:"name"`
	Chart   string        `json:"chart"`
	Status  health.Status `json:"status"`
	Updated int64         `json:"updated"`
	Value   *float64      `json:"value"`
	Units   string        `json:"units"`
}
type serviceHandover struct {
	Schema           string                     `json:"schema"`
	Service          string                     `json:"service"`
	Hostname         string                     `json:"hostname"`
	CapturedAt       int64                      `json:"captured_at"`
	CompletedAt      int64                      `json:"completed_at"`
	Revision         string                     `json:"config_revision"`
	Report           sla.Report                 `json:"report"`
	Alarms           []serviceReportAlarm       `json:"alarms"`
	Availability     []sla.Availability         `json:"availability"`
	History          []operations.HistoryRecord `json:"handling_history"`
	HistoryMatched   int                        `json:"history_matched"`
	HistoryLimit     int                        `json:"history_limit"`
	HistorySnapshot  string                     `json:"history_snapshot"`
	HistoryCapacity  int                        `json:"history_capacity"`
	ActionsPerRecord int                        `json:"actions_per_record"`
	Persistent       bool                       `json:"history_persistent"`
	Notes            []string                   `json:"notes"`
}

// serviceClosure includes each dependency once and never includes unrelated services.
func serviceClosure(all []sla.Service, root string) ([]sla.Service, map[string]bool, bool) {
	byName := map[string]sla.Service{}
	for _, s := range all {
		byName[s.Name] = s
	}
	if _, ok := byName[root]; !ok {
		return nil, nil, false
	}
	seen := map[string]bool{}
	alarms := map[string]bool{}
	out := []sla.Service{}
	var visit func(string)
	visit = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		s, ok := byName[name]
		if !ok {
			return
		}
		out = append(out, s)
		for _, a := range s.Alarms {
			alarms[a] = true
		}
		for _, child := range s.Children {
			visit(child)
		}
	}
	visit(root)
	return out, alarms, true
}
func (s *Server) handleServiceReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !diagnosticAllowed(userOf(r)) {
		http.Error(w, "service handover requires administrator or troubleshooter", http.StatusForbidden)
		return
	}
	if node := r.URL.Query().Get("node"); node != "" && node != "local" {
		http.Error(w, "service report is local only", http.StatusBadRequest)
		return
	}
	duration := r.URL.Query().Get("window")
	if duration != "" {
		d, err := time.ParseDuration(duration)
		if err != nil || d <= 0 || d > 90*24*time.Hour {
			http.Error(w, "invalid window", http.StatusBadRequest)
			return
		}
	}
	config := s.catalog.ServiceSnapshot()
	services, names, ok := serviceClosure(config.Services, r.URL.Query().Get("service"))
	if !ok {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}
	now := time.Now()
	from, to := sla.Window(now, duration)
	alarms := []health.Alarm{}
	for _, a := range s.alarmSnapshot() {
		if names[a.Name] {
			alarms = append(alarms, a)
		}
	}
	log := []health.LogEntry{}
	for _, e := range s.alarmLog() {
		if names[e.Name] {
			log = append(log, e)
		}
	}
	bundle := serviceHandover{Schema: "monitor.service-handover.v1", Service: r.URL.Query().Get("service"), Hostname: s.reg.Host.Hostname, CapturedAt: to, Revision: config.Revision,
		Report: sla.Build(services, alarms, log, from, to), Availability: sla.AlarmAvailability(alarms, log, from, to), Alarms: []serviceReportAlarm{}, History: []operations.HistoryRecord{}, HistoryLimit: 200,
		Notes: []string{
			"范围为本机所选服务及其递归依赖，不包含远端节点或无关服务。",
			"状态、告警历史和处理记录分别读取；采集起止时间标明读取区间，不是跨模块原子快照。",
			"百分比仅依据保留的告警历史；未知时间不计为正常，服务取关联项最小值，不是业务 SLA 承诺。",
			"处理记录按最后保留操作时间匹配统计窗口，最多导出最近 200 条；单条历史可能被裁剪，不代表完整审计归档。",
			"不包含登录凭据、通知配置或终端输出；处理备注为原文，分享前请核对是否含敏感信息。",
		}}
	for _, a := range alarms {
		bundle.Alarms = append(bundle.Alarms, serviceReportAlarm{Name: a.Name, Chart: a.Chart, Status: a.Status, Updated: a.LastUpdated, Value: finiteOrNil(a.Value), Units: a.Units})
	}
	history, err := s.operations.QueryHistory(operations.HistoryQuery{HistoryFilter: operations.HistoryFilter{From: from, Until: to}, All: true, ScopeNodes: []string{"", "local", s.reg.Host.ID}})
	if err != nil {
		http.Error(w, "cannot read handling history", http.StatusInternalServerError)
		return
	}
	bundle.HistorySnapshot = history.Snapshot
	bundle.HistoryCapacity = history.Capacity
	bundle.ActionsPerRecord = history.ActionsPerRecord
	bundle.Persistent = s.operations.Persistent()
	for _, record := range history.Records {
		if names[record.Problem.Name] {
			bundle.HistoryMatched++
			if len(bundle.History) < bundle.HistoryLimit {
				bundle.History = append(bundle.History, record)
			}
		}
	}
	bundle.CompletedAt = time.Now().Unix()
	writeJSON(w, bundle)
}
