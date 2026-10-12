package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const (
	checkStatusOK       = 0
	checkStatusWarning  = 1
	checkStatusCritical = 2
	checkStatusExpired  = 3

	checkMax        = 200
	checkNameMax    = 64
	checkMessageMax = 240
	checkDefaultTTL = 5 * time.Minute
	checkMinTTL     = 10 * time.Second
	checkMaxTTL     = 24 * time.Hour
)

// ExternalCheck is one passive result pushed by an outside probe.
// Status is ok, warning, or critical. After TTL with no new report the
// collector publishes it as expired.
type ExternalCheck struct {
	Name    string    `json:"name"`
	Status  string    `json:"status"`
	Message string    `json:"message,omitempty"`
	Value   *float64  `json:"value,omitempty"`
	TTL     string    `json:"ttl"`
	Updated time.Time `json:"updated"`
	Expires time.Time `json:"expires"`
	Expired bool      `json:"expired"`
	Chart   string    `json:"chart"`
}

type storedCheck struct {
	name    string
	status  string
	message string
	value   *float64
	ttl     time.Duration
	updated time.Time
	expired bool
}

// CheckBook is the process-wide set of external service checks.
type CheckBook struct {
	mu    sync.Mutex
	path  string // empty = memory only
	items map[string]*storedCheck
}

type checkDisk struct {
	Version int                `json:"version"`
	Items   []checkDiskItem    `json:"items"`
}

type checkDiskItem struct {
	Name    string   `json:"name"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
	Value   *float64 `json:"value,omitempty"`
	TTL     string   `json:"ttl"`
	Updated int64    `json:"updated"`
	Expired bool     `json:"expired,omitempty"`
}

var serviceChecks = &CheckBook{items: map[string]*storedCheck{}}

// Checks returns the shared book used by the API and the checks collector.
func Checks() *CheckBook { return serviceChecks }

// OpenChecks loads persisted passive checks from dir/checks.json (0600).
// dir="" keeps the existing in-memory book (tests). Missing file is OK.
func OpenChecks(dir string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "checks.json")
	b := &CheckBook{path: path, items: map[string]*storedCheck{}}
	raw, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(raw) > 0 {
		var disk checkDisk
		if err := json.Unmarshal(raw, &disk); err != nil {
			return fmt.Errorf("checks.json: %w", err)
		}
		if disk.Version != 0 && disk.Version != 1 {
			return fmt.Errorf("checks.json: unsupported version %d", disk.Version)
		}
		for _, item := range disk.Items {
			ttl, err := time.ParseDuration(item.TTL)
			if err != nil || ttl < checkMinTTL || ttl > checkMaxTTL {
				continue
			}
			if !validCheckName(item.Name) {
				continue
			}
			switch item.Status {
			case "ok", "warning", "critical":
			default:
				continue
			}
			var copied *float64
			if item.Value != nil {
				v := *item.Value
				if math.IsNaN(v) || math.IsInf(v, 0) {
					continue
				}
				copied = &v
			}
			updated := time.Unix(item.Updated, 0)
			if item.Updated <= 0 {
				updated = time.Now()
			}
			b.items[item.Name] = &storedCheck{
				name: item.Name, status: item.Status, message: item.Message,
				value: copied, ttl: ttl, updated: updated, expired: item.Expired,
			}
		}
	}
	serviceChecks = b
	return nil
}

func (b *CheckBook) persistLocked() error {
	if b.path == "" {
		return nil
	}
	disk := checkDisk{Version: 1, Items: make([]checkDiskItem, 0, len(b.items))}
	for _, c := range b.items {
		disk.Items = append(disk.Items, checkDiskItem{
			Name: c.name, Status: c.status, Message: c.message, Value: c.value,
			TTL: c.ttl.String(), Updated: c.updated.Unix(), Expired: c.expired,
		})
	}
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".checks-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), b.path)
}

// Report records a check. An existing name is replaced. A new name is
// rejected once checkMax distinct checks are stored.
func (b *CheckBook) Report(name, status, message string, value *float64, ttl time.Duration, now time.Time) error {
	name = strings.TrimSpace(name)
	status = strings.ToLower(strings.TrimSpace(status))
	if !validCheckName(name) {
		return errCheckName
	}
	switch status {
	case "ok", "warning", "critical":
	default:
		return errCheckStatus
	}
	if ttl == 0 {
		ttl = checkDefaultTTL
	}
	if ttl < checkMinTTL || ttl > checkMaxTTL {
		return errCheckTTL
	}
	if value != nil {
		v := *value
		if math.IsNaN(v) || math.IsInf(v, 0) || v > 1e308 || v < -1e308 {
			return errCheckValue
		}
	}
	message = oneLine(message, checkMessageMax)
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.items[name]; !ok && len(b.items) >= checkMax {
		return errCheckFull
	}
	var copied *float64
	if value != nil {
		v := *value
		copied = &v
	}
	b.items[name] = &storedCheck{
		name: name, status: status, message: message, value: copied, ttl: ttl, updated: now,
	}
	return b.persistLocked()
}

// Remove drops one check. The chart already written stays until restart.
func (b *CheckBook) Remove(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.items, name)
	_ = b.persistLocked()
}

// List returns a snapshot, marking checks past their TTL as expired.
func (b *CheckBook) List(now time.Time) []ExternalCheck {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]ExternalCheck, 0, len(b.items))
	for _, c := range b.items {
		out = append(out, c.view(now))
	}
	return out
}

// Publish writes each check onto reg and marks those past TTL expired.
func (b *CheckBook) Publish(reg *registry.Registry, now time.Time) {
	if reg == nil {
		return
	}
	b.mu.Lock()
	snap := make([]storedCheck, 0, len(b.items))
	for _, c := range b.items {
		if !c.expired && now.Sub(c.updated) > c.ttl {
			c.expired = true
		}
		snap = append(snap, *c)
	}
	b.mu.Unlock()
	for _, c := range snap {
		id := checkChartID(c.name)
		reg.AddChart(&registry.Chart{
			ID: id, Context: "check.status", Family: "checks",
			Title: "External check " + c.name, Units: "status", Priority: 70000,
			Plugin: "checks", Module: "checks",
			Labels:     map[string]string{"check": c.name},
			Dimensions: []*registry.Dimension{{ID: "status", Name: "status"}, {ID: "value", Name: "value", Hidden: true}},
		})
		sample := map[string]float64{"status": checkStatusCode(c, now)}
		if c.value != nil {
			sample["value"] = *c.value
		}
		_ = reg.Collect(id, now, sample)
	}
}

func (c storedCheck) view(now time.Time) ExternalCheck {
	expired := c.expired || now.Sub(c.updated) > c.ttl
	status := c.status
	if expired {
		status = "expired"
	}
	return ExternalCheck{
		Name: c.name, Status: status, Message: c.message, Value: c.value,
		TTL: c.ttl.String(), Updated: c.updated, Expires: c.updated.Add(c.ttl),
		Expired: expired, Chart: checkChartID(c.name),
	}
}

func checkStatusCode(c storedCheck, now time.Time) float64 {
	if c.expired || now.Sub(c.updated) > c.ttl {
		return checkStatusExpired
	}
	switch c.status {
	case "warning":
		return checkStatusWarning
	case "critical":
		return checkStatusCritical
	default:
		return checkStatusOK
	}
}

func checkChartID(name string) string { return "check." + name }

func validCheckName(name string) bool {
	if name == "" || len(name) > checkNameMax {
		return false
	}
	for i, r := range name {
		ok := r == '_' || r == '-' || r == '.' || (r >= '0' && r <= '9') || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		if !ok || (i == 0 && (r == '.' || r == '-')) {
			return false
		}
	}
	return true
}

func oneLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(s, "\n", " "), "\r", " "))
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	runes := []rune(s)
	return string(runes[:max])
}

type checkError string

func (e checkError) Error() string { return string(e) }

const (
	errCheckName   = checkError("check name must be 1..64 letters, digits, '_', '-' or '.'")
	errCheckStatus = checkError("check status must be ok, warning or critical")
	errCheckTTL    = checkError("check ttl must be from 10s to 24h")
	errCheckValue  = checkError("check value must be a finite number")
	errCheckFull   = checkError("too many external checks")
)

type checksCollector struct{}

func init() { Register("checks", func() Collector { return &checksCollector{} }) }

func (c *checksCollector) Name() string { return "checks" }

func (c *checksCollector) Init(*registry.Registry) error { return nil }

func (c *checksCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	Checks().Publish(reg, now)
	return nil
}

func (c *checksCollector) Functions() []Function {
	return []Function{{
		Name:    "checks",
		Help:    "External service checks pushed through POST /api/v1/checks",
		Timeout: 5,
		Run: func(_ context.Context, _ map[string]string) (any, error) {
			rows := Checks().List(time.Now())
			out := make([]any, len(rows))
			for i := range rows {
				out[i] = rows[i]
			}
			return Table{Columns: []string{"name", "status", "message", "value", "ttl", "updated", "expires", "chart"}, Rows: out, Total: len(out)}, nil
		},
	}}
}
