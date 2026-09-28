package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/health"
	"github.com/mengzhihua/monitor/core/internal/stream"
)

// Template is a Zabbix-style linkable bundle: alert rules, {$NAME} macros,
// rule tombstones, collector disables and inventory tags, applied to every
// node matched by Assign.
type Template struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Macros      map[string]string `json:"macros,omitempty"`
	Rules       []health.RuleSpec `json:"rules,omitempty"`
	Removed     []string          `json:"removed,omitempty"`  // base rule names to disable
	Disabled    []string          `json:"disabled,omitempty"` // collectors to disable
	Tags        map[string]string `json:"tags,omitempty"`     // inventory tags stamped on matched nodes
	Assign      TemplateAssign    `json:"assign"`
	Updated     int64             `json:"updated"`
}

// TemplateAssign selects target nodes: a node matches when it sits in any of
// Rooms, is listed in Nodes, or carries every label in Labels (AND within
// Labels). An empty Assign matches nothing.
type TemplateAssign struct {
	Rooms  []string          `json:"rooms,omitempty"`
	Nodes  []string          `json:"nodes,omitempty"`
	Labels map[string]string `json:"labels,omitempty"`
}

// NodeInventory holds manual Zabbix-like inventory fields for one node
// (location, owner, serial, contact, ...).
type NodeInventory struct {
	NodeID  string            `json:"node_id"`
	Fields  map[string]string `json:"fields,omitempty"`
	Updated int64             `json:"updated"`
}

// ErrTemplateConflict means the template changed since the caller read it.
var ErrTemplateConflict = errors.New("template changed since read")

// ErrTemplateNotFound means the template id is unknown.
var ErrTemplateNotFound = errors.New("unknown template")

// ErrTemplateInvalid means the template failed validation.
var ErrTemplateInvalid = errors.New("invalid template")

// Templates persists hub templates and node inventory next to org.json.
type Templates struct {
	path string
	now  func() time.Time

	mu        sync.Mutex
	templates map[string]*Template
	inventory map[string]*NodeInventory
}

type templatesFile struct {
	Templates []*Template               `json:"templates"`
	Inventory map[string]*NodeInventory `json:"inventory,omitempty"`
}

// OpenTemplates loads templates.json from dir (created on first save).
func OpenTemplates(dir string) (*Templates, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	t := &Templates{path: filepath.Join(dir, "templates.json"), now: time.Now,
		templates: map[string]*Template{}, inventory: map[string]*NodeInventory{}}
	b, err := os.ReadFile(t.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return t, nil
		}
		return nil, err
	}
	var f templatesFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", t.path, err)
	}
	for _, tpl := range f.Templates {
		t.templates[tpl.ID] = tpl
	}
	if f.Inventory != nil {
		t.inventory = f.Inventory
	}
	return t, nil
}

func (t *Templates) saveLocked() error {
	f := templatesFile{Inventory: t.inventory}
	for _, tpl := range t.templates {
		f.Templates = append(f.Templates, tpl)
	}
	sort.Slice(f.Templates, func(i, j int) bool { return f.Templates[i].Name < f.Templates[j].Name })
	b, err := json.MarshalIndent(f, "", " ")
	if err != nil {
		return err
	}
	return writeOrgFile(t.path, b)
}

func cloneTemplate(t *Template) *Template {
	cp := *t
	cp.Macros = cloneMap(t.Macros)
	cp.Tags = cloneMap(t.Tags)
	cp.Rules = append([]health.RuleSpec(nil), t.Rules...)
	cp.Removed = append([]string(nil), t.Removed...)
	cp.Disabled = append([]string(nil), t.Disabled...)
	cp.Assign.Rooms = append([]string(nil), t.Assign.Rooms...)
	cp.Assign.Nodes = append([]string(nil), t.Assign.Nodes...)
	cp.Assign.Labels = cloneMap(t.Assign.Labels)
	return &cp
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// List returns all templates sorted by name.
func (t *Templates) List() []Template {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]Template, 0, len(t.templates))
	for _, tpl := range t.templates {
		out = append(out, *cloneTemplate(tpl))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns one template by id.
func (t *Templates) Get(id string) (Template, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tpl, ok := t.templates[id]
	if !ok {
		return Template{}, false
	}
	return *cloneTemplate(tpl), true
}

// Upsert creates or replaces a template under a CAS revision check: a nil
// expected always writes; a pointer requires the stored Updated to equal it
// (0 means "must not exist yet"). Returns the stored template with its id and
// new revision.
func (t *Templates) Upsert(in Template, expected *int64) (Template, error) {
	if in.Name == "" {
		return Template{}, fmt.Errorf("%w: name required", ErrTemplateInvalid)
	}
	for _, spec := range in.Rules {
		if _, err := health.CompileWith(spec, "template:"+in.Name, in.Macros); err != nil {
			return Template{}, fmt.Errorf("%w: rule %q: %v", ErrTemplateInvalid, spec.Name, err)
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.templates[in.ID]
	if expected != nil {
		var cur int64
		if previous != nil {
			cur = previous.Updated
		}
		if *expected != cur {
			return Template{}, ErrTemplateConflict
		}
	}
	cp := *cloneTemplate(&in)
	if cp.ID == "" {
		cp.ID = randomID("tpl")
	}
	rev := t.now().Unix()
	if previous != nil && rev <= previous.Updated {
		rev = previous.Updated + 1
	}
	cp.Updated = rev
	t.templates[cp.ID] = &cp
	if err := t.saveLocked(); err != nil {
		if previous == nil {
			delete(t.templates, cp.ID)
		} else {
			t.templates[cp.ID] = previous
		}
		return Template{}, err
	}
	return *cloneTemplate(&cp), nil
}

// Delete removes a template.
func (t *Templates) Delete(id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.templates[id]; !ok {
		return ErrTemplateNotFound
	}
	delete(t.templates, id)
	return t.saveLocked()
}

// Inventory returns the manual inventory fields for a node.
func (t *Templates) Inventory(nodeID string) (NodeInventory, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	inv, ok := t.inventory[nodeID]
	if !ok {
		return NodeInventory{NodeID: nodeID}, false
	}
	cp := *inv
	cp.Fields = cloneMap(inv.Fields)
	return cp, true
}

// SetInventory stores manual fields under the same CAS convention as Upsert.
func (t *Templates) SetInventory(nodeID string, fields map[string]string, expected *int64) (NodeInventory, error) {
	if nodeID == "" {
		return NodeInventory{}, errors.New("node_id required")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := t.inventory[nodeID]
	var cur int64
	if previous != nil {
		cur = previous.Updated
	}
	if expected != nil && *expected != cur {
		return NodeInventory{}, ErrTemplateConflict
	}
	inv := NodeInventory{NodeID: nodeID, Fields: cloneMap(fields)}
	rev := t.now().Unix()
	if rev <= cur {
		rev = cur + 1
	}
	inv.Updated = rev
	t.inventory[nodeID] = &inv
	if err := t.saveLocked(); err != nil {
		if previous == nil {
			delete(t.inventory, nodeID)
		} else {
			t.inventory[nodeID] = previous
		}
		return NodeInventory{}, err
	}
	out := inv
	out.Fields = cloneMap(inv.Fields)
	return out, nil
}

func (t *Templates) matches(tpl *Template, nodeID, roomID string, labels map[string]string) bool {
	for _, id := range tpl.Assign.Nodes {
		if id == nodeID {
			return true
		}
	}
	if roomID != "" {
		for _, id := range tpl.Assign.Rooms {
			if id == roomID {
				return true
			}
		}
	}
	if len(tpl.Assign.Labels) > 0 {
		for k, v := range tpl.Assign.Labels {
			if labels[k] != v {
				return false
			}
		}
		return true
	}
	return false
}

// Effective resolves every template matching the node and merges them: later
// (by name) templates win for the same macro / rule name; Removed and
// Disabled are unions; Rev is the max Updated over matched templates (0 when
// none match).
func (t *Templates) Effective(nodeID, roomID string, labels map[string]string) stream.HealthOverlay {
	t.mu.Lock()
	defer t.mu.Unlock()
	var matched []*Template
	for _, tpl := range t.templates {
		if t.matches(tpl, nodeID, roomID, labels) {
			matched = append(matched, tpl)
		}
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	out := stream.HealthOverlay{}
	if len(matched) == 0 {
		return out
	}
	ruleIx := map[string]int{}
	macros := map[string]string{}
	tags := map[string]string{}
	removed := map[string]bool{}
	disabled := map[string]bool{}
	ruleSource := map[string]string{}
	for _, tpl := range matched {
		if tpl.Updated > out.Rev {
			out.Rev = tpl.Updated
		}
		out.Templates = append(out.Templates, tpl.Name)
		for k, v := range tpl.Macros {
			macros[k] = v
		}
		for k, v := range tpl.Tags {
			tags[k] = v
		}
		for _, spec := range tpl.Rules {
			if i, ok := ruleIx[spec.Name]; ok {
				out.Rules[i] = spec
			} else {
				ruleIx[spec.Name] = len(out.Rules)
				out.Rules = append(out.Rules, spec)
			}
			ruleSource[spec.Name] = tpl.Name
		}
		for _, n := range tpl.Removed {
			removed[n] = true
		}
		for _, n := range tpl.Disabled {
			disabled[n] = true
		}
	}
	out.Macros = macros
	out.Tags = tags
	out.RuleSource = ruleSource
	for n := range removed {
		out.Removed = append(out.Removed, n)
	}
	for n := range disabled {
		out.Disabled = append(out.Disabled, n)
	}
	sort.Strings(out.Removed)
	sort.Strings(out.Disabled)
	return out
}
