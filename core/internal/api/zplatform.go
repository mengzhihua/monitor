package api

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/collect"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/registry"
	"github.com/mengzhihua/monitor/core/internal/sla"
)

// Command is a whitelisted argv the admin API may run. Argv[0] is an absolute path.
type Command struct {
	Name string   `json:"name"`
	Argv []string `json:"argv"`
}

func (s *Server) handleServices(w http.ResponseWriter, r *http.Request) {
	from, to := sla.Window(time.Now(), r.URL.Query().Get("window"))
	writeJSON(w, sla.Build(s.opt.Services, s.alarmSnapshot(), s.alarmLog(), from, to))
}

func (s *Server) handleAvailability(w http.ResponseWriter, r *http.Request) {
	from, to := sla.Window(time.Now(), r.URL.Query().Get("window"))
	rows := sla.AlarmAvailability(s.alarmSnapshot(), s.alarmLog(), from, to)
	if rows == nil {
		rows = []sla.Availability{}
	}
	if r.URL.Query().Get("format") == "csv" {
		w.Header().Set("Content-Type", "text/csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"name", "chart", "uptime", "raised_seconds", "window_seconds"})
		for _, row := range rows {
			_ = cw.Write([]string{row.Name, row.Chart, strconv.FormatFloat(row.Uptime, 'f', 2, 64), strconv.FormatInt(row.Raised, 10), strconv.FormatInt(row.Window, 10)})
		}
		cw.Flush()
		return
	}
	writeJSON(w, map[string]any{"from": from, "to": to, "alarms": rows})
}

func (s *Server) handleTopology(w http.ResponseWriter, r *http.Request) {
	type edge struct {
		Source string `json:"source"`
		Target string `json:"target"`
		Kind   string `json:"kind"`
	}
	host := s.reg.Host.Hostname
	var edges []edge
	for _, ch := range s.reg.Charts() {
		if ch.Context != "snmp_topology.lldp_neighbor" {
			continue
		}
		target := ch.Labels["neighbor"]
		if target == "" {
			continue
		}
		edges = append(edges, edge{Source: host, Target: target, Kind: "lldp"})
	}
	for _, link := range s.opt.Links {
		if link.Source == "" || link.Target == "" {
			continue
		}
		edges = append(edges, edge{Source: link.Source, Target: link.Target, Kind: "manual"})
	}
	if edges == nil {
		edges = []edge{}
	}
	writeJSON(w, map[string]any{"edges": edges})
}

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	if userOf(r).Role != RoleAdmin || userOf(r).principal == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	var argv []string
	for _, c := range s.opt.Commands {
		if c.Name == body.Name {
			argv = append([]string(nil), c.Argv...)
			break
		}
	}
	if err := validateArgv(argv); err != nil {
		http.Error(w, "unknown command", http.StatusBadRequest)
		return
	}
	cmd := exec.CommandContext(r.Context(), argv[0], argv[1:]...)
	out, err := cmd.CombinedOutput()
	msg := strings.TrimSpace(string(out))
	if len(msg) > 4000 {
		msg = msg[:4000]
	}
	code := 0
	if err != nil {
		code = 1
	}
	s.auditEvent(r, "command:"+body.Name, userOf(r).Name)
	writeJSON(w, map[string]any{"name": body.Name, "exit": code, "output": msg})
}

func validateArgv(argv []string) error {
	if len(argv) == 0 || len(argv) > 8 || !filepath.IsAbs(argv[0]) {
		return os.ErrInvalid
	}
	for _, a := range argv {
		if strings.ContainsAny(a, "\n\r") {
			return os.ErrInvalid
		}
	}
	return nil
}

func (s *Server) handleDiscovery(w http.ResponseWriter, r *http.Request) {
	if userOf(r).Role != RoleAdmin || userOf(r).principal == "" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		CIDR  string `json:"cidr"`
		Ports []int  `json:"ports"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(body.Ports) == 0 || len(body.Ports) > 8 {
		http.Error(w, "1..8 ports", http.StatusBadRequest)
		return
	}
	hosts, open := collect.ScanCIDR(r.Context(), body.CIDR, body.Ports, 300*time.Millisecond)
	if hosts == 0 {
		http.Error(w, "cidr must be an IPv4 network of at most 256 addresses", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"cidr": body.CIDR, "hosts": hosts, "open": open})
}

func (s *Server) alarmSnapshot() []health.Alarm {
	if s.opt.Health == nil {
		return nil
	}
	return s.opt.Health.Alarms()
}

func (s *Server) alarmLog() []health.LogEntry {
	if s.opt.Health == nil {
		return nil
	}
	return s.opt.Health.Log(0)
}

func (s *Server) roomAllowed(u User, nodeID string) bool {
	if len(u.Rooms) == 0 || nodeID == "" || nodeID == "local" || s.isLocal(nodeID) || s.opt.Nodes == nil {
		return true
	}
	node, ok := s.opt.Nodes.Get(nodeID)
	if !ok {
		return true
	}
	room := node.Info(time.Now()).RoomID
	if room == "" {
		return true
	}
	for _, allowed := range u.Rooms {
		if allowed == room || allowed == "*" {
			return true
		}
	}
	return false
}

func (s *Server) selfMonitor() {
	s.reg.AddChart(&registry.Chart{ID: "monitor.notify_queue", Context: "monitor.notify_queue", Title: "Notification queue",
		Units: "entries", Family: "monitor", Plugin: "monitor", Module: "self", Priority: 100,
		Dimensions: []*registry.Dimension{{ID: "depth"}}})
	s.reg.AddChart(&registry.Chart{ID: "monitor.hub_nodes", Context: "monitor.hub_nodes", Title: "Hub nodes",
		Units: "nodes", Family: "monitor", Plugin: "monitor", Module: "self", Priority: 110,
		Dimensions: []*registry.Dimension{{ID: "live"}, {ID: "offline"}}})
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	s.sampleSelf(time.Now())
	for now := range t.C {
		s.sampleSelf(now)
	}
}

func (s *Server) sampleSelf(now time.Time) {
	depth := 0
	if s.opt.Health != nil {
		depth = s.opt.Health.NotificationDiagnostics().QueueSize
	}
	_ = s.reg.Collect("monitor.notify_queue", now, map[string]float64{"depth": float64(depth)})
	live, offline := 0, 0
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			switch n.Status(now) {
			case "live":
				live++
			default:
				offline++
			}
		}
	}
	_ = s.reg.Collect("monitor.hub_nodes", now, map[string]float64{"live": float64(live), "offline": float64(offline)})
}

func (s *Server) exportReports() {
	if s.opt.ReportEvery <= 0 || s.opt.ReportDir == "" {
		return
	}
	t := time.NewTicker(s.opt.ReportEvery)
	defer t.Stop()
	s.writeReport(time.Now())
	for now := range t.C {
		s.writeReport(now)
	}
}

func (s *Server) writeReport(now time.Time) {
	if err := os.MkdirAll(s.opt.ReportDir, 0o755); err != nil {
		return
	}
	from, to := sla.Window(now, "")
	rows := sla.AlarmAvailability(s.alarmSnapshot(), s.alarmLog(), from, to)
	path := filepath.Join(s.opt.ReportDir, "availability-"+strconv.FormatInt(to, 10)+".csv")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	cw := csv.NewWriter(f)
	_ = cw.Write([]string{"name", "chart", "uptime", "raised_seconds", "window_seconds"})
	for _, row := range rows {
		_ = cw.Write([]string{row.Name, row.Chart, strconv.FormatFloat(row.Uptime, 'f', 2, 64), strconv.FormatInt(row.Raised, 10), strconv.FormatInt(row.Window, 10)})
	}
	cw.Flush()
}
