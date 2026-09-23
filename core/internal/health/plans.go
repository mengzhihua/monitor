package health

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const MaintenancePlanLimit = 50
const MaintenanceHistoryLimit = 200
const maxPlanBytes = 4 << 20
const maxPlanRevision = 1<<53 - 1
const maxPlanDuration = 7 * 86400
const maxPlanAhead = 90 * 86400

var ErrPlanInvalid = errors.New("invalid maintenance plan")
var ErrPlanConflict = errors.New("maintenance plans changed or plan already ended")
var ErrPlanCapacity = errors.New("maintenance plan capacity reached")

type MaintenanceSpec struct {
	Title           string `json:"title"`
	Reason          string `json:"reason"`
	Scope           string `json:"scope"` // all or an exact chart/alarm pair
	Chart           string `json:"chart"`
	Alarm           string `json:"alarm"`
	StartsAt        int64  `json:"starts_at"` // create: zero means server time now
	DurationSeconds int64  `json:"duration_seconds"`
}

type MaintenancePlan struct {
	MaintenanceSpec
	ID           string `json:"id"`
	EndsAt       int64  `json:"ends_at"`
	CreatedAt    int64  `json:"created_at"`
	CreatedBy    string `json:"created_by"`
	CanceledAt   int64  `json:"canceled_at,omitempty"`
	CanceledBy   string `json:"canceled_by,omitempty"`
	CancelReason string `json:"cancel_reason,omitempty"`
}

func (p MaintenancePlan) Status(now int64) string {
	if p.CanceledAt != 0 {
		return "canceled"
	}
	if now >= p.EndsAt {
		return "ended"
	}
	if now < p.StartsAt {
		return "scheduled"
	}
	return "active"
}

type MaintenancePlanView struct {
	MaintenancePlan
	State string `json:"state"`
}

type MaintenancePlanSnapshot struct {
	Revision uint64                `json:"revision"`
	Plans    []MaintenancePlanView `json:"plans"`
}

type maintenancePlanState struct {
	Version  int               `json:"version"`
	Revision uint64            `json:"revision"`
	Plans    []MaintenancePlan `json:"plans"`
}

// Store has its own lock. A plan is published to the evaluator only after the
// durable write succeeds; the API never acquires the engine lock while saving.
type MaintenancePlanStore struct {
	mu    sync.RWMutex
	path  string
	state maintenancePlanState
}

func validPlanText(s string, max int) bool {
	return s != "" && strings.TrimSpace(s) == s && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func validPlanSpec(p MaintenanceSpec) bool {
	if !validPlanText(p.Title, 160) || !validPlanText(p.Reason, 2048) || p.DurationSeconds < 1 || p.DurationSeconds > maxPlanDuration {
		return false
	}
	switch p.Scope {
	case "all":
		return p.Chart == "" && p.Alarm == ""
	case "alarm":
		return validPlanText(p.Chart, 256) && validPlanText(p.Alarm, 256)
	default:
		return false
	}
}

// Damaged files fail startup instead of silently losing active maintenance.
func openMaintenancePlans(dir string) (*MaintenancePlanStore, error) {
	s := &MaintenancePlanStore{state: maintenancePlanState{Version: 1, Plans: []MaintenancePlan{}}}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s.path = filepath.Join(dir, "maintenance-plans.json")
	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxPlanBytes+1))
	if err != nil {
		return nil, err
	}
	var disk maintenancePlanState
	if len(b) > maxPlanBytes || json.Unmarshal(b, &disk) != nil || disk.Version != 1 || disk.Plans == nil || len(disk.Plans) > MaintenanceHistoryLimit || disk.Revision > maxPlanRevision || (len(disk.Plans) > 0 && disk.Revision == 0) {
		return nil, errors.New("invalid maintenance plans file")
	}
	s.state = disk
	ids := map[string]bool{}
	for _, p := range s.state.Plans {
		id, err := hex.DecodeString(p.ID)
		if err != nil || len(id) != 16 || p.ID != strings.ToLower(p.ID) || ids[p.ID] || !validPlanSpec(p.MaintenanceSpec) || p.CreatedAt <= 0 || p.CreatedAt > maxPlanRevision || p.StartsAt < p.CreatedAt || p.StartsAt > p.CreatedAt+maxPlanAhead || p.EndsAt != p.StartsAt+p.DurationSeconds || p.EndsAt <= p.StartsAt || p.EndsAt > maxPlanRevision || !validPlanText(p.CreatedBy, 128) {
			return nil, errors.New("invalid maintenance plan record")
		}
		if p.CanceledAt == 0 {
			if p.CanceledBy != "" || p.CancelReason != "" {
				return nil, errors.New("invalid maintenance cancellation")
			}
		} else if p.CanceledAt < p.CreatedAt || p.CanceledAt >= p.EndsAt || !validPlanText(p.CanceledBy, 128) || !validPlanText(p.CancelReason, 2048) {
			return nil, errors.New("invalid maintenance cancellation")
		}
		ids[p.ID] = true
	}
	return s, nil
}

func (s *MaintenancePlanStore) Persistent() bool { return s.path != "" }

func (s *MaintenancePlanStore) Snapshot(now int64) MaintenancePlanSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d := MaintenancePlanSnapshot{Revision: s.state.Revision, Plans: make([]MaintenancePlanView, 0, len(s.state.Plans))}
	for _, p := range s.state.Plans {
		d.Plans = append(d.Plans, MaintenancePlanView{MaintenancePlan: p, State: p.Status(now)})
	}
	// Active first, then upcoming; history by latest creation. Stable tie-breaker.
	rank := map[string]int{"active": 0, "scheduled": 1, "ended": 2, "canceled": 2}
	sort.Slice(d.Plans, func(i, j int) bool {
		a, b := d.Plans[i], d.Plans[j]
		if rank[a.State] != rank[b.State] {
			return rank[a.State] < rank[b.State]
		}
		if a.State == "scheduled" && a.StartsAt != b.StartsAt {
			return a.StartsAt < b.StartsAt
		}
		if a.CreatedAt != b.CreatedAt {
			return a.CreatedAt > b.CreatedAt
		}
		return a.ID < b.ID
	})
	return d
}

func (s *MaintenancePlanStore) Matches(chart, alarm string, now int64) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, p := range s.state.Plans {
		if p.Status(now) == "active" && (p.Scope == "all" || (p.Chart == chart && p.Alarm == alarm)) {
			return true
		}
	}
	return false
}

func (s *MaintenancePlanStore) Create(revision uint64, spec MaintenanceSpec, actor string, now int64) (MaintenancePlan, error) {
	if spec.StartsAt == 0 {
		spec.StartsAt = now
	}
	if !validPlanSpec(spec) || !validPlanText(actor, 128) || now <= 0 || spec.StartsAt < now || spec.StartsAt > now+maxPlanAhead || spec.StartsAt > maxPlanRevision-maxPlanDuration {
		return MaintenancePlan{}, ErrPlanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.state.Revision {
		return MaintenancePlan{}, ErrPlanConflict
	}
	unfinished := 0
	for _, p := range s.state.Plans {
		if p.CanceledAt == 0 && now < p.EndsAt {
			unfinished++
		}
	}
	if unfinished >= MaintenancePlanLimit {
		return MaintenancePlan{}, ErrPlanCapacity
	}
	next := append([]MaintenancePlan{}, s.state.Plans...)
	if len(next) >= MaintenanceHistoryLimit {
		oldest := -1
		for i, p := range next {
			if p.CanceledAt != 0 || now >= p.EndsAt {
				if oldest == -1 || p.CreatedAt < next[oldest].CreatedAt {
					oldest = i
				}
			}
		}
		if oldest < 0 {
			return MaintenancePlan{}, ErrPlanCapacity
		}
		next = append(next[:oldest], next[oldest+1:]...)
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return MaintenancePlan{}, err
	}
	p := MaintenancePlan{MaintenanceSpec: spec, ID: hex.EncodeToString(id[:]), EndsAt: spec.StartsAt + spec.DurationSeconds, CreatedAt: now, CreatedBy: actor}
	if err := s.saveLocked(append(next, p)); err != nil {
		return MaintenancePlan{}, err
	}
	return p, nil
}

func (s *MaintenancePlanStore) Cancel(revision uint64, id, reason, actor string, now int64) error {
	if !validPlanText(reason, 2048) || !validPlanText(actor, 128) {
		return ErrPlanInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if revision != s.state.Revision {
		return ErrPlanConflict
	}
	next := append([]MaintenancePlan{}, s.state.Plans...)
	for i := range next {
		p := &next[i]
		if p.ID != id {
			continue
		}
		if p.CanceledAt != 0 || now >= p.EndsAt {
			return ErrPlanConflict
		}
		if now < p.CreatedAt {
			return ErrPlanInvalid
		}
		p.CanceledAt, p.CanceledBy, p.CancelReason = now, actor, reason
		return s.saveLocked(next)
	}
	return ErrPlanConflict
}

func (s *MaintenancePlanStore) saveLocked(plans []MaintenancePlan) error {
	if s.state.Revision >= maxPlanRevision {
		return ErrPlanCapacity
	}
	next := maintenancePlanState{Version: 1, Revision: s.state.Revision + 1, Plans: plans}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(b) > maxPlanBytes {
		return ErrPlanCapacity
	}
	if s.path != "" {
		f, err := os.CreateTemp(filepath.Dir(s.path), ".maintenance-*") // 0600
		if err != nil {
			return err
		}
		defer os.Remove(f.Name())
		_, err = f.Write(b)
		if err == nil {
			err = f.Sync()
		}
		closeErr := f.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
		if err := os.Rename(f.Name(), s.path); err != nil {
			return fmt.Errorf("persist maintenance plans: %w", err)
		}
	}
	s.state = next
	return nil
}

func (e *Engine) MaintenancePlans() *MaintenancePlanStore { return e.plans }
