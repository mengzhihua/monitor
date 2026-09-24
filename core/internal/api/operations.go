package api

import (
	"bytes"
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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/operations"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

type resourceMetric struct {
	Value *float64 `json:"value"`
	At    int64    `json:"at"`
	State string   `json:"state"`
}

type operationsNode struct {
	hub.Info
	CPU           resourceMetric `json:"cpu"`
	Memory        resourceMetric `json:"memory"`
	AlarmCoverage string         `json:"alarm_coverage"`
}

type problem struct {
	ID         string            `json:"id"`
	Node       string            `json:"node"`
	Hostname   string            `json:"hostname"`
	NodeStatus string            `json:"node_status"`
	Chart      string            `json:"chart"`
	Name       string            `json:"name"`
	Severity   string            `json:"severity"`
	Family     string            `json:"family"`
	Info       string            `json:"info"`
	Value      *float64          `json:"value"`
	Units      string            `json:"units"`
	Since      int64             `json:"since"`
	Updated    int64             `json:"updated"`
	Stale      bool              `json:"stale"`
	Handling   operations.Record `json:"handling"`
	Delivery   *problemDelivery  `json:"delivery,omitempty"`
}

// problemDelivery is the latest local channel outcome for this alarm.
// Accepted means the channel accepted the request, not that a person read it.
type problemDelivery struct {
	At      int64  `json:"at"`
	Channel string `json:"channel"`
	Outcome string `json:"outcome"`
	Reason  string `json:"reason,omitempty"`
}

type operationsFamily struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// operationsPage is set when the client asks for a window. Summary stays global.
type operationsPage struct {
	Limit           int                `json:"limit"`
	NodesMatched    int                `json:"nodes_matched"`
	ProblemsMatched int                `json:"problems_matched"`
	Families        []operationsFamily `json:"families"`
}

type operationsSnapshot struct {
	CurrentUser User                `json:"current_user"`
	Assignees   []User              `json:"assignees"`
	Activity    []operations.Record `json:"activity"`
	Now         int64               `json:"now"`
	Nodes       []operationsNode    `json:"nodes"`
	Problems    []problem           `json:"problems"`
	Summary     map[string]int      `json:"summary"`
	Persistent  bool                `json:"persistent"`
	Page        *operationsPage     `json:"page,omitempty"`
}

// Static operators plus the caller's federated identity. Never expose tokens
// or enumerate OIDC/LDAP sessions as a user directory.
func (s *Server) operationsAssignees(r *http.Request) []User {
	users := append([]User{}, s.opt.Users...)
	if s.opt.Token != "" {
		users = append(users, User{Name: "admin", Role: RoleAdmin})
	}
	users = append(users, userOf(r))
	out := []User{}
	seen := map[string]bool{}
	for _, u := range users {
		if (u.Role != RoleAdmin && u.Role != RoleTroubleshooter) || !operations.ValidAssignee(u.Name) || seen[u.Name] {
			continue
		}
		seen[u.Name] = true
		out = append(out, User{Name: u.Name, Role: u.Role})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func finiteValue(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func metricFor(reg *registry.Registry, id, status string, now int64) resourceMetric {
	m := resourceMetric{State: "unavailable"}
	c, ok := reg.Chart(id)
	if !ok {
		return m
	}
	at, values := c.LatestValues()
	m.At = at
	if at <= 0 {
		return m
	}
	if status != hub.StatusLive || now-at > int64(max(15, c.UpdateEvery*3)) || at > now+5 {
		m.State = "stale"
		return m
	}
	var value float64
	for _, dimension := range c.Dims() {
		if _, ok := values[dimension.ID]; !ok {
			return m
		}
	}
	if id == "system.cpu" {
		if len(values) == 0 {
			return m
		}
		for k, v := range values {
			if finiteValue(v) == nil || v < 0 {
				return m
			}
			if k != "idle" {
				value += v
			}
		}
	} else {
		used, ok := values["used"]
		if !ok {
			return m
		}
		if _, ok := values["free"]; !ok {
			return m
		}
		total := 0.0
		for _, k := range []string{"used", "free", "cached", "buffers"} {
			v := values[k]
			if finiteValue(v) == nil || v < 0 {
				return m
			}
			total += v
		}
		if total <= 0 {
			return m
		}
		value = used / total * 100
	}
	if finiteValue(value) == nil || value < 0 || value > 100.01 {
		return m
	}
	m.Value, m.State = finiteValue(math.Min(value, 100)), "fresh"
	return m
}

func problemID(node string, a health.Alarm) string {
	// Confirmation belongs to one severity transition, never the name forever.
	// Recovery/retrigger and escalation demand a new acknowledgement.
	b, _ := json.Marshal([]any{node, a.Chart, a.Name, a.ID, a.Status.String(), a.LastStatusChange})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Server) operationsSnapshot(r *http.Request) operationsSnapshot {
	now := time.Now().Unix()
	out := operationsSnapshot{Now: now, Nodes: []operationsNode{}, Problems: []problem{}, Persistent: s.operations.Persistent(), CurrentUser: userOf(r), Assignees: s.operationsAssignees(r),
		Summary: map[string]int{"nodes": 0, "live": 0, "stale": 0, "offline": 0, "critical": 0, "warning": 0, "unacknowledged": 0, "coverage_unknown": 0, "unassigned": 0, "open": 0, "investigating": 0, "watching": 0}}
	// Global snapshot ignores the current chart's node/status/contexts filter.
	request := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = ""
	request.URL = &u
	infos := s.nodesPayload(request, 1)["nodes"].([]hub.Info)
	for _, inf := range infos {
		n := operationsNode{Info: inf, CPU: resourceMetric{State: "unavailable"}, Memory: resourceMetric{State: "unavailable"}, AlarmCoverage: "unknown"}
		var alarms []health.Alarm
		v, ok := s.resolve(inf.ID)
		if ok {
			n.CPU = metricFor(v.reg, "system.cpu", inf.Status, now)
			n.Memory = metricFor(v.reg, "system.ram", inf.Status, now)
			if inf.Local {
				n.LastData = max(n.CPU.At, n.Memory.At)
				if s.opt.Health != nil {
					alarms = s.opt.Health.Alarms()
					n.AlarmCoverage = "local"
					if !s.opt.Health.Enabled() {
						n.AlarmCoverage = "disabled"
					} else if len(alarms) == 0 {
						n.AlarmCoverage = "empty"
					}
				} else {
					n.AlarmCoverage = "disabled"
				}
			} else if v.node != nil {
				n.AlarmCoverage = "mirrored"
				for _, e := range v.node.Alarms() {
					alarms = append(alarms, alarmFromEntry(e))
				}
			}
		} else if inf.Peer != "" && inf.ID != "" {
			if list, err := s.peerAlarms(r.Context(), inf.Peer, inf.ID); err == nil {
				alarms = list
				n.AlarmCoverage = "peer"
			}
		}
		if n.AlarmCoverage == "unknown" || n.AlarmCoverage == "disabled" || n.AlarmCoverage == "empty" {
			out.Summary["coverage_unknown"]++
		}
		out.Nodes = append(out.Nodes, n)
		out.Summary["nodes"]++
		out.Summary[inf.Status]++
		for _, a := range alarms {
			if a.Status != health.StatusCritical && a.Status != health.StatusWarning {
				continue
			}
			p := problem{ID: problemID(inf.ID, a), Node: inf.ID, Hostname: inf.Hostname, NodeStatus: inf.Status,
				Chart: a.Chart, Name: a.Name, Severity: a.Status.String(), Family: a.Family, Info: a.Info,
				Value: finiteValue(a.Value), Units: a.Units, Since: a.LastStatusChange, Updated: a.LastUpdated,
				Stale: n.AlarmCoverage == "disabled" || inf.Status != hub.StatusLive || a.LastUpdated <= 0 || now-a.LastUpdated > max(30, 3*a.Every)}
			p.Handling = s.operations.Get(p.ID)
			if n.AlarmCoverage == "local" && s.opt.Health != nil {
				if d, ok := s.opt.Health.LatestDelivery(a.Chart, a.Name); ok {
					p.Delivery = &problemDelivery{At: d.At, Channel: d.Channel, Outcome: d.Outcome, Reason: d.Reason}
				}
			}
			out.Problems = append(out.Problems, p)
			out.Summary[strings.ToLower(p.Severity)]++
			if !p.Handling.Acknowledged {
				out.Summary["unacknowledged"]++
			}
			if p.Handling.Assignee == "" {
				out.Summary["unassigned"]++
			}
			if p.Handling.Assignee != "" && p.Handling.Assignee == out.CurrentUser.Name {
				out.Summary["mine"]++
			}
			out.Summary[p.Handling.Status]++
		}
	}
	sort.Slice(out.Problems, func(i, j int) bool {
		a, b := out.Problems[i], out.Problems[j]
		if a.Severity != b.Severity {
			return a.Severity == "CRITICAL"
		}
		if a.Handling.Acknowledged != b.Handling.Acknowledged {
			return !a.Handling.Acknowledged
		}
		if a.Since != b.Since {
			return a.Since > b.Since
		}
		return a.ID < b.ID
	})
	out.Activity = s.operations.Recent()
	return out
}

func (s *Server) peerAlarms(ctx context.Context, peer, id string) ([]health.Alarm, error) {
	if s.opt.Cluster == nil {
		return nil, fmt.Errorf("no cluster")
	}
	body, code, err := s.opt.Cluster.FetchRelay(ctx, peer, "/api/v1/alarms?node="+url.QueryEscape(id))
	if err != nil {
		return nil, err
	}
	if code >= 300 {
		return nil, fmt.Errorf("peer alarms status %d", code)
	}
	var payload struct {
		Alarms map[string]health.Alarm `json:"alarms"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	out := make([]health.Alarm, 0, len(payload.Alarms))
	for _, a := range payload.Alarms {
		out = append(out, a)
	}
	return out, nil
}

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	snap := s.operationsSnapshot(r)
	limit, ok := operationsLimit(r)
	if !ok {
		http.Error(w, "invalid limit", 400)
		return
	}
	if limit > 0 {
		snap = pageOperations(snap, r, limit)
	}
	writeJSON(w, snap)
}

func operationsLimit(r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > 200 {
		return 0, false
	}
	return n, true
}

func pageOperations(snap operationsSnapshot, r *http.Request, limit int) operationsSnapshot {
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	nodeStatus := r.URL.Query().Get("node_status")
	if nodeStatus == "" {
		nodeStatus = "all"
	}
	severity := r.URL.Query().Get("severity")
	if severity == "" {
		severity = "all"
	}
	owner := r.URL.Query().Get("owner")
	if owner == "" {
		owner = "all"
	}
	progress := r.URL.Query().Get("progress")
	if progress == "" {
		progress = "all"
	}
	pending := r.URL.Query().Get("pending") == "1"
	me := snap.CurrentUser.Name
	nodes := make([]operationsNode, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		if nodeStatus != "all" && n.Status != nodeStatus {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(n.Hostname+" "+n.ID+" "+n.OS+" "+fmt.Sprint(n.Labels)), q) {
			continue
		}
		nodes = append(nodes, n)
	}
	sort.Slice(nodes, func(i, j int) bool {
		ci, cj := nodes[i].Alarms["critical"], nodes[j].Alarms["critical"]
		if ci != cj {
			return ci > cj
		}
		return nodes[i].Hostname < nodes[j].Hostname
	})
	problems := make([]problem, 0, len(snap.Problems))
	families := map[string]int{}
	for _, p := range snap.Problems {
		if severity != "all" && p.Severity != severity {
			continue
		}
		if nodeStatus != "all" && p.NodeStatus != nodeStatus {
			continue
		}
		if pending && p.Handling.Acknowledged {
			continue
		}
		switch owner {
		case "mine":
			if p.Handling.Assignee == "" || p.Handling.Assignee != me {
				continue
			}
		case "unassigned":
			if p.Handling.Assignee != "" {
				continue
			}
		case "assigned":
			if p.Handling.Assignee == "" {
				continue
			}
		}
		if progress != "all" && p.Handling.Status != progress {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(p.Hostname+" "+p.Node+" "+p.Name+" "+p.Chart+" "+p.Family+" "+p.Info+" "+p.Handling.Assignee), q) {
			continue
		}
		problems = append(problems, p)
		family := p.Family
		if family == "" {
			family = "其他"
		}
		families[family]++
	}
	page := &operationsPage{Limit: limit, NodesMatched: len(nodes), ProblemsMatched: len(problems), Families: []operationsFamily{}}
	for name, count := range families {
		page.Families = append(page.Families, operationsFamily{Name: name, Count: count})
	}
	sort.Slice(page.Families, func(i, j int) bool {
		if page.Families[i].Count != page.Families[j].Count {
			return page.Families[i].Count > page.Families[j].Count
		}
		return page.Families[i].Name < page.Families[j].Name
	})
	if len(page.Families) > 8 {
		page.Families = page.Families[:8]
	}
	if len(nodes) > limit {
		nodes = nodes[:limit]
	}
	if len(problems) > limit {
		problems = problems[:limit]
	}
	snap.Nodes, snap.Problems, snap.Page = nodes, problems, page
	return snap
}

func (s *Server) handleAcknowledgement(w http.ResponseWriter, r *http.Request) {
	s.handleProblemChange(w, r, false)
}

func (s *Server) handleHandling(w http.ResponseWriter, r *http.Request) {
	s.handleProblemChange(w, r, true)
}

func (s *Server) handleProblemChange(w http.ResponseWriter, r *http.Request, workflow bool) {
	var body struct {
		operations.Change
		ID       string  `json:"id"`
		Revision *uint64 `json:"revision"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil {
		http.Error(w, "invalid request", 400)
		return
	}
	if err := d.Decode(new(any)); err != io.EOF {
		http.Error(w, "expected one JSON object", 400)
		return
	}
	body.Note = strings.TrimSpace(body.Note)
	if body.Revision == nil || len(body.ID) != 64 || body.Change.Validate() != nil || (!workflow && body.Action != "acknowledge" && body.Action != "unacknowledge" && body.Action != "comment") {
		http.Error(w, "invalid handling change (note maximum 2048 UTF-8 bytes)", 400)
		return
	}
	if body.Action == "assign" {
		eligible := false
		for _, u := range s.operationsAssignees(r) {
			if u.Name == body.Assignee {
				eligible = true
				break
			}
		}
		if !eligible {
			http.Error(w, "assignee must be an available admin or troubleshooter", 400)
			return
		}
	}
	// Never let a delayed action confirm a newer severity/episode.
	found := false
	var target operations.Target
	for _, p := range s.operationsSnapshot(r).Problems {
		if p.ID == body.ID {
			found = true
			target = operations.Target{Node: p.Node, Hostname: p.Hostname, Chart: p.Chart, Name: p.Name, Severity: p.Severity, Since: p.Since}
			break
		}
	}
	if !found {
		http.Error(w, "problem recovered or changed; refresh before updating", 409)
		return
	}
	record, err := s.operations.Apply(body.ID, userOf(r).Name, body.Change, *body.Revision, target)
	if errors.Is(err, operations.ErrConflict) {
		http.Error(w, err.Error(), 409)
		return
	}
	if errors.Is(err, operations.ErrInvalidChange) {
		http.Error(w, err.Error(), 400)
		return
	}
	if err != nil {
		s.log.Error("persist problem handling", "err", err)
		http.Error(w, fmt.Sprintf("could not save handling record: %v", err), 503)
		return
	}
	s.postTicket(record)
	writeJSON(w, record)
}

func (s *Server) postTicket(records ...operations.Record) {
	raw := strings.TrimSpace(s.opt.TicketWebhook)
	if raw == "" || len(records) == 0 {
		return
	}
	if !strings.HasPrefix(raw, "https://") && !strings.HasPrefix(raw, "http://") {
		s.log.Warn("ticket webhook skipped", "reason", "unsupported scheme")
		return
	}
	body, err := json.Marshal(map[string]any{"event": "handling", "records": records})
	if err != nil {
		s.log.Warn("ticket webhook skipped", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, bytes.NewReader(body))
	if err != nil {
		s.log.Warn("ticket webhook skipped", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.log.Warn("ticket webhook failed", "err", err)
		return
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.log.Warn("ticket webhook rejected", "status", resp.StatusCode)
	}
}
