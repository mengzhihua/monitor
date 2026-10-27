package hub

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/mengzhihua/monitor/core/internal/health"
	"gopkg.in/yaml.v3"
)

const templateBundleVersion = 1
const templateBundleLimit = 64

// TemplateBundle is a portable set of templates. Updated revisions are omitted
// so another hub can import the file. Identity on import is the template name.
type TemplateBundle struct {
	Version   int        `json:"version" yaml:"version"`
	Templates []Template `json:"templates" yaml:"templates"`
}

// Export returns templates by id, or every template when ids is empty.
// Revisions are cleared. The id is kept so a later update on the same hub
// can see it, but import matches by name.
func (t *Templates) Export(ids []string) (TemplateBundle, error) {
	if t == nil {
		return TemplateBundle{}, fmt.Errorf("%w: templates not enabled", ErrTemplateInvalid)
	}
	want := map[string]bool{}
	for _, id := range ids {
		if id != "" {
			want[id] = true
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := TemplateBundle{Version: templateBundleVersion}
	for _, tpl := range t.templates {
		if len(want) > 0 && !want[tpl.ID] {
			continue
		}
		cp := *cloneTemplate(tpl)
		cp.Updated = 0
		out.Templates = append(out.Templates, cp)
	}
	if len(want) > 0 && len(out.Templates) != len(want) {
		return TemplateBundle{}, ErrTemplateNotFound
	}
	return out, nil
}

// EncodeBundle writes the bundle as JSON or YAML.
func EncodeBundle(bundle TemplateBundle, format string) ([]byte, error) {
	switch format {
	case "", "json":
		return json.MarshalIndent(bundle, "", "  ")
	case "yaml", "yml":
		raw, err := json.Marshal(bundle)
		if err != nil {
			return nil, err
		}
		var generic any
		if err := json.Unmarshal(raw, &generic); err != nil {
			return nil, err
		}
		return yaml.Marshal(generic)
	default:
		return nil, fmt.Errorf("%w: format must be json or yaml", ErrTemplateInvalid)
	}
}

// DecodeBundle accepts a JSON object or a YAML document.
func DecodeBundle(raw []byte) (TemplateBundle, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return TemplateBundle{}, fmt.Errorf("%w: empty bundle", ErrTemplateInvalid)
	}
	var bundle TemplateBundle
	if raw[0] == '{' {
		if err := json.Unmarshal(raw, &bundle); err != nil {
			return TemplateBundle{}, fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
		}
	} else {
		var generic any
		if err := yaml.Unmarshal(raw, &generic); err != nil {
			return TemplateBundle{}, fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
		}
		encoded, err := json.Marshal(generic)
		if err != nil {
			return TemplateBundle{}, err
		}
		if err := json.Unmarshal(encoded, &bundle); err != nil {
			return TemplateBundle{}, fmt.Errorf("%w: %v", ErrTemplateInvalid, err)
		}
	}
	if bundle.Version != templateBundleVersion {
		return TemplateBundle{}, fmt.Errorf("%w: bundle version %d", ErrTemplateInvalid, bundle.Version)
	}
	return bundle, nil
}

// Import writes every template in the bundle or none of them.
// mode "create" refuses an existing name. mode "update" overwrites by name
// and creates the rest. Incoming ids are kept only when they are unused.
func (t *Templates) Import(bundle TemplateBundle, mode string) ([]Template, error) {
	if t == nil {
		return nil, fmt.Errorf("%w: templates not enabled", ErrTemplateInvalid)
	}
	switch mode {
	case "", "create":
		mode = "create"
	case "update":
	default:
		return nil, fmt.Errorf("%w: mode must be create or update", ErrTemplateInvalid)
	}
	if len(bundle.Templates) == 0 || len(bundle.Templates) > templateBundleLimit {
		return nil, fmt.Errorf("%w: bundle needs 1 to %d templates", ErrTemplateInvalid, templateBundleLimit)
	}
	names := map[string]bool{}
	for _, tpl := range bundle.Templates {
		if tpl.Name == "" || names[tpl.Name] {
			return nil, fmt.Errorf("%w: template name %q", ErrTemplateInvalid, tpl.Name)
		}
		names[tpl.Name] = true
		if err := validateTemplateLimits(tpl); err != nil {
			return nil, err
		}
		for _, spec := range tpl.Rules {
			if _, err := health.CompileWith(spec, "template:"+tpl.Name, tpl.Macros); err != nil {
				return nil, fmt.Errorf("%w: rule %q: %v", ErrTemplateInvalid, spec.Name, err)
			}
		}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	previous := map[string]*Template{}
	for id, tpl := range t.templates {
		previous[id] = tpl
	}
	var stored []Template
	for _, incoming := range bundle.Templates {
		id, existing, err := t.resolveImportLocked(incoming)
		if err != nil {
			t.templates = previous
			return nil, err
		}
		if existing != nil && mode == "create" {
			t.templates = previous
			return nil, fmt.Errorf("%w: %s", ErrTemplateExists, incoming.Name)
		}
		if existing == nil && len(t.templates) >= TemplateLimit {
			t.templates = previous
			return nil, fmt.Errorf("%w: template limit %d reached", ErrTemplateInvalid, TemplateLimit)
		}
		cp := *cloneTemplate(&incoming)
		cp.ID = id
		cp.Updated = 0
		rev := t.now().Unix()
		if existing != nil && rev <= existing.Updated {
			rev = existing.Updated + 1
		}
		cp.Updated = rev
		t.templates[id] = &cp
		stored = append(stored, *cloneTemplate(&cp))
	}
	if err := t.saveLocked(); err != nil {
		t.templates = previous
		return nil, err
	}
	return stored, nil
}

// ErrTemplateExists means create mode found a template with the same name.
var ErrTemplateExists = fmt.Errorf("%w: template name already exists", ErrTemplateInvalid)

func (t *Templates) resolveImportLocked(in Template) (string, *Template, error) {
	var byName *Template
	for _, existing := range t.templates {
		if existing.Name == in.Name {
			byName = existing
		}
	}
	if byName != nil {
		return byName.ID, byName, nil
	}
	if in.ID != "" {
		if existing, ok := t.templates[in.ID]; ok {
			return "", nil, fmt.Errorf("%w: id %s belongs to %s", ErrTemplateInvalid, in.ID, existing.Name)
		}
		return in.ID, nil, nil
	}
	return randomID("tpl"), nil, nil
}
