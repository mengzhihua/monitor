package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
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
}

type operationsSnapshot struct {
	Activity   []operations.Record `json:"activity"`
	Now        int64               `json:"now"`
	Nodes      []operationsNode    `json:"nodes"`
	Problems   []problem           `json:"problems"`
	Summary    map[string]int      `json:"summary"`
	Persistent bool                `json:"persistent"`
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
	out := operationsSnapshot{Now: now, Nodes: []operationsNode{}, Problems: []problem{}, Persistent: s.operations.Persistent(),
		Summary: map[string]int{"nodes": 0, "live": 0, "stale": 0, "offline": 0, "critical": 0, "warning": 0, "unacknowledged": 0, "coverage_unknown": 0}}
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
			out.Problems = append(out.Problems, p)
			out.Summary[strings.ToLower(p.Severity)]++
			if !p.Handling.Acknowledged {
				out.Summary["unacknowledged"]++
			}
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

func (s *Server) handleOperations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, s.operationsSnapshot(r))
}

func (s *Server) handleAcknowledgement(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string  `json:"id"`
		Action   string  `json:"action"`
		Note     string  `json:"note"`
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
	if body.Revision == nil || len(body.ID) != 64 || len(body.Note) > 2048 || (body.Action != "acknowledge" && body.Action != "unacknowledge" && body.Action != "comment") || (body.Action == "comment" && body.Note == "") {
		http.Error(w, "invalid action or note (maximum 2048 bytes)", 400)
		return
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
	record, err := s.operations.Update(body.ID, userOf(r).Name, body.Action, body.Note, *body.Revision, target)
	if errors.Is(err, operations.ErrConflict) {
		http.Error(w, err.Error(), 409)
		return
	}
	if err != nil {
		s.log.Error("persist acknowledgement", "err", err)
		http.Error(w, fmt.Sprintf("could not save acknowledgement: %v", err), 503)
		return
	}
	writeJSON(w, record)
}
