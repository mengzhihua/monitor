package collect

import (
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

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
