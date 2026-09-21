package collect

import (
	"context"
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
