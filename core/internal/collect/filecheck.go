package collect

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// filecheckConfig is collectors.modules.filecheck.
type filecheckConfig struct {
	Files []string `yaml:"files"`
	Dirs  []string `yaml:"dirs"`
}

type filecheckCollector struct {
	cfg  filecheckConfig
	seen map[string]bool
}

func init() {
	Register("filecheck", func() Collector { return &filecheckCollector{} })
}

func (f *filecheckCollector) Name() string { return "filecheck" }

func (f *filecheckCollector) Configure(decode func(v any) error) error {
	return decode(&f.cfg)
}

func (f *filecheckCollector) Init(reg *registry.Registry) error {
	_ = f.Configure(func(any) error { return nil })
	if len(f.cfg.Files) == 0 && len(f.cfg.Dirs) == 0 {
		return fmt.Errorf("filecheck: no files or dirs configured")
	}
	f.seen = map[string]bool{}
	return nil
}

func (f *filecheckCollector) Collect(_ context.Context, reg *registry.Registry, now time.Time) error {
	for _, path := range f.cfg.Files {
		f.ensureFile(reg, path)
		id := sanitizeID(filepath.ToSlash(path))
		st, err := os.Stat(path)
		exist, missing := 0.0, 1.0
		if err == nil && !st.IsDir() {
			exist, missing = 1, 0
			_ = reg.Collect("filecheck.file_size_bytes."+id, now, map[string]float64{"size": float64(st.Size())})
			_ = reg.Collect("filecheck.file_modification_time_ago."+id, now, map[string]float64{
				"mtime_ago": now.Sub(st.ModTime()).Seconds()})
		}
		_ = reg.Collect("filecheck.file_existence_status."+id, now, map[string]float64{"exist": exist, "not_exist": missing})
	}
	for _, path := range f.cfg.Dirs {
		f.ensureDir(reg, path)
		id := sanitizeID(filepath.ToSlash(path))
		st, err := os.Stat(path)
		exist, missing := 0.0, 1.0
		if err == nil && st.IsDir() {
			exist, missing = 1, 0
			ents, _ := os.ReadDir(path)
			_ = reg.Collect("filecheck.dir_files_count."+id, now, map[string]float64{"files": float64(len(ents))})
			_ = reg.Collect("filecheck.dir_modification_time_ago."+id, now, map[string]float64{
				"mtime_ago": now.Sub(st.ModTime()).Seconds()})
		}
		_ = reg.Collect("filecheck.dir_existence_status."+id, now, map[string]float64{"exist": exist, "not_exist": missing})
	}
	return nil
}

func (f *filecheckCollector) ensureFile(reg *registry.Registry, path string) {
	key := "file:" + path
	if f.seen[key] {
		return
	}
	f.seen[key] = true
	id := sanitizeID(filepath.ToSlash(path))
	labels := map[string]string{"file_path": path}
	for _, c := range []*registry.Chart{
		{ID: "filecheck.file_existence_status." + id, Context: "filecheck.file_existence_status", Title: "File existence", Units: "status", Priority: 55800, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "exist"}, {ID: "not_exist"}}},
		{ID: "filecheck.file_modification_time_ago." + id, Context: "filecheck.file_modification_time_ago", Title: "File time since the last modification", Units: "seconds", Priority: 55810, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "mtime_ago"}}},
		{ID: "filecheck.file_size_bytes." + id, Context: "filecheck.file_size_bytes", Title: "File size", Units: "bytes", Priority: 55820, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "size"}}},
	} {
		c.Family, c.Plugin, c.Module = "filecheck", "filecheck", "filecheck"
		reg.AddChart(c)
	}
}

func (f *filecheckCollector) ensureDir(reg *registry.Registry, path string) {
	key := "dir:" + path
	if f.seen[key] {
		return
	}
	f.seen[key] = true
	id := sanitizeID(filepath.ToSlash(path))
	labels := map[string]string{"dir_path": path}
	for _, c := range []*registry.Chart{
		{ID: "filecheck.dir_existence_status." + id, Context: "filecheck.dir_existence_status", Title: "Directory existence", Units: "status", Priority: 55830, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "exist"}, {ID: "not_exist"}}},
		{ID: "filecheck.dir_modification_time_ago." + id, Context: "filecheck.dir_modification_time_ago", Title: "Directory time since the last modification", Units: "seconds", Priority: 55840, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "mtime_ago"}}},
		{ID: "filecheck.dir_files_count." + id, Context: "filecheck.dir_files_count", Title: "Directory files count", Units: "files", Priority: 55850, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: "files"}}},
	} {
		c.Family, c.Plugin, c.Module = "filecheck", "filecheck", "filecheck"
		reg.AddChart(c)
	}
}
