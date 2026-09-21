package collect

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func execRun(timeout time.Duration) func(ctx context.Context, name string, args ...string) ([]byte, error) {
	return func(ctx context.Context, name string, args ...string) ([]byte, error) {
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return exec.CommandContext(cctx, name, args...).Output()
	}
}

type fileCursor struct {
	path string
	off  int64
}

func (f *fileCursor) skipToEnd() error {
	st, err := os.Stat(f.path)
	if err != nil {
		return err
	}
	f.off = st.Size()
	return nil
}

func (f *fileCursor) lines() ([]string, error) {
	st, err := os.Stat(f.path)
	if err != nil {
		return nil, err
	}
	size := st.Size()
	if size < f.off {
		f.off = 0
	}
	if size == f.off {
		return nil, nil
	}
	fh, err := os.Open(f.path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	if _, err := fh.Seek(f.off, io.SeekStart); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(fh, 8<<20))
	if err != nil {
		return nil, err
	}
	f.off += int64(len(b))
	s := strings.TrimSuffix(string(b), "\n")
	if s == "" {
		return nil, nil
	}
	return strings.Split(s, "\n"), nil
}

// globMatch is a case-insensitive path.Match that never errors (a malformed
// pattern simply does not match).
func globMatch(pattern, name string) bool {
	ok, _ := path.Match(strings.ToLower(pattern), strings.ToLower(name))
	return ok
}

func readTrim(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func durDefault(v, def time.Duration) time.Duration {
	if v <= 0 {
		return def
	}
	return v
}

func sysChart(id, family, title, units string, prio int, dims ...*registry.Dimension) *registry.Chart {
	return &registry.Chart{
		ID: id, Family: family, Title: title, Units: units, Priority: prio,
		Plugin: "system", Module: "proc", Dimensions: dims,
	}
}

func chartMeta(ch *registry.Chart, family, plugin, module string) *registry.Chart {
	ch.Family, ch.Plugin, ch.Module = family, plugin, module
	return ch
}

func incDim(id string) *registry.Dimension {
	return &registry.Dimension{ID: id, Algorithm: registry.Incremental}
}

func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

func jsonMap(b []byte) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func nestFloat(v any, path ...string) float64 {
	cur := v
	for _, p := range path {
		switch n := cur.(type) {
		case map[string]any:
			cur = n[p]
		default:
			return 0
		}
	}
	switch n := cur.(type) {
	case float64:
		return n
	case json.Number:
		f, _ := n.Float64()
		return f
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case string:
		return firstFloat(n)
	case bool:
		if n {
			return 1
		}
		return 0
	default:
		return 0
	}
}

func nestString(v any, path ...string) string {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = m[p]
	}
	if s, ok := cur.(string); ok {
		return s
	}
	return ""
}

func nestMap(v any, path ...string) map[string]any {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	m, _ := cur.(map[string]any)
	return m
}

func nestSlice(v any, path ...string) []any {
	cur := v
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[p]
	}
	s, _ := cur.([]any)
	return s
}
