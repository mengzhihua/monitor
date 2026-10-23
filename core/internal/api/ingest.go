package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/ingest"
)

func (s *Server) handleIngestOpenMetrics(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	samples := ingest.ParseOpenMetrics(string(body))
	n := s.ingest.Apply(s.reg, time.Now(), samples)
	writeJSON(w, map[string]any{"samples": n, "parsed": len(samples)})
}

func (s *Server) handleIngestOTLP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	samples, err := ingest.ParseOTLP(body, r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n := s.otlp.Apply(s.reg, time.Now(), samples)
	if strings.HasPrefix(r.URL.Path, "/v1/") {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"partialSuccess":{}}`)
		return
	}
	writeJSON(w, map[string]any{"samples": n, "parsed": len(samples)})
}
