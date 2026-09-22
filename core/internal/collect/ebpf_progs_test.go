package collect

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestParseVmstatAndKprobe(t *testing.T) {
	vm := parseVmstatMap("pgpgin 10\npgpgout 4\npgfault 100\npgmajfault 5\noom_kill 2\npswpin 1\npswpout 3\n")
	if vm["pgpgin"] != 10 || vm["oom_kill"] != 2 || vm["pswpout"] != 3 {
		t.Fatalf("%v", vm)
	}
	kp := parseKprobeProfile("vfs_read  12  0\np:kprobes/vfs_write  7  1\n")
	if kp["vfs_read"] != 12 || kp["vfs_write"] != 7 {
		t.Fatalf("%v", kp)
	}
	rd, wr := sumDiskOps("   8       0 sda 10 0 0 0 20 0 0 0 0 0 0\n   7       0 loop0 99 0 0 0 99 0 0 0\n")
	if rd != 10 || wr != 20 {
		t.Fatalf("disk %v %v", rd, wr)
	}
}

func TestEBPFProgFamilyFixture(t *testing.T) {
	root := t.TempDir()
	write := func(name, v string) {
		t.Helper()
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil && !os.IsExist(err) {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("vmstat", "pgpgin 8\npgpgout 3\npgfault 50\npgmajfault 2\noom_kill 1\npswpin 4\npswpout 5\n")
	write("dentry", "100 40 45 0")
	write("file-nr", "20\t10\t1000")
	write("inode-state", "30\t8")
	write("stat", "cpu 1 2 3\nprocesses 9\nintr 11\n")
	write("shm", "key shmid perms size\n0x1 1 600 4096\n")
	write("diskstats", "   8       0 sda 6 0 0 0 7 0 0 0 0 0 0")
	write("mounts", "/dev/sda1 / ext4 rw 0 0\nproc /proc proc rw 0 0")
	write("interrupts", "           CPU0\n  0:  5  IO-APIC   2-edge  timer\n")
	write("kprobes", "vfs_read  3  0\nvfs_write  2  0\n")

	c := &ebpfCollector{}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return nil, os.ErrNotExist
	}
	c.vmstat = filepath.Join(root, "vmstat")
	c.dentry = filepath.Join(root, "dentry")
	c.filenr = filepath.Join(root, "file-nr")
	c.inodes = filepath.Join(root, "inode-state")
	c.stat = filepath.Join(root, "stat")
	c.shm = filepath.Join(root, "shm")
	c.diskstats = filepath.Join(root, "diskstats")
	c.mounts = filepath.Join(root, "mounts")
	c.interrupts = filepath.Join(root, "interrupts")
	c.kprobes = filepath.Join(root, "kprobes")

	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if c.haveTool {
		t.Fatal("bpftool should be off")
	}
	now := time.Unix(1_700_000_000, 0)
	if err := c.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	write("vmstat", "pgpgin 18\npgpgout 9\npgfault 80\npgmajfault 4\noom_kill 4\npswpin 6\npswpout 8\n")
	write("kprobes", "vfs_read  10  0\nvfs_write  7  0\n")
	if err := c.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"ebpf.cachestat", "ebpf.dcstat", "ebpf.fd", "ebpf.vfs", "ebpf.oomkill", "ebpf.process", "ebpf.shm", "ebpf.swap", "ebpf.disk", "ebpf.mount", "ebpf.hardirq"} {
		if _, ok := reg.Chart(id); !ok {
			t.Fatalf("missing %s", id)
		}
	}
	ch, _ := reg.Chart("ebpf.oomkill")
	_, vals := ch.LastValues()
	if vals["kills"] != 3 {
		t.Fatalf("oomkill %v", vals)
	}
	ch, _ = reg.Chart("ebpf.vfs")
	_, vals = ch.LastValues()
	if vals["read"] != 7 || vals["write"] != 5 {
		t.Fatalf("vfs %v", vals)
	}
	ch, _ = reg.Chart("ebpf.dcstat")
	_, vals = ch.LastValues()
	if vals["existing"] != 100 || vals["unused"] != 40 {
		t.Fatalf("dcstat %v", vals)
	}
}
