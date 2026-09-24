package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/config"
	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/registry"
)

func (s *Server) requireOrg(w http.ResponseWriter) *hub.Org {
	if s.opt.Org == nil {
		http.Error(w, "hub org not enabled", http.StatusNotFound)
		return nil
	}
	return s.opt.Org
}

func (s *Server) handleSpaces(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"spaces": org.Spaces()})
	case http.MethodPost:
		var body struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && err != io.EOF {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		sp, err := org.CreateSpace(body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, sp)
	case http.MethodDelete:
		id := r.URL.Query().Get("id")
		if err := org.DeleteSpace(id); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRooms(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	q := r.URL.Query()
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"rooms": org.Rooms(q.Get("space_id"))})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			SpaceID string `json:"space_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if body.SpaceID == "" {
			body.SpaceID = q.Get("space_id")
		}
		rm, err := org.CreateRoom(body.SpaceID, body.Name)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, rm)
	case http.MethodPut:
		var body struct {
			Nodes []string `json:"nodes"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rm, err := org.SetRoomNodes(q.Get("id"), body.Nodes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, rm)
	case http.MethodDelete:
		if err := org.DeleteRoom(q.Get("id")); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleClaimTokens(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	if r.Method == http.MethodGet {
		writeJSON(w, map[string]any{"claims": org.Claims()})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		SpaceID string `json:"space_id"`
		RoomID  string `json:"room_id"`
		TTL     string `json:"ttl"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil && err != io.EOF {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ttl := 24 * time.Hour
	if body.TTL != "" {
		if d, err := time.ParseDuration(body.TTL); err == nil {
			ttl = d
		}
	}
	c, err := org.IssueClaim(body.SpaceID, body.RoomID, ttl)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, c)
}

func (s *Server) handleClaimRedeem(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	var body struct {
		Token  string `json:"token"`
		NodeID string `json:"node_id"`
		Host   string `json:"hostname"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.NodeID == "" {
		body.NodeID = body.Host
	}
	c, err := org.RedeemClaim(body.Token, body.NodeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"api_key": c.APIKey, "node_id": c.NodeID, "space_id": c.SpaceID, "room_id": c.RoomID,
	})
}

// handleHubConfig is GET/PUT /api/v1/hub/config?node=<id>: the desired
// per-node config (disabled overlay + optional full yaml replacement) plus the
// agent's reported file and apply outcome. The PUT body is decoded into a
// narrow struct so a caller can never overwrite the agent-reported state.
func (s *Server) handleHubConfig(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	nodeID := r.URL.Query().Get("node")
	if r.Method == http.MethodGet {
		cfg, ok := org.GetConfig(nodeID)
		if !ok {
			http.Error(w, "no config", http.StatusNotFound)
			return
		}
		out := s.hubConfigPayload(cfg)
		if userOf(r).Role != RoleAdmin {
			// the desired and reported files carry credentials
			out["yaml"], out["reported"] = "", ""
		}
		writeJSON(w, out)
		return
	}
	if nodeID == "" {
		http.Error(w, "node parameter required", http.StatusBadRequest)
		return
	}
	var body struct {
		YAML      string   `json:"yaml"`
		Disabled  []string `json:"disabled"`
		IfUpdated int64    `json:"if_updated"` // Updated observed by a previous GET; 0 = no check
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.YAML) > maxConfigYAML {
		http.Error(w, "config exceeds 256KiB limit", http.StatusBadRequest)
		return
	}
	cur, _ := org.GetConfig(nodeID)
	if body.IfUpdated != 0 && body.IfUpdated != cur.Updated {
		http.Error(w, "config changed since read", http.StatusConflict)
		return
	}
	if body.YAML != "" { // an empty yaml is the legacy disabled-only update
		if _, err := config.Parse([]byte(body.YAML)); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := checkAgentLocks(cur.Reported, body.YAML); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	in := hub.NodeConfig{NodeID: nodeID, Disabled: body.Disabled, YAML: body.YAML}
	if in.YAML == "" {
		in.YAML = cur.YAML // legacy disabled-only update keeps the stored yaml
	}
	out, err := org.SetConfig(in)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	pushed := false
	if s.opt.Nodes != nil {
		if n, ok := s.opt.Nodes.Get(nodeID); ok {
			pushed = n.PushConfig(out)
		}
	}
	resp := s.hubConfigPayload(out)
	resp["pushed"] = pushed
	writeJSON(w, resp)
}

// checkAgentLocks rejects a yaml replacement that changes fields the agent
// must keep: rewriting them remotely could cut the agent's path back to the
// hub. The reference point is what the agent last reported on its file.
func checkAgentLocks(reported, next string) error {
	if reported == "" {
		return nil
	}
	cur, err := config.Parse([]byte(reported))
	if err != nil {
		return nil // nothing reliable to compare against
	}
	nxt, err := config.Parse([]byte(next))
	if err != nil {
		return nil // already rejected by the caller's Parse check
	}
	if err := config.CheckLocked(cur, nxt); err != nil {
		return fmt.Errorf("agent config locks mode/stream.destinations/stream.api_key: %w", err)
	}
	return nil
}

// hubConfigPayload renders a stored NodeConfig with its live connection state.
func (s *Server) hubConfigPayload(cfg hub.NodeConfig) map[string]any {
	online := false
	if s.opt.Nodes != nil {
		if n, ok := s.opt.Nodes.Get(cfg.NodeID); ok {
			online = n.Online()
		}
	}
	pending := cfg.Updated > 0 && (cfg.Apply == nil || cfg.Apply.Rev != cfg.Updated || cfg.Apply.State != "applied")
	return map[string]any{
		"node_id": cfg.NodeID, "disabled": cfg.Disabled, "yaml": cfg.YAML, "updated": cfg.Updated,
		"reported": cfg.Reported, "report_at": cfg.ReportAt, "apply": cfg.Apply,
		"online": online, "pending": pending,
	}
}

func (s *Server) handleAgentConfig(w http.ResponseWriter, r *http.Request) {
	if s.opt.Org == nil || s.opt.Nodes == nil {
		http.Error(w, "hub org not enabled", http.StatusNotFound)
		return
	}
	if !s.opt.Nodes.Authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	key := hub.StreamKey(r)
	cfg, ok := s.opt.Org.ConfigByKey(key)
	if !ok {
		nodeID := r.URL.Query().Get("node")
		if nodeID == "" {
			writeJSON(w, hub.NodeConfig{})
			return
		}
		cfg, ok = s.opt.Org.GetConfig(nodeID)
		if !ok {
			writeJSON(w, hub.NodeConfig{NodeID: nodeID})
			return
		}
	}
	writeJSON(w, cfg)
}

func (s *Server) handleRing(w http.ResponseWriter, r *http.Request) {
	if s.opt.Nodes == nil {
		http.Error(w, "not a hub", http.StatusNotFound)
		return
	}
	if s.opt.PeerToken != "" {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got != s.opt.PeerToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	} else if s.opt.Token != "" || len(s.opt.Users) > 0 || s.oidc != nil || s.ldap != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body hub.RingPayload
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var charts []*registry.Chart
	for _, d := range body.Charts {
		if d != nil {
			charts = append(charts, d.ToChart())
		}
	}
	node, err := s.opt.Nodes.AcceptReplica(body.Host, charts, body.Samples, time.Now())
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	writeJSON(w, map[string]any{"node": node.ID, "replica": true, "samples": len(body.Samples)})
}

// handleConsole is GET /api/v1/hub/console: Space→Room→node topology, ACLK
// summary, alarm routing and raised alarms (Cloud product surface).
func (s *Server) handleConsole(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	now := time.Now()
	assigned := map[string]bool{}
	spaceOut := make([]map[string]any, 0)
	for _, sp := range org.Spaces() {
		rooms := org.Rooms(sp.ID)
		roomOut := make([]map[string]any, 0, len(rooms))
		for _, rm := range rooms {
			var nodes []map[string]any
			var warn, crit int
			var alarms []any
			seen := map[string]bool{}
			ids := append([]string{}, rm.Nodes...)
			if s.opt.Nodes != nil {
				for _, n := range s.opt.Nodes.List() {
					_, rid := org.Membership(n.ID)
					if rid == rm.ID && !seen[n.ID] {
						ids = append(ids, n.ID)
					}
				}
			}
			for _, nid := range ids {
				if seen[nid] {
					continue
				}
				seen[nid] = true
				assigned[nid] = true
				item, raised := s.consoleNode(nid, now)
				nodes = append(nodes, item)
				if a, ok := item["alarms"].(map[string]int); ok {
					warn += a["warning"]
					crit += a["critical"]
				}
				alarms = append(alarms, raised...)
			}
			roomOut = append(roomOut, map[string]any{
				"id": rm.ID, "name": rm.Name, "nodes": nodes,
				"alarms":     map[string]int{"warning": warn, "critical": crit},
				"alarm_list": alarms,
			})
		}
		spaceOut = append(spaceOut, map[string]any{"id": sp.ID, "name": sp.Name, "created": sp.Created, "rooms": roomOut})
	}
	var unassigned []map[string]any
	if s.opt.Nodes != nil {
		for _, n := range s.opt.Nodes.List() {
			if assigned[n.ID] {
				continue
			}
			item, _ := s.consoleNode(n.ID, now)
			unassigned = append(unassigned, item)
		}
	}
	routing := map[string]any{"roles": map[string][]string{}, "channels": []any{}}
	if s.opt.Health != nil {
		routing = s.opt.Health.RoutingInfo()
	}
	aclk := map[string]any{"available": true, "protocol": "stream+mqtt", "storage": "full"}
	if s.opt.Nodes != nil {
		live := 0
		for _, n := range s.opt.Nodes.List() {
			if n.Info(now).Status == hub.StatusLive {
				live++
			}
		}
		aclk["online"] = s.opt.Nodes.IngestEnabled() && live > 0
		aclk["nodes"] = len(s.opt.Nodes.List())
		aclk["live"] = live
		aclk["storage"] = s.opt.Nodes.Storage()
	}
	writeJSON(w, map[string]any{
		"spaces": spaceOut, "unassigned": unassigned, "routing": routing, "aclk": aclk,
	})
}

func (s *Server) consoleNode(id string, now time.Time) (map[string]any, []any) {
	item := map[string]any{"id": id, "hostname": id, "status": hub.StatusOffline, "aclk": false, "protocol": "", "alarms": map[string]int{"warning": 0, "critical": 0}}
	var raised []any
	if s.opt.Nodes == nil {
		return item, raised
	}
	n, ok := s.opt.Nodes.Get(id)
	if !ok {
		return item, raised
	}
	inf := n.Info(now)
	item["hostname"] = inf.Hostname
	item["status"] = inf.Status
	item["aclk"] = inf.ACLK
	item["protocol"] = inf.Protocol
	item["alarms"] = inf.Alarms
	item["charts_count"] = inf.ChartsCount
	item["last_seen"] = inf.LastSeen
	for _, e := range n.Alarms() {
		if e.Status == health.StatusWarning || e.Status == health.StatusCritical {
			raised = append(raised, e)
		}
	}
	return item, raised
}
