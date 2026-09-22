package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

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
		writeJSON(w, cfg)
		return
	}
	var cfg hub.NodeConfig
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&cfg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if cfg.NodeID == "" {
		cfg.NodeID = nodeID
	}
	out, err := org.SetConfig(cfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
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
	} else if s.opt.Token != "" || len(s.opt.Users) > 0 {
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
