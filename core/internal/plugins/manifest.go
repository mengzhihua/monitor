package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

const ProtocolV1 = "plugins.d/v1"

// Manifest is the versioned local package contract. Execution settings are
// decoded from disk but never serialized into a status/API response.
type Manifest struct {
	APIVersion   int               `json:"api_version"`
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Version      string            `json:"version"`
	Description  string            `json:"description"`
	Protocol     string            `json:"protocol"`
	Capabilities []string          `json:"capabilities"`
	Command      string            `json:"command"`
	Args         []string          `json:"args,omitempty"`
	UpdateEvery  int               `json:"update_every,omitempty"`
	Timeout      int               `json:"timeout,omitempty"`
	Resources    *PackageResources `json:"resources,omitempty"`
}

// PackageResources is the optional ceiling a package declares for itself.
// Zero fields inherit the process budget. A package cannot raise that budget.
type PackageResources struct {
	MaxCharts     int `json:"max_charts,omitempty"`
	MaxDimensions int `json:"max_dimensions,omitempty"`
	MaxLabels     int `json:"max_labels,omitempty"`
	MaxVariables  int `json:"max_variables,omitempty"`
	MaxLine       int `json:"max_line,omitempty"`
}

func (m Manifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		APIVersion   int               `json:"api_version"`
		ID           string            `json:"id"`
		Name         string            `json:"name"`
		Version      string            `json:"version"`
		Description  string            `json:"description"`
		Protocol     string            `json:"protocol"`
		Capabilities []string          `json:"capabilities"`
		Resources    *PackageResources `json:"resources,omitempty"`
	}{m.APIVersion, m.ID, m.Name, m.Version, m.Description, m.Protocol, m.Capabilities, m.Resources})
}

type Diagnostic struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

var packageID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// DiscoverManifests reads <dir>/<id>/plugin.json, without executing anything.
// An invalid package is isolated and reported; other packages remain usable.
func DiscoverManifests(dir string) ([]Spec, []Diagnostic) {
	specs, diagnostics, _ := discoverManifests(context.Background(), dir)
	return specs, diagnostics
}

func discoverManifests(ctx context.Context, dir string) ([]Spec, []Diagnostic, error) {
	entries, err := readPluginDirectory(ctx, dir)
	if err != nil {
		return nil, directoryDiagnostics(err), err
	}
	return discoverManifestEntries(ctx, dir, entries)
}

func discoverManifestEntries(ctx context.Context, dir string, entries []os.DirEntry) ([]Spec, []Diagnostic, error) {
	var specs []Spec
	diagnostics := []Diagnostic{}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		// Do not traverse package symlinks. Report them rather than silently
		// treating an outside directory as an installed package.
		if entry.Type()&os.ModeSymlink != 0 {
			diagnostics = append(diagnostics, Diagnostic{Path: entry.Name(), Error: "package symlinks are not supported"})
			continue
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(dir, entry.Name(), "plugin.json")
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		}
		manifest, command, err := readManifest(ctx, path, entry.Name())
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: filepath.ToSlash(filepath.Join(entry.Name(), "plugin.json")), Error: err.Error()})
			continue
		}
		specs = append(specs, Spec{Name: manifest.ID, Command: command, Args: append([]string(nil), manifest.Args...),
			UpdateEvery: manifest.UpdateEvery, Timeout: manifest.Timeout, Source: "manifest", Manifest: manifest,
			MaxCharts:     resourceField(manifest.Resources, func(r *PackageResources) int { return r.MaxCharts }),
			MaxDimensions: resourceField(manifest.Resources, func(r *PackageResources) int { return r.MaxDimensions }),
			MaxLabels:     resourceField(manifest.Resources, func(r *PackageResources) int { return r.MaxLabels }),
			MaxVariables:  resourceField(manifest.Resources, func(r *PackageResources) int { return r.MaxVariables }),
			MaxLine:       resourceField(manifest.Resources, func(r *PackageResources) int { return r.MaxLine }),
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return specs, diagnostics, nil
}

func readManifest(ctx context.Context, path, id string) (*Manifest, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, "", errors.New("manifest must be a regular file of at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", errors.New("cannot read manifest")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(discoveryReader{ctx: ctx, reader: f}, (64<<10)+1))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return nil, "", errors.New("invalid manifest JSON or unknown fields")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, "", errors.New("manifest must contain one JSON object")
	}
	if manifest.APIVersion != 1 || manifest.Protocol != ProtocolV1 {
		return nil, "", errors.New("unsupported manifest API or plugin protocol")
	}
	if !packageID.MatchString(manifest.ID) || manifest.ID != id {
		return nil, "", errors.New("manifest id must match its lowercase package directory")
	}
	if err := validateCapabilities(manifest.Capabilities); err != nil {
		return nil, "", err
	}
	if strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 120 || strings.TrimSpace(manifest.Version) == "" || len(manifest.Version) > 64 || len(manifest.Description) > 2000 {
		return nil, "", errors.New("invalid package name, version or description length")
	}
	if manifest.UpdateEvery < 0 || manifest.UpdateEvery > 86400 || manifest.Timeout < 0 || manifest.Timeout > 86400 {
		return nil, "", errors.New("update_every and timeout must be between 0 and 86400 seconds")
	}
	if err := manifest.Resources.validate(); err != nil {
		return nil, "", err
	}
	if len(manifest.Args) > 64 {
		return nil, "", errors.New("too many plugin arguments")
	}
	for _, arg := range manifest.Args {
		if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
			return nil, "", errors.New("invalid plugin argument")
		}
	}
	command, err := packageCommand(filepath.Dir(path), manifest.Command)
	if err != nil {
		return nil, "", err
	}
	return &manifest, command, nil
}

// Package executables must stay inside the package, including every directory
// component. Local administrators still own these files; this is not a sandbox.
func packageCommand(dir, command string) (string, error) {
	if command == "" || strings.ContainsAny(command, "\\\x00") || !filepath.IsLocal(command) {
		return "", errors.New("command must be a relative file inside the package")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return "", errors.New("cannot resolve package directory")
	}
	path := root
	parts := strings.Split(filepath.ToSlash(filepath.Clean(command)), "/")
	for i, part := range parts {
		path = filepath.Join(path, part)
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("command is missing or crosses a symlink")
		}
		if i < len(parts)-1 && !info.IsDir() {
			return "", errors.New("command parent is not a directory")
		}
		if i == len(parts)-1 && (!info.Mode().IsRegular() || runtime.GOOS != "windows" && info.Mode()&0o111 == 0) {
			return "", errors.New("command must be an executable regular file")
		}
	}
	return path, nil
}

func validateCapabilities(capabilities []string) error {
	if len(capabilities) == 0 || len(capabilities) > 2 {
		return errors.New("v1 packages must declare metrics, functions, or both")
	}
	seen := map[string]bool{}
	for _, capability := range capabilities {
		if capability != "metrics" && capability != "functions" || seen[capability] {
			return errors.New("v1 packages must declare metrics, functions, or both")
		}
		seen[capability] = true
	}
	return nil
}

func (m *Manifest) allowsFunctions() bool {
	if m == nil {
		return false
	}
	for _, capability := range m.Capabilities {
		if capability == "functions" {
			return true
		}
	}
	return false
}

func resourceField(resources *PackageResources, pick func(*PackageResources) int) int {
	if resources == nil {
		return 0
	}
	return pick(resources)
}

func (r *PackageResources) validate() error {
	if r == nil {
		return nil
	}
	checks := []struct {
		name    string
		value   int
		minimum int
		maximum int
	}{
		{"resources.max_charts", r.MaxCharts, 1, 10000},
		{"resources.max_dimensions", r.MaxDimensions, 1, 10000},
		{"resources.max_labels", r.MaxLabels, 1, 10000},
		{"resources.max_variables", r.MaxVariables, 1, 10000},
		{"resources.max_line", r.MaxLine, 1024, 1 << 20},
	}
	for _, check := range checks {
		if check.value == 0 {
			continue
		}
		if check.value < check.minimum || check.value > check.maximum {
			return fmt.Errorf("%s must be 0 or %d..%d", check.name, check.minimum, check.maximum)
		}
	}
	return nil
}

func newPluginParser(spec Spec, reg *registry.Registry, budgets PackageBudgets) *Parser {
	enforced, limited := enforcedBudgets(spec, budgets)
	p := &Parser{Plugin: spec.Name, Reg: reg}
	if spec.Manifest != nil {
		p.Namespace = fmt.Sprintf("plugin.%s.", spec.Manifest.ID)
		p.MaxCharts = enforced.MaxCharts
		p.MaxDimensions = enforced.MaxDimensions
		p.MaxLabels = enforced.MaxLabels
		p.MaxVariables = enforced.MaxVariables
		p.MaxLine = enforced.MaxLine
		return p
	}
	if limited {
		p.MaxLine = enforced.MaxLine
		p.MaxCharts = enforced.MaxCharts
		p.MaxDimensions = enforced.MaxDimensions
		p.MaxLabels = enforced.MaxLabels
		p.MaxVariables = enforced.MaxVariables
	} else {
		p.MaxLine = budgets.Effective().MaxLine
	}
	return p
}
