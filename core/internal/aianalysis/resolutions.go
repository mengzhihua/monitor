package aianalysis

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const ResolutionLimit = 10
const resolutionCapacity = 256
const resolutionFileLimit = 16 << 20

var ErrNotFound = errors.New("AI resolution not found")
var ErrCapacity = errors.New("AI resolution storage full")

type StepProgress struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Note   string `json:"note"`
}
type Resolution struct {
	ID        string          `json:"id"`
	Node      string          `json:"node"`
	Hostname  string          `json:"hostname"`
	CreatedAt int64           `json:"created_at"`
	UpdatedAt int64           `json:"updated_at"`
	Revision  uint64          `json:"revision"`
	Model     string          `json:"model"`
	Question  string          `json:"question"`
	Plan      Plan            `json:"plan"`
	Baseline  json.RawMessage `json:"baseline"`
	Progress  []StepProgress  `json:"progress"`
	Review    json.RawMessage `json:"review,omitempty"`
}
type ResolutionSummary struct {
	ID        string `json:"id"`
	Node      string `json:"node"`
	Hostname  string `json:"hostname"`
	Summary   string `json:"summary"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
	Done      int    `json:"done"`
	Total     int    `json:"total"`
}
type resolutionDisk struct {
	Version int                              `json:"version"`
	Owners  map[string]map[string]Resolution `json:"owners"`
}
type ResolutionStore struct {
	mu          sync.Mutex
	path        string
	disk        resolutionDisk
	unavailable bool
	syncDir     func(string) error
}

func OpenResolutions(dir string) (*ResolutionStore, error) {
	s := &ResolutionStore{disk: resolutionDisk{1, map[string]map[string]Resolution{}}, syncDir: syncDirectory}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, ErrUnavailable
	}
	s.path = filepath.Join(dir, "ai-resolutions.json")
	info, err := os.Lstat(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > resolutionFileLimit {
		return nil, ErrUnavailable
	}
	f, err := os.Open(s.path)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) || f.Chmod(0600) != nil {
		return nil, ErrUnavailable
	}
	var disk resolutionDisk
	data, err := io.ReadAll(io.LimitReader(f, resolutionFileLimit+1))
	if err != nil || len(data) > resolutionFileLimit || json.Unmarshal(data, &disk) != nil || disk.Version != 1 || disk.Owners == nil || len(disk.Owners) > resolutionCapacity {
		return nil, ErrUnavailable
	}
	count := 0
	for owner, records := range disk.Owners {
		if !validOwner(owner) || records == nil || len(records) > ResolutionLimit {
			return nil, ErrUnavailable
		}
		for id, record := range records {
			if id != record.ID || !validResolution(record) {
				return nil, ErrUnavailable
			}
			count++
		}
	}
	if count > resolutionCapacity {
		return nil, ErrUnavailable
	}
	s.disk = disk
	return s, nil
}

// Persistent exposes storage mode, never the private identity namespace.
func (s *ResolutionStore) Persistent() bool { return s.path != "" }
func cloneResolution(r Resolution) Resolution {
	data, _ := json.Marshal(r)
	var copy Resolution
	_ = json.Unmarshal(data, &copy)
	return copy
}
func validResolution(r Resolution) bool {
	if len(r.ID) != 48 || r.Revision == 0 || r.Revision > maxRevision || r.CreatedAt <= 0 || r.UpdatedAt < r.CreatedAt ||
		!planText(r.Node, 256, false) || !planText(r.Hostname, 512, false) || !planText(r.Model, 256, true) || !planText(r.Question, 8000, false) ||
		!ValidPlan(r.Plan) || len(r.Baseline) > 64<<10 || !json.Valid(r.Baseline) || (len(r.Review) > 128<<10) || (len(r.Review) > 0 && !json.Valid(r.Review)) || len(r.Progress) != len(r.Plan.Steps) {
		return false
	}
	if _, err := hex.DecodeString(r.ID); err != nil {
		return false
	}
	var snapshot struct {
		Node   string            `json:"node"`
		HostID string            `json:"host_id"`
		After  int64             `json:"after"`
		Before int64             `json:"before"`
		Charts []json.RawMessage `json:"charts"`
	}
	if json.Unmarshal(r.Baseline, &snapshot) != nil || snapshot.Node != r.Node || snapshot.HostID == "" || snapshot.Before <= snapshot.After || len(snapshot.Charts) == 0 || len(snapshot.Charts) > 6 {
		return false
	}
	for i, p := range r.Progress {
		if p.ID != r.Plan.Steps[i].ID || (p.Status != "todo" && p.Status != "done" && p.Status != "skipped") || !planText(p.Note, 2000, false) {
			return false
		}
	}
	return true
}

func (s *ResolutionStore) CanCreate(owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return ErrUnavailable
	}
	if !validOwner(owner) {
		return ErrInvalid
	}
	count := 0
	for _, records := range s.disk.Owners {
		count += len(records)
	}
	if len(s.disk.Owners[owner]) >= ResolutionLimit || count >= resolutionCapacity {
		return ErrCapacity
	}
	return nil
}
func (s *ResolutionStore) List(owner string) ([]ResolutionSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return nil, ErrUnavailable
	}
	out := []ResolutionSummary{}
	for _, r := range s.disk.Owners[owner] {
		done := 0
		for _, p := range r.Progress {
			if p.Status == "done" {
				done++
			}
		}
		out = append(out, ResolutionSummary{r.ID, r.Node, r.Hostname, r.Plan.Summary, r.CreatedAt, r.UpdatedAt, done, len(r.Progress)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}
func (s *ResolutionStore) Get(owner, id string) (Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return Resolution{}, ErrUnavailable
	}
	r, ok := s.disk.Owners[owner][id]
	if !ok {
		return Resolution{}, ErrNotFound
	}
	return cloneResolution(r), nil
}
func (s *ResolutionStore) Create(owner string, r Resolution) (Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return Resolution{}, ErrUnavailable
	}
	if !validOwner(owner) {
		return Resolution{}, ErrInvalid
	}
	count := 0
	for _, records := range s.disk.Owners {
		count += len(records)
	}
	if len(s.disk.Owners[owner]) >= ResolutionLimit || count >= resolutionCapacity {
		return Resolution{}, ErrCapacity
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return Resolution{}, ErrUnavailable
	}
	r.ID, r.Revision = hex.EncodeToString(nonce[:]), 1
	r.CreatedAt, r.UpdatedAt = time.Now().Unix(), time.Now().Unix()
	r.Progress = []StepProgress{}
	for _, step := range r.Plan.Steps {
		r.Progress = append(r.Progress, StepProgress{ID: step.ID, Status: "todo"})
	}
	if !validResolution(r) {
		return Resolution{}, ErrInvalid
	}
	if err := s.commitLocked(owner, r.ID, &r); err != nil {
		return Resolution{}, err
	}
	return cloneResolution(r), nil
}
func (s *ResolutionStore) Update(owner, id string, revision uint64, stepID, status, note string, review json.RawMessage) (Resolution, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return Resolution{}, ErrUnavailable
	}
	r, ok := s.disk.Owners[owner][id]
	if !ok {
		return Resolution{}, ErrNotFound
	}
	if r.Revision != revision {
		return Resolution{}, ErrConflict
	}
	if revision >= maxRevision {
		return Resolution{}, ErrUnavailable
	}
	r = cloneResolution(r)
	if stepID != "" {
		found := false
		for i := range r.Progress {
			if r.Progress[i].ID == stepID {
				r.Progress[i].Status, r.Progress[i].Note = status, note
				found = true
			}
		}
		if !found || len(review) != 0 {
			return Resolution{}, ErrInvalid
		}
	} else if len(review) > 0 {
		r.Review = append(json.RawMessage{}, review...)
	} else {
		return Resolution{}, ErrInvalid
	}
	r.Revision++
	r.UpdatedAt = max(r.CreatedAt, time.Now().Unix())
	if !validResolution(r) {
		return Resolution{}, ErrInvalid
	}
	if err := s.commitLocked(owner, id, &r); err != nil {
		return Resolution{}, err
	}
	return cloneResolution(r), nil
}
func (s *ResolutionStore) Delete(owner, id string, revision uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unavailable {
		return ErrUnavailable
	}
	r, ok := s.disk.Owners[owner][id]
	if !ok {
		return ErrNotFound
	}
	if r.Revision != revision {
		return ErrConflict
	}
	return s.commitLocked(owner, id, nil)
}
func (s *ResolutionStore) commitLocked(owner, id string, record *Resolution) error {
	if s.unavailable {
		return ErrUnavailable
	}
	next := resolutionDisk{1, make(map[string]map[string]Resolution, len(s.disk.Owners)+1)}
	for key, records := range s.disk.Owners {
		next.Owners[key] = records
	}
	copy := map[string]Resolution{}
	for key, r := range next.Owners[owner] {
		copy[key] = r
	}
	if record == nil {
		delete(copy, id)
	} else {
		copy[id] = cloneResolution(*record)
	}
	if len(copy) == 0 {
		delete(next.Owners, owner)
	} else {
		next.Owners[owner] = copy
	}
	data, err := json.Marshal(next)
	if err != nil || len(data) > resolutionFileLimit {
		return ErrCapacity
	}
	if s.path != "" {
		committed, err := atomicWrite(s.path, data, s.syncDir)
		if err != nil {
			if committed {
				s.unavailable = true
			}
			return ErrUnavailable
		}
	}
	s.disk = next
	return nil
}
