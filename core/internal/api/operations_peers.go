package api

import (
	"context"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/hub"
)

const operationPeerConcurrency = 8
const operationPeerBudget = 5 * time.Second

type operationPeerResult struct {
	alarms []health.Alarm
	ok     bool
}

// Keep the whole peer phase bounded, including waiting for other viewers. A
// failed or unstarted read keeps unknown coverage, never a healthy empty list.
// The server-wide slots prevent simultaneous overviews multiplying peer load.
func (s *Server) operationPeerAlarms(ctx context.Context, infos []hub.Info, views []*view) []operationPeerResult {
	out := make([]operationPeerResult, len(infos))
	var pending []int
	for i, inf := range infos {
		if views[i] == nil && inf.Peer != "" && inf.ID != "" {
			pending = append(pending, i)
		}
	}
	if len(pending) == 0 || ctx.Err() != nil {
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, operationPeerBudget)
	defer cancel()
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(operationPeerConcurrency, len(pending)) {
		workers.Go(func() {
			for i := range jobs {
				if ctx.Err() != nil {
					return
				}
				select {
				case s.operationPeerSlots <- struct{}{}:
				case <-ctx.Done():
					return
				}
				alarms, err := s.peerAlarms(ctx, infos[i].Peer, infos[i].ID)
				<-s.operationPeerSlots
				if err == nil {
					out[i] = operationPeerResult{alarms: alarms, ok: true}
				}
			}
		})
	}
queue:
	for _, i := range pending {
		select {
		case jobs <- i:
		case <-ctx.Done():
			break queue
		}
	}
	close(jobs)
	workers.Wait()
	return out
}
