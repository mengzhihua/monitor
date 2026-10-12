package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/aianalysis"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/operations"
)

const diagnosticSchema = "monitor.diagnostic.v1"
const diagnosticAlarmLogCap = 200
const diagnosticHistoryCap = 200
const diagnosticAICap = 5

// diagnosticLogEntry is a redacted alarm transition for handoff packages.
// Recipient role names stay out of the download so the file is safer to share.
type diagnosticLogEntry struct {
	UniqueID   uint64        `json:"unique_id"`
	AlarmID    uint64        `json:"alarm_id"`
	When       int64         `json:"when"`
	Hostname   string        `json:"hostname"`
	Name       string        `json:"name"`
	Chart      string        `json:"chart"`
	Context    string        `json:"context,omitempty"`
	Family     string        `json:"family,omitempty"`
	Status     health.Status `json:"status"`
	OldStatus  health.Status `json:"old_status"`
	// Value/OldValue use pointers so NaN/Inf from UNINITIALIZED transitions
	// become JSON null (encoding/json cannot marshal NaN).
	Value    *float64 `json:"value"`
	OldValue *float64 `json:"old_value"`
	Units    string   `json:"units,omitempty"`
	Info       string        `json:"info,omitempty"`
	Delay      int64         `json:"delay,omitempty"`
	Repeat     bool          `json:"repeat,omitempty"`
	Notified   bool          `json:"notified"`
	NotifiedAt int64         `json:"notified_at,omitempty"`
	Manual     bool          `json:"manual,omitempty"`
	User       string        `json:"user,omitempty"`
	Comment    string        `json:"comment,omitempty"`
}

type diagnosticExport struct {
	Schema          string                       `json:"schema"`
	CapturedAt      int64                        `json:"captured_at"`
	Exporter        User                         `json:"exporter"`
	Host            map[string]any               `json:"host"`
	Scope           map[string]any               `json:"scope"`
	Summary         map[string]int               `json:"summary"`
	Nodes           []operationsNode             `json:"nodes"`
	Problems        []problem                    `json:"problems"`
	HandlingHistory []operations.HistoryRecord   `json:"handling_history"`
	AlarmLog        []diagnosticLogEntry         `json:"alarm_log"`
	Silence         map[string]any               `json:"silence,omitempty"`
	Notifications   *health.NotificationSnapshot `json:"notifications,omitempty"`
	AIResolutions   []aianalysis.Resolution      `json:"ai_resolutions,omitempty"`
	Notes           []string                     `json:"notes"`
	Limits          map[string]any               `json:"limits"`
}

func diagnosticAllowed(u User) bool {
	return u.Role == RoleAdmin || u.Role == RoleTroubleshooter
}

func parseDiagnosticIncludes(raw string) map[string]bool {
	out := map[string]bool{}
	if strings.TrimSpace(raw) == "" {
		for _, key := range []string{"ops", "alarms", "notify", "history", "ai"} {
			out[key] = true
		}
		return out
	}
	for _, part := range strings.Split(raw, ",") {
		key := strings.ToLower(strings.TrimSpace(part))
		switch key {
		case "ops", "alarms", "notify", "history", "ai":
			out[key] = true
		}
	}
	return out
}

func parseProblemIDs(raw string) map[string]bool {
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		id := strings.TrimSpace(part)
		if id != "" {
			out[id] = true
		}
	}
	return out
}

func redactAlarmLog(entries []health.LogEntry, from, until int64, limit int) []diagnosticLogEntry {
	if limit <= 0 {
		limit = diagnosticAlarmLogCap
	}
	out := make([]diagnosticLogEntry, 0, min(limit, len(entries)))
	for i := len(entries) - 1; i >= 0 && len(out) < limit; i-- {
		e := entries[i]
		if from > 0 && e.When < from {
			continue
		}
		if until > 0 && e.When > until {
			continue
		}
		out = append(out, diagnosticLogEntry{
			UniqueID: e.UniqueID, AlarmID: e.AlarmID, When: e.When, Hostname: e.Hostname,
			Name: e.Name, Chart: e.Chart, Context: e.Context, Family: e.Family,
			Status: e.Status, OldStatus: e.OldStatus, Value: finiteOrNil(e.Value), OldValue: finiteOrNil(e.OldValue),
			Units: e.Units, Info: e.Info, Delay: e.Delay, Repeat: e.Repeat,
			Notified: e.Notified, NotifiedAt: e.NotifiedAt, Manual: e.Manual, User: e.User, Comment: e.Comment,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].When != out[j].When {
			return out[i].When < out[j].When
		}
		return out[i].UniqueID < out[j].UniqueID
	})
	return out
}

func (s *Server) handleDiagnosticExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	u := userOf(r)
	if !diagnosticAllowed(u) {
		http.Error(w, "diagnostic export requires an administrator or troubleshooter login", http.StatusForbidden)
		return
	}
	q := r.URL.Query()
	includes := parseDiagnosticIncludes(q.Get("include"))
	nodeFilter := strings.TrimSpace(q.Get("node"))
	problemIDs := parseProblemIDs(q.Get("problem_id"))
	var from, until int64
	for _, field := range []struct {
		key    string
		target *int64
	}{{"from", &from}, {"until", &until}} {
		if raw := q.Get(field.key); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n < 0 {
				http.Error(w, "invalid from/until", http.StatusBadRequest)
				return
			}
			*field.target = n
		}
	}
	if until > 0 && from > until {
		http.Error(w, "invalid from/until", http.StatusBadRequest)
		return
	}
	if from == 0 && until == 0 {
		until = time.Now().Unix()
		from = until - 24*3600
	}

	now := time.Now().Unix()
	host := s.reg.Host
	mode := s.opt.Mode
	if mode == "" {
		mode = "agent"
	}
	bundle := diagnosticExport{
		Schema:     diagnosticSchema,
		CapturedAt: now,
		Exporter:   User{Name: u.Name, Role: u.Role},
		Host: map[string]any{
			"node_id":  host.ID,
			"hostname": host.Hostname,
			"os":       host.OS,
			"arch":     host.Arch,
			"version":  s.opt.Version,
			"mode":     mode,
			"uptime":   int64(time.Since(s.opt.StartedAt).Seconds()),
		},
		Scope: map[string]any{
			"node":        nodeFilter,
			"problem_ids": keysOf(problemIDs),
			"from":        from,
			"until":       until,
			"include":     includeKeys(includes),
		},
		Nodes:           []operationsNode{},
		Problems:        []problem{},
		HandlingHistory: []operations.HistoryRecord{},
		AlarmLog:        []diagnosticLogEntry{},
		Notes: []string{
			"此文件用于向有经验的同事交接排查上下文，不是完整备份。",
			"已省略登录凭据、通知地址/令牌、SSH 凭据、WebShell 终端输出和 AI 密钥。",
			"通知结果表示通道接受或失败，不证明收件人已读。",
		},
		Limits: map[string]any{
			"alarm_log":        diagnosticAlarmLogCap,
			"handling_history": diagnosticHistoryCap,
			"ai_resolutions":   diagnosticAICap,
			"notify_retention": 500,
		},
	}

	if includes["ops"] {
		snap := s.operationsSnapshot(r)
		if r.Context().Err() != nil {
			return
		}
		bundle.Summary = snap.Summary
		if snap.Persistent {
			bundle.Notes = append(bundle.Notes, "处理记录已持久化到服务端数据目录。")
		} else {
			bundle.Notes = append(bundle.Notes, "处理记录仅在内存中，服务重启后可能丢失。")
		}
		for _, n := range snap.Nodes {
			if nodeFilter != "" && n.ID != nodeFilter && !(nodeFilter == "local" && (n.ID == "" || n.ID == host.ID)) {
				continue
			}
			bundle.Nodes = append(bundle.Nodes, n)
		}
		for _, p := range snap.Problems {
			if nodeFilter != "" && p.Node != nodeFilter && !(nodeFilter == "local" && (p.Node == "" || p.Node == "local" || p.Node == host.ID)) {
				continue
			}
			if len(problemIDs) > 0 && !problemIDs[p.ID] {
				continue
			}
			bundle.Problems = append(bundle.Problems, p)
		}
	}

	if includes["history"] && s.operations != nil {
		hq := operations.HistoryQuery{
			HistoryFilter: operations.HistoryFilter{Node: nodeFilter, From: from, Until: until},
			All:           true,
			ScopeNodes:    s.operationsHistoryScope(r),
		}
		page, err := s.operations.QueryHistory(hq)
		if err == nil {
			records := page.Records
			if len(problemIDs) > 0 {
				filtered := records[:0]
				for _, rec := range records {
					if problemIDs[rec.ID] {
						filtered = append(filtered, rec)
					}
				}
				records = filtered
			}
			if len(records) > diagnosticHistoryCap {
				bundle.Notes = append(bundle.Notes, fmt.Sprintf("处理历史已截断为最近 %d 条匹配记录（共 %d）。", diagnosticHistoryCap, len(records)))
				records = records[:diagnosticHistoryCap]
			}
			bundle.HandlingHistory = records
		} else if !errors.Is(err, operations.ErrInvalidQuery) {
			bundle.Notes = append(bundle.Notes, "处理历史查询失败，本包未包含历史记录。")
		}
	}

	if includes["alarms"] && s.opt.Health != nil {
		bundle.Silence = s.opt.Health.SilenceInfo()
		entries := s.opt.Health.Log(0)
		if nodeFilter != "" && nodeFilter != "local" && nodeFilter != host.ID {
			// Local alarm log only describes this process. Hub remote logs stay
			// on their own nodes; note the limitation instead of inventing data.
			bundle.Notes = append(bundle.Notes, "告警转移日志仅含本机引擎；指定远端节点时未附带对端 alarm_log。")
		} else {
			bundle.AlarmLog = redactAlarmLog(entries, from, until, diagnosticAlarmLogCap)
			if len(entries) > diagnosticAlarmLogCap {
				bundle.Notes = append(bundle.Notes, fmt.Sprintf("告警转移日志最多保留最近 %d 条。", diagnosticAlarmLogCap))
			}
		}
	}

	if includes["notify"] && s.opt.Health != nil {
		snap := s.opt.Health.NotificationDiagnostics()
		bundle.Notifications = &snap
		bundle.Notes = append(bundle.Notes, "通知诊断仅覆盖本进程启动后的结果，重启后清空。")
	}

	if includes["ai"] && s.analysis != nil && s.analysis.resolutions != nil && u.principal != "" {
		summaries, err := s.analysis.resolutions.List(u.principal)
		if err == nil {
			for _, summary := range summaries {
				if len(bundle.AIResolutions) >= diagnosticAICap {
					bundle.Notes = append(bundle.Notes, fmt.Sprintf("个人 AI 方案最多附带 %d 份。", diagnosticAICap))
					break
				}
				if nodeFilter != "" && summary.Node != nodeFilter && !(nodeFilter == "local" && (summary.Node == "" || summary.Node == "local")) {
					continue
				}
				full, err := s.analysis.resolutions.Get(u.principal, summary.ID)
				if err != nil {
					continue
				}
				bundle.AIResolutions = append(bundle.AIResolutions, full)
			}
		}
	} else if includes["ai"] && u.principal == "" {
		bundle.Notes = append(bundle.Notes, "当前登录无法绑定个人 AI 方案，未附带 resolutions。")
	}

	name := host.Hostname
	if name == "" {
		name = host.ID
	}
	if name == "" {
		name = "monitor"
	}
	safe := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, name)
	payload, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		s.log.Error("diagnostic export encode failed", "err", err)
		http.Error(w, "diagnostic export encode failed", http.StatusInternalServerError)
		return
	}
	payload = append(payload, '\n')
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="monitor-diagnostic-%s-%s.json"`, safe, time.Now().UTC().Format("20060102T150405Z")))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
	if _, err := w.Write(payload); err != nil {
		s.log.Debug("diagnostic export disconnected", "err", err)
	}
}

func finiteOrNil(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func includeKeys(m map[string]bool) []string {
	out := keysOf(m)
	if len(out) == 0 {
		return []string{}
	}
	return out
}
