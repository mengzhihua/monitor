package api

import (
	"net/http"
	"time"

	"github.com/mengzhihua/monitor/core/internal/hub"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	s.serveInfo(w, r, 0)
}

func (s *Server) handleInfoV3(w http.ResponseWriter, r *http.Request) {
	s.serveInfo(w, r, 3)
}

func (s *Server) serveInfo(w http.ResponseWriter, r *http.Request, api int) {
	h := s.reg.Host
	charts := s.reg.Charts()
	var dims int
	for _, c := range charts {
		dims += len(c.Dims())
	}
	mode := s.opt.Mode
	if mode == "" {
		mode = "agent"
	}
	out := map[string]any{
		"version":       s.opt.Version,
		"mode":          mode,
		"host":          h,
		"uptime":        int64(time.Since(s.opt.StartedAt).Seconds()),
		"charts_count":  len(charts),
		"metrics_count": dims,
		"collectors":    s.sched.Status(),
		"plugins":       s.pluginStatus(),
		"platform":      s.platformInfo(r),
		"alarms":        s.alarmSummary(),
		"db": map[string]any{
			"tiers": s.db.Tiers(), "dir": s.db.Dir(), "persistence": s.db.Persistence(),
			"block_size": s.db.BlockSize(),
		},
		"user": userOf(r),
	}
	if s.opt.Commit != "" {
		out["commit"] = s.opt.Commit
	}
	if s.opt.BuildTime != "" {
		out["build_time"] = s.opt.BuildTime
	}
	if s.opt.Commit != "" || s.opt.BuildTime != "" || s.opt.Dirty {
		out["dirty"] = s.opt.Dirty
	}
	var nodes []*hub.Node
	if s.opt.Nodes != nil {
		nodes = s.opt.Nodes.List()
		if !unrestrictedRooms(userOf(r)) {
			visible := nodes[:0]
			for _, node := range nodes {
				if s.roomAllowed(userOf(r), node.ID) {
					visible = append(visible, node)
				}
			}
			nodes = visible
		}
		out["nodes_count"] = len(nodes) + 1
		out["streaming_enabled"] = s.opt.Nodes.IngestEnabled()
	}
	if s.opt.Stream != nil {
		out["stream"] = s.opt.Stream.Status()
	}
	aclk := map[string]any{
		"available":    false,
		"online":       false,
		"protocol":     "stream",
		"claimed":      false,
		"capabilities": stream.ACLKCapabilities,
	}
	if s.opt.Nodes != nil {
		now := time.Now()
		live := 0
		for _, n := range nodes {
			if n.Status(now) == hub.StatusLive {
				live++
			}
		}
		aclk["available"] = true
		aclk["online"] = s.opt.Nodes.IngestEnabled() && live > 0
		aclk["nodes"] = len(nodes)
		aclk["live"] = live
		aclk["protocol"] = "stream+mqtt"
		aclk["storage"] = s.opt.Nodes.Storage()
	} else if s.opt.Stream != nil {
		st := s.opt.Stream.Status()
		aclk["available"] = st.Enabled
		aclk["online"] = st.Connected
		aclk["destination"] = st.Destination
		aclk["protocol"] = st.Protocol
		if st.Protocol == "" {
			aclk["protocol"] = "stream"
		}
		aclk["claimed"] = st.Claimed
		aclk["next_connection"] = st.NextConnection
		aclk["last_error"] = st.LastError
		aclk["sent"] = st.Sent
		aclk["reconnects"] = st.Reconnects
		if len(st.Capabilities) > 0 {
			aclk["capabilities"] = st.Capabilities
		}
	}
	out["aclk"] = aclk
	if api > 0 {
		out["api"] = api
	}
	writeJSON(w, out)
}

func (s *Server) alarmSummary() any {
	if s.opt.Health == nil {
		return nil
	}
	return s.opt.Health.Summary()
}
