package collect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func writeFakeUnit(t *testing.T, root, name string, usec, mem, rbytes int) {
	t.Helper()
	dir := filepath.Join(root, "system.slice", name+".service")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec " + strconv.Itoa(usec) + "\nuser_usec 1\nsystem_usec 1\n",
		"memory.current": strconv.Itoa(mem) + "\n",
		"io.stat":        "253:0 rbytes=" + strconv.Itoa(rbytes) + " wbytes=0 rios=1 wios=0\n253:16 rbytes=" + strconv.Itoa(rbytes) + " wbytes=0\n",
	}
	for f, c := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSystemdCollectorFakeCgroup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "cgroup.controllers"), []byte("cpu io memory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeFakeUnit(t, root, "ssh", 1_000_000, 50<<20, 1024)
	writeFakeUnit(t, root, "noisy", 1_000_000, 10<<20, 0)

	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	s := &systemdCollector{}
	if err := s.Configure(func(v any) error {
		c := v.(*systemdConfig)
		c.CgroupRoot = root
		c.Exclude = []string{"noisy"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := s.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	writeFakeUnit(t, root, "ssh", 1_250_000, 50<<20, 1024+2048) // +0.25 core-second, +4 KiB read
	if err := s.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	cpu, _ := reg.Chart("systemd.cpu")
	_, cv := cpu.LastValues()
	if cv["ssh"] != 25 {
		t.Fatalf("cpu = %v", cv)
	}
	if _, ok := cv["noisy"]; ok {
		t.Fatal("excluded unit collected")
	}
	mem, _ := reg.Chart("systemd.mem")
	_, mv := mem.LastValues()
	if mv["ssh"] != 50 {
		t.Fatalf("mem = %v", mv)
	}
	rd, _ := reg.Chart("systemd.io_read")
	_, rv := rd.LastValues()
	if rv["ssh"] != 4 {
		t.Fatalf("io read = %v", rv)
	}
}
