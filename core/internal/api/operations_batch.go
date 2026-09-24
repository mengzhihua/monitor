package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/operations"
)

// One requested action applies to explicitly selected episodes only. The server
// never re-runs a client-side filter to discover additional batch members.
func (s *Server) handleHandlingBatch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var body struct {
		operations.Change
		Items []struct {
			ID       string  `json:"id"`
			Revision *uint64 `json:"revision"`
		} `json:"items"`
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
	d.DisallowUnknownFields()
	if err := d.Decode(&body); err != nil || d.Decode(new(any)) != io.EOF {
		http.Error(w, "invalid batch request", http.StatusBadRequest)
		return
	}
	body.Note = strings.TrimSpace(body.Note)
	if len(body.Items) == 0 || len(body.Items) > operations.BatchLimit || body.Change.Validate() != nil {
		http.Error(w, "batch requires 1-50 unique episodes and a valid change (note maximum 2048 UTF-8 bytes)", http.StatusBadRequest)
		return
	}
	seen := map[string]bool{}
	for _, item := range body.Items {
		id, err := hex.DecodeString(item.ID)
		if err != nil || len(id) != 32 || item.Revision == nil || seen[item.ID] {
			http.Error(w, "each batch item requires a unique episode ID and revision", http.StatusBadRequest)
			return
		}
		seen[item.ID] = true
	}
	if body.Action == "assign" {
		eligible := false
		for _, user := range s.operationsAssignees(r) {
			if user.Name == body.Assignee {
				eligible = true
				break
			}
		}
		if !eligible {
			http.Error(w, "assignee must be an available admin or troubleshooter", http.StatusBadRequest)
			return
		}
	}
	active := map[string]problem{}
	for _, p := range s.operationsSnapshot(r).Problems {
		active[p.ID] = p
	}
	items := make([]operations.BatchItem, 0, len(body.Items))
	for _, item := range body.Items {
		p, ok := active[item.ID]
		if !ok {
			http.Error(w, "one or more episodes recovered or changed; no batch changes saved", http.StatusConflict)
			return
		}
		items = append(items, operations.BatchItem{ID: item.ID, Revision: *item.Revision, Target: operations.Target{
			Node: p.Node, Hostname: p.Hostname, Chart: p.Chart, Name: p.Name, Severity: p.Severity, Since: p.Since,
		}})
	}
	records, err := s.operations.ApplyBatch(items, userOf(r).Name, body.Change)
	switch {
	case errors.Is(err, operations.ErrConflict):
		http.Error(w, "one or more records changed; no batch changes saved", http.StatusConflict)
	case errors.Is(err, operations.ErrInvalidChange):
		http.Error(w, "invalid batch change", http.StatusBadRequest)
	case err != nil:
		s.log.Error("persist batch handling", "error", err)
		http.Error(w, "could not save batch; no batch changes saved", http.StatusServiceUnavailable)
	default:
		s.postTicket(records...)
		writeJSON(w, map[string]any{"records": records, "count": len(records)})
	}
}
