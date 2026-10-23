package api

import (
	"net/http"
)

// handleData implements /api/v1/data?chart=&context=&after=&before=&points=&group=&dimensions=&format=
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	s.serveData(w, r, 1)
}

func (s *Server) handleDataV2(w http.ResponseWriter, r *http.Request) {
	s.serveData(w, r, 2)
}

func (s *Server) handleDataV3(w http.ResponseWriter, r *http.Request) {
	s.serveData(w, r, 3)
}

func (s *Server) serveData(w http.ResponseWriter, r *http.Request, api int) {
	q := r.URL.Query()
	gb := parseGroupBy(q.Get("group_by"))
	if hasGroup(gb, "node") {
		if hasGroup(gb, "dimension") {
			s.serveDataByNodeDim(w, r, api)
			return
		}
		s.serveDataByNode(w, r, api)
		return
	}
	v, ok := s.target(w, r)
	if !ok {
		return
	}
	if v.node != nil && s.opt.Nodes != nil && s.opt.Nodes.Storage() == "proxy" && v.node.Online() {
		args := map[string]string{"chart": q.Get("chart"), "after": q.Get("after"), "before": q.Get("before"), "points": q.Get("points")}
		raw, err := v.node.Query(r.Context(), args)
		if err == nil && len(raw) > 0 {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(raw)
			return
		}
	}
	out, code, errMsg := s.queryData(v, q, api)
	if errMsg != "" {
		http.Error(w, errMsg, code)
		return
	}
	writeData(w, q, out)
}
