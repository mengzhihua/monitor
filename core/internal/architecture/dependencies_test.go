// Package architecture enforces the internal dependency direction across all
// platform files, including those excluded by the current host's build tags.
package architecture

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const internalPrefix = "github.com/mengzhihua/monitor/core/internal/"

// Packages absent from this table are leaves: they may use standard/external
// libraries, but must not reach into another Monitor domain. New cross-domain
// dependencies require an explicit review here instead of growing invisibly.
var dependencies = map[string]string{
	"api":     "aianalysis audit collect config health hostshell hub ingest operations plugins registry sla stream tsdb",
	"collect": "ingest kafka preprocess registry tsdb",
	"config":  "collect export health hostshell plugins sla",
	"export":  "kafka registry",
	"health":  "registry tsdb",
	"hub":     "health registry stream tsdb",
	"ingest":  "registry",
	"plugins": "registry",
	"sla":     "health",
	"stream":  "collect health registry tsdb",
	"trapper": "registry",
}

func allowed(from, to string) bool {
	if from == to {
		return true
	}
	for _, dependency := range strings.Fields(dependencies[from]) {
		if dependency == to {
			return true
		}
	}
	return false
}

func TestInternalDependencies(t *testing.T) {
	// go test runs each package in its source directory. Avoid runtime.Caller:
	// -trimpath replaces that filename with a non-filesystem module path.
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		from := strings.Split(filepath.ToSlash(rel), "/")[0]
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if strings.HasPrefix(value, "github.com/mengzhihua/monitor/core/cmd/") {
				t.Errorf("%s imports the composition root %s", rel, value)
			}
			if !strings.HasPrefix(value, internalPrefix) {
				continue
			}
			to := strings.Split(strings.TrimPrefix(value, internalPrefix), "/")[0]
			if !allowed(from, to) {
				t.Errorf("%s: dependency %s -> %s crosses its domain boundary", rel, from, to)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDependencyPolicyRejectsInversions(t *testing.T) {
	for _, pair := range [][2]string{{"health", "api"}, {"health", "config"}, {"tsdb", "health"}, {"registry", "collect"}, {"operations", "api"}, {"newdomain", "api"}} {
		if allowed(pair[0], pair[1]) {
			t.Errorf("inverted dependency allowed: %v", pair)
		}
	}
	for _, pair := range [][2]string{{"health", "tsdb"}, {"api", "health"}, {"collect", "registry"}, {"hub", "stream"}} {
		if !allowed(pair[0], pair[1]) {
			t.Errorf("valid dependency rejected: %v", pair)
		}
	}
}
