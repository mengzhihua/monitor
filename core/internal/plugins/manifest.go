package plugins

import (
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
	APIVersion   int      `json:"api_version"`
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	Description  string   `json:"description"`
	Protocol     string   `json:"protocol"`
	Capabilities []string `json:"capabilities"`
	Command      string   `json:"command"`
	Args         []string `json:"args,omitempty"`
	UpdateEvery  int      `json:"update_every,omitempty"`
	Timeout      int      `json:"timeout,omitempty"`
}

func (m Manifest) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		APIVersion   int      `json:"api_version"`
		ID           string   `json:"id"`
		Name         string   `json:"name"`
		Version      string   `json:"version"`
		Description  string   `json:"description"`
		Protocol     string   `json:"protocol"`
		Capabilities []string `json:"capabilities"`
	}{m.APIVersion, m.ID, m.Name, m.Version, m.Description, m.Protocol, m.Capabilities})
}

type Diagnostic struct {
	Path  string `json:"path"`
	Error string `json:"error"`
}

var packageID = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)

// DiscoverManifests reads <dir>/<id>/plugin.json, without executing anything.
// An invalid package is isolated and reported; other packages remain usable.
func DiscoverManifests(dir string) ([]Spec, []Diagnostic) {
	var specs []Spec
	diagnostics := []Diagnostic{}
	if dir == "" {
		return specs, diagnostics
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			diagnostics = append(diagnostics, Diagnostic{Path: ".", Error: "cannot read plugin directory"})
		}
		return specs, diagnostics
	}
	for _, entry := range entries {
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
		manifest, command, err := readManifest(path, entry.Name())
		if err != nil {
			diagnostics = append(diagnostics, Diagnostic{Path: filepath.ToSlash(filepath.Join(entry.Name(), "plugin.json")), Error: err.Error()})
			continue
		}
		specs = append(specs, Spec{Name: manifest.ID, Command: command, Args: append([]string(nil), manifest.Args...),
			UpdateEvery: manifest.UpdateEvery, Timeout: manifest.Timeout, Source: "manifest", Manifest: manifest})
	}
	return specs, diagnostics
}

func readManifest(path, id string) (*Manifest, string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<10 {
		return nil, "", errors.New("manifest must be a regular file of at most 64 KiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, "", errors.New("cannot read manifest")
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, (64<<10)+1))
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
	if len(manifest.Capabilities) != 1 || manifest.Capabilities[0] != "metrics" {
		return nil, "", errors.New("v1 packages must declare exactly the metrics capability")
	}
	if strings.TrimSpace(manifest.Name) == "" || len(manifest.Name) > 120 || strings.TrimSpace(manifest.Version) == "" || len(manifest.Version) > 64 || len(manifest.Description) > 2000 {
		return nil, "", errors.New("invalid package name, version or description length")
	}
	if manifest.UpdateEvery < 0 || manifest.UpdateEvery > 86400 || manifest.Timeout < 0 || manifest.Timeout > 86400 {
		return nil, "", errors.New("update_every and timeout must be between 0 and 86400 seconds")
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

func newPluginParser(spec Spec, reg *registry.Registry) *Parser {
	p := &Parser{Plugin: spec.Name, Reg: reg}
	if spec.Manifest != nil {
		p.Namespace = fmt.Sprintf("plugin.%s.", spec.Manifest.ID)
		p.MaxCharts = 128
		p.MaxDimensions = 64
	}
	return p
}
