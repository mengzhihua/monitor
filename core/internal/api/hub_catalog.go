package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hub"
)

func (s *Server) handleHostGroups(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"host_groups": org.HostGroups()})
	case http.MethodPost:
		var body struct {
			Name     string `json:"name"`
			ParentID string `json:"parent_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		group, err := org.CreateHostGroup(body.Name, body.ParentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, group)
	case http.MethodPut:
		var body struct {
			Name     string   `json:"name"`
			ParentID string   `json:"parent_id"`
			Nodes    []string `json:"nodes"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		group, err := org.UpdateHostGroup(r.URL.Query().Get("id"), body.Name, body.ParentID, body.Nodes)
		if err != nil {
			status := http.StatusBadRequest
			if err.Error() == "unknown host group" {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, group)
	case http.MethodDelete:
		if err := org.DeleteHostGroup(r.URL.Query().Get("id")); err != nil {
			status := http.StatusBadRequest
			if err.Error() == "unknown host group" {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProxies(w http.ResponseWriter, r *http.Request) {
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, map[string]any{"proxies": org.Proxies()})
	case http.MethodPost:
		var body struct {
			Name  string   `json:"name"`
			URL   string   `json:"url"`
			Mode  string   `json:"mode"`
			Nodes []string `json:"nodes"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		proxy, err := org.CreateProxy(body.Name, body.URL, body.Mode, body.Nodes)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, proxy)
	case http.MethodPut:
		var body struct {
			Name  string   `json:"name"`
			URL   string   `json:"url"`
			Mode  string   `json:"mode"`
			Nodes []string `json:"nodes"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		proxy, err := org.UpdateProxy(r.URL.Query().Get("id"), body.Name, body.URL, body.Mode, body.Nodes)
		if err != nil {
			status := http.StatusBadRequest
			if err.Error() == "unknown proxy" {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		writeJSON(w, proxy)
	case http.MethodDelete:
		if err := org.DeleteProxy(r.URL.Query().Get("id")); err != nil {
			status := http.StatusBadRequest
			if err.Error() == "unknown proxy" {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleProxyHeartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	org := s.requireOrg(w)
	if org == nil {
		return
	}
	var body struct {
		ID    string `json:"id"`
		Token string `json:"token"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.Token == "" {
		body.Token = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	if err := org.TouchProxy(body.ID, body.Token, time.Now()); err != nil {
		status := http.StatusBadRequest
		if err.Error() == "unknown proxy" {
			status = http.StatusNotFound
		}
		if err.Error() == "unauthorized" {
			status = http.StatusUnauthorized
		}
		http.Error(w, err.Error(), status)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleLeader(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.opt.Elector == nil {
		writeJSON(w, hub.LeaderStatus{Mode: "single", Active: true})
		return
	}
	writeJSON(w, s.opt.Elector.Status())
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.opt.Elector == nil {
		http.Error(w, "election is not configured", http.StatusNotFound)
		return
	}
	if !s.peerAuthorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body hub.Ballot
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, s.opt.Elector.Accept(body))
}

func (s *Server) peerAuthorized(r *http.Request) bool {
	if s.opt.PeerToken != "" {
		return strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") == s.opt.PeerToken
	}
	if s.opt.Token != "" || len(s.opt.Users) > 0 || s.oidc != nil || s.ldap != nil {
		return false
	}
	return true
}
