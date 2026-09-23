//go:build darwin

package collect

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/process"
	"golang.org/x/sys/unix"
)

func findAppProcess(entries []appProcess, pid int32) *appProcess {
	for i := range entries {
		if entries[i].process.Pid == pid {
			return &entries[i]
		}
	}
	return nil
}

func TestAppsDarwinSnapshotIdentityAndExit(t *testing.T) {
	ctx := context.Background()
	child := exec.Command("/bin/sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	pid := int32(child.Process.Pid)
	entries, err := listAppProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.process.Pid <= 0 {
			t.Fatalf("invalid PID included: %d", entry.process.Pid)
		}
	}
	sample := findAppProcess(entries, pid)
	if sample == nil {
		t.Fatal("running child omitted from snapshot")
	}
	legacy, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	started, err := legacy.CreateTimeWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if sample.startedAt <= 0 || sample.startedAt/1000 != started {
		t.Fatalf("birth time mismatch: snapshot=%d existing=%d", sample.startedAt, started)
	}
	got, cmdline, err := sample.readIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want, err := legacy.NameWithContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("name changed: got %q want %q", got, want)
	}
	wantCmdline, err := legacy.CmdlineWithContext(ctx)
	if err != nil || cmdline != wantCmdline {
		t.Fatalf("command line mismatch: got %q want %q, err=%v", cmdline, wantCmdline, err)
	}
	if _, err := sample.process.TimesWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := sample.process.MemoryInfoWithContext(ctx); err != nil {
		t.Fatal(err)
	}
	a := &appsCollector{userCache: map[string]string{}, groupCache: map[string]string{}}
	var owners, portable, fallback pidState
	sample.readOwners(ctx, a, &owners)
	a.readPortableOwners(ctx, legacy, &portable)
	(appProcess{process: legacy}).readOwners(ctx, a, &fallback)
	if owners.ppid != int32(os.Getpid()) || owners.user == "" || owners.osGroup == "" ||
		!reflect.DeepEqual(owners, portable) || !reflect.DeepEqual(owners, fallback) {
		t.Fatal("snapshot parent/user/group differ from the live portable identity")
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	entries, err = listAppProcesses(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if findAppProcess(entries, pid) != nil {
		t.Fatal("exited PID retained in fresh snapshot")
	}
	if findAppProcess(entries, int32(os.Getpid())) == nil {
		t.Fatal("own PID missing")
	}
}

func TestAppsDarwinSnapshotIdentityContract(t *testing.T) {
	for _, tc := range []struct {
		name, comm, wantName, wantCommand string
		argv                              []string
		argsErr                           error
		wantError                         bool
		wantReads                         int
	}{
		{name: "short", comm: "sleep", argv: []string{"/bin/sleep", "30"}, wantName: "sleep", wantCommand: "/bin/sleep 30", wantReads: 1},
		{name: "short denied args", comm: "short", argsErr: os.ErrPermission, wantName: "short", wantReads: 1},
		{name: "long with spaces", comm: "monitor long na", argv: []string{"/tmp/monitor long name", "argument with spaces", ""}, wantName: "monitor long name", wantCommand: "/tmp/monitor long name argument with spaces ", wantReads: 1},
		{name: "long denied args", comm: "monitor long na", argsErr: os.ErrPermission, wantError: true, wantReads: 1},
		{name: "long process exited", comm: "monitor long na", argsErr: os.ErrNotExist, wantError: true, wantReads: 1},
		{name: "long empty args", comm: "monitor long na", wantName: "monitor long na", wantReads: 1},
		{name: "long empty argv zero", comm: "monitor long na", argv: []string{"", "argument"}, wantName: "monitor long na", wantCommand: " argument", wantReads: 1},
		{name: "fourteen bytes stay short", comm: strings.Repeat("s", 14), argv: []string{"/tmp/expanded"}, wantName: strings.Repeat("s", 14), wantCommand: "/tmp/expanded", wantReads: 1},
		{name: "fifteen bytes expand", comm: strings.Repeat("s", 15), argv: []string{"/tmp/expanded"}, wantName: "expanded", wantCommand: "/tmp/expanded", wantReads: 1},
		{name: "empty name", argv: []string{"/tmp/not a fallback"}},
		{name: "nul terminator", comm: "name\x00ignore", argv: []string{"name"}, wantName: "name", wantCommand: "name", wantReads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := appProcessMetadata{valid: true}
			copy(m.comm[:], tc.comm)
			reads := 0
			name, command, err := m.readIdentity(context.Background(), func(context.Context) ([]string, error) {
				reads++
				return tc.argv, tc.argsErr
			})
			if tc.wantError {
				if !errors.Is(err, tc.argsErr) || name != "" || command != "" {
					t.Fatalf("unavailable identity: name=%q command=%q err=%v", name, command, err)
				}
			} else if err != nil || name != tc.wantName || command != tc.wantCommand {
				t.Fatalf("name=%q command=%q err=%v; want %q %q", name, command, err, tc.wantName, tc.wantCommand)
			}
			if reads != tc.wantReads {
				t.Fatalf("args reads=%d want %d", reads, tc.wantReads)
			}
		})
	}
}

func TestAppsDarwinIdentityCancellation(t *testing.T) {
	for _, cancelDuringRead := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		m := appProcessMetadata{valid: true}
		copy(m.comm[:], "sleep")
		if !cancelDuringRead {
			cancel()
		}
		reads := 0
		name, command, err := m.readIdentity(ctx, func(context.Context) ([]string, error) {
			reads++
			cancel()
			return []string{"sleep", "30"}, nil
		})
		cancel()
		if !errors.Is(err, context.Canceled) || name != "" || command != "" {
			t.Fatalf("canceled identity: name=%q command=%q err=%v", name, command, err)
		}
		if (reads == 1) != cancelDuringRead {
			t.Fatalf("cancelDuringRead=%t reads=%d", cancelDuringRead, reads)
		}
	}
}

func TestAppsDarwinLongProcessIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor identity long name")
	writeAppIdentityFixture(t, path)
	child := exec.Command(path, "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	entries, err := listAppProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p := findAppProcess(entries, int32(child.Process.Pid))
	if p == nil {
		t.Fatal("long-named child missing")
	}
	name, command, err := p.readIdentity(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	legacyName, legacyCommand, err := readPortableAppIdentity(context.Background(), p.process)
	if err != nil {
		t.Fatal(err)
	}
	if name != filepath.Base(path) || command != path+" 30" || name != legacyName || command != legacyCommand {
		t.Fatalf("long process identity differs: %q %q vs %q %q", name, command, legacyName, legacyCommand)
	}
	// The identity chooses the same group, including configured command-line
	// patterns, and an unavailable snapshot retains the portable fallback.
	a := &appsCollector{}
	if err := a.Configure(func(v any) error {
		cfg := v.(*appsConfig)
		cfg.Cmdline = true
		cfg.Groups = map[string][]string{"fixture": {" 30"}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if a.match(name, command).name != "fixture" || a.match(legacyName, legacyCommand).name != "fixture" {
		t.Fatal("process group changed")
	}
	fallback := appProcess{process: p.process}
	fallbackName, fallbackCommand, err := fallback.readIdentity(context.Background())
	if err != nil || fallbackName != name || fallbackCommand != command {
		t.Fatal("portable identity fallback differs")
	}
}

func writeAppIdentityFixture(tb testing.TB, path string) {
	tb.Helper()
	binary, err := os.ReadFile("/bin/sleep")
	if err != nil {
		tb.Fatal(err)
	}
	if err := os.WriteFile(path, binary, 0700); err != nil {
		tb.Fatal(err)
	}
	// Sign only the temporary copy. A relocated system signature may be
	// rejected asynchronously, yielding a misleading short-lived fixture.
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", path).CombinedOutput(); err != nil {
		tb.Fatalf("sign fixture: %v: %s", err, out)
	}
}

func BenchmarkAppsDarwinIdentity(b *testing.B) {
	for _, processName := range []string{"short", "monitor identity long name"} {
		path := filepath.Join(b.TempDir(), processName)
		writeAppIdentityFixture(b, path)
		child := exec.Command(path, "300")
		if err := child.Start(); err != nil {
			b.Fatal(err)
		}
		b.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
		entries, err := listAppProcesses(context.Background())
		if err != nil {
			b.Fatal(err)
		}
		p := findAppProcess(entries, int32(child.Process.Pid))
		if p == nil {
			b.Fatal("fixture process missing")
		}
		for _, mode := range []string{"legacy", "snapshot"} {
			b.Run(processName+"/"+mode, func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					var err error
					if mode == "legacy" {
						_, _, err = readPortableAppIdentity(context.Background(), p.process)
					} else {
						_, _, err = p.readIdentity(context.Background())
					}
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestAppsDarwinSnapshotCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := listAppProcesses(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error = %v", err)
	}
}

func TestAppsDarwinSnapshotOwners(t *testing.T) {
	for _, uid := range []uint32{0, 501} {
		entry := unix.KinfoProc{}
		entry.Proc.P_pid = 2147483647 // no live PID query can supply this fixture
		entry.Eproc.Ppid = 42
		entry.Eproc.Ucred.Uid = uid
		entry.Eproc.Pcred.P_ruid = 99
		entry.Eproc.Pcred.P_rgid = 20
		entry.Eproc.Ucred.Groups[0] = 80
		p := appProcessFromKinfo(&entry)
		a := &appsCollector{
			userCache:  map[string]string{"0": "root_fixture", "501": "user_fixture"},
			groupCache: map[string]string{"20": "real_group", "80": "effective_group"},
		}
		var st pidState
		p.readOwners(context.Background(), a, &st)
		want := "user_fixture"
		if uid == 0 {
			want = "root_fixture"
		}
		if st.ppid != 42 || st.user != want || st.osGroup != "real_group" {
			t.Fatalf("wrong credential source for uid=%d: %+v", uid, st)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		st = pidState{}
		p.readOwners(ctx, a, &st)
		if !reflect.DeepEqual(st, pidState{}) {
			t.Fatal("canceled read populated owners")
		}
	}
}

func BenchmarkAppsDarwinOwners(b *testing.B) {
	entries, err := listAppProcesses(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	p := findAppProcess(entries, int32(os.Getpid()))
	if p == nil {
		b.Fatal("own PID missing")
	}
	for _, mode := range []string{"legacy", "snapshot"} {
		b.Run(mode, func(b *testing.B) {
			a := &appsCollector{userCache: map[string]string{}, groupCache: map[string]string{}}
			var st pidState
			p.readOwners(context.Background(), a, &st) // warm shared UID/GID caches
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if mode == "legacy" {
					a.readPortableOwners(context.Background(), p.process, &st)
				} else {
					p.readOwners(context.Background(), a, &st)
				}
			}
		})
	}
}

func BenchmarkAppsProcessSnapshot(b *testing.B) {
	ctx := context.Background()
	for _, baseline := range []bool{true, false} {
		name := "snapshot"
		if baseline {
			name = "legacy"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if baseline {
					if _, err := process.ProcessesWithContext(ctx); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, err := listAppProcesses(ctx); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
