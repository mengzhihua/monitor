package api

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
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
	if v.id != "" && s.opt.Org != nil {
		if base, ok := s.opt.Org.ProxyRoute(v.id, time.Now()); ok && s.relayProxy(w, r, base) {
			return
		}
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

// relayProxy forwards a successful data read to the proxy that owns the node.
// Any other result falls through to local storage.
func (s *Server) relayProxy(w http.ResponseWriter, r *http.Request, base string) bool {
	target, err := url.Parse(strings.TrimRight(base, "/") + r.URL.RequestURI())
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	if auth := r.Header.Get("Authorization"); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false
	}
	var first [1]byte
	n, readErr := resp.Body.Read(first[:])
	if n == 0 {
		return false
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	if _, err = w.Write(first[:n]); err != nil {
		return true
	}
	if readErr == io.EOF {
		return true
	}
	// Stream the rest. The response has started, so a later copy error must
	// not fall through to a second local body.
	_, _ = io.Copy(w, resp.Body)
	return true
}
