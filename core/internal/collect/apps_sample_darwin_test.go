//go:build darwin

package collect

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/shirou/gopsutil/v4/process"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestAppsDarwinTaskInfoABIAndConversion(t *testing.T) {
	var layout process.ProcTaskInfo
	if appTaskInfoSize != 96 || unsafe.Offsetof(layout.Resident_size) != 8 ||
		unsafe.Offsetof(layout.Total_user) != 16 || unsafe.Offsetof(layout.Threadnum) != 84 {
		t.Fatal("proc_taskinfo layout no longer matches the macOS SDK")
	}
	calls := 0
	reader := &appDarwinTaskReader{
		secondsPerTick: 25.0 / 3 / 1e9,
		info: func(pid, flavor int32, arg uint64, out *process.ProcTaskInfo, size int32) int32 {
			calls++
			if pid != 123 || flavor != appProcPIDTaskInfo || arg != 0 || size != 96 {
				t.Fatalf("unexpected proc_pidinfo arguments: %d %d %d %d", pid, flavor, arg, size)
			}
			out.Total_user = 150000000
			out.Total_system = 210000000
			out.Resident_size = 123456
			out.Threadnum = 7
			return size
		},
	}
	got := reader.read(123)
	if !got.ok || math.Abs(got.cpuSec-3) > 1e-9 || got.rss != 123456 || got.threads != 7 || calls != 1 {
		t.Fatalf("combined sample = %+v, calls = %d", got, calls)
	}
	if got := reader.read(0); got.ok || calls != 1 {
		t.Fatal("invalid PID invoked the native reader")
	}
}

func TestAppsDarwinTaskInfoRejectsFailedAndPartialReads(t *testing.T) {
	for _, returned := range []int32{-1, 0, 95, 97} {
		reader := &appDarwinTaskReader{
			secondsPerTick: 1e-9,
			info: func(_, _ int32, _ uint64, out *process.ProcTaskInfo, _ int32) int32 {
				out.Total_user, out.Resident_size, out.Threadnum = 999, 888, 7
				return returned
			},
		}
		if got := reader.read(123); got.ok || got.cpuSec != 0 || got.rss != 0 || got.threads != 0 {
			t.Fatalf("return size %d treated as a valid sample: %+v", returned, got)
		}
	}
}

func TestAppsDarwinCombinedSampleMatchesLiveCounters(t *testing.T) {
	ctx := context.Background()
	p := &process.Process{Pid: int32(os.Getpid())}
	if _, err := appDarwinTaskAPI(); err != nil {
		t.Fatal(err)
	}
	before := readAppProcessPortable(ctx, p, false)
	got := readAppProcessSample(ctx, p, false)
	after := readAppProcessPortable(ctx, p, false)
	if !before.ok || !got.ok || !after.ok || got.rss == 0 || got.threads == 0 {
		t.Fatalf("own process counters unavailable: before=%+v got=%+v after=%+v", before, got, after)
	}
	if got.cpuSec+1e-6 < before.cpuSec || got.cpuSec-1e-6 > after.cpuSec {
		t.Fatalf("CPU counter conversion disagrees with existing reader: %f <= %f <= %f", before.cpuSec, got.cpuSec, after.cpuSec)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if readAppProcessSample(canceled, p, false).ok {
		t.Fatal("canceled sample read counters")
	}
}

func TestAppsDarwinReaderInitFailureUsesPortableFallback(t *testing.T) {
	original := appDarwinTaskAPI
	t.Cleanup(func() { appDarwinTaskAPI = original })
	appDarwinTaskAPI = func() (*appDarwinTaskReader, error) { return nil, errors.New("missing symbol") }
	p := &process.Process{Pid: int32(os.Getpid())}
	if got := readAppProcessSample(context.Background(), p, false); !got.ok || got.rss == 0 || got.threads == 0 {
		t.Fatalf("portable fallback unavailable: %+v", got)
	}
}

func TestAppsDarwinFailedSamplePreservesCPUBaseline(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	ctx, now := context.Background(), time.Now()
	if err := a.Collect(ctx, reg, now); err != nil {
		t.Fatal(err)
	}
	pid := int32(os.Getpid())
	st := a.pids[pid]
	if st == nil || !st.hasCPU {
		t.Fatal("own PID has no valid initial CPU baseline")
	}
	initialCPU, groupCPU := st.cpuMs, a.cpuMs[st.group.name]
	st.user, st.osGroup = "fixture_cpu_owner", "fixture_cpu_group"
	st.cpuPct = 12.5 // A previous successful interval had nonzero activity.
	ownerCPU, osGroupCPU := a.cpuUser[st.user], a.cpuOSGroup[st.osGroup]
	original := appDarwinTaskAPI
	t.Cleanup(func() { appDarwinTaskAPI = original })
	denied := true
	reader := &appDarwinTaskReader{
		secondsPerTick: 1e-9,
		info: func(id, _ int32, _ uint64, out *process.ProcTaskInfo, size int32) int32 {
			if denied || id != pid {
				return 0
			}
			out.Total_user = uint64((initialCPU + 5) * 1e6)
			return size
		},
	}
	appDarwinTaskAPI = func() (*appDarwinTaskReader, error) { return reader, nil }
	if err := a.Collect(ctx, reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if a.pids[pid] != st || !st.hasCPU || st.cpuMs != initialCPU || a.cpuMs[st.group.name] != groupCPU {
		t.Fatal("failed taskinfo read replaced the last valid CPU baseline")
	}
	if st.cpuPct != 0 || a.cpuUser[st.user] != ownerCPU || a.cpuOSGroup[st.osGroup] != osGroupCPU {
		t.Fatal("failed taskinfo read reused the previous CPU rate for an owner")
	}
	denied = false
	if err := a.Collect(ctx, reg, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if delta := a.cpuMs[st.group.name] - groupCPU; math.Abs(delta-5) > 1e-3 {
		t.Fatalf("recovered counter produced a lifetime CPU spike: delta = %f, want 5 ms", delta)
	}
	if math.Abs(a.cpuUser[st.user]-ownerCPU-5) > 1e-3 || math.Abs(a.cpuOSGroup[st.osGroup]-osGroupCPU-5) > 1e-3 {
		t.Fatal("recovery did not add exactly the new CPU time to both owners")
	}
	if math.Abs(st.cpuPct-0.25) > 1e-3 {
		t.Fatalf("CPU rate after a missing interval = %f, want 0.25%% over two seconds", st.cpuPct)
	}
}

func TestAppsDarwinSlowCanceledSampleDoesNotBlockTable(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := a.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	before := a.processes(map[string]string{"sort": "pid"})
	pid := int32(os.Getpid())
	initial := *a.pids[pid]
	started, release := make(chan struct{}), make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := appDarwinTaskAPI
	reader := &appDarwinTaskReader{secondsPerTick: 1e-9, info: func(id, _ int32, _ uint64, out *process.ProcTaskInfo, size int32) int32 {
		if id == pid {
			close(started)
			<-release
		}
		out.Total_user = uint64((initial.cpuMs + 500) * 1e6)
		out.Resident_size = 999999
		return size
	}}
	appDarwinTaskAPI = func() (*appDarwinTaskReader, error) { return reader, nil }
	done := make(chan error, 1)
	go func() { done <- a.Collect(ctx, reg, now.Add(time.Second)) }()
	defer func() {
		cancel()
		close(release)
		err := <-done
		appDarwinTaskAPI = original
		if !errors.Is(err, context.Canceled) {
			t.Errorf("interrupted collection returned %v", err)
		}
		if !reflect.DeepEqual(initial, *a.pids[pid]) || a.last != now ||
			!reflect.DeepEqual(before, a.processes(map[string]string{"sort": "pid"})) {
			t.Error("interrupted OS sample changed a baseline or published snapshot")
		}
		for _, update := range a.samples[:cap(a.samples)] {
			if update.state != nil || update.counters.name != "" {
				t.Error("aborted pass retained a staging reference")
				break
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture sampler was not reached")
	}
	queried := make(chan Table, 1)
	go func() { queried <- a.processes(map[string]string{"sort": "pid"}) }()
	select {
	case table := <-queried:
		if !reflect.DeepEqual(table, before) {
			t.Fatal("in-flight sample exposed a partial table")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("process query waited for the blocked OS sampler")
	}
}

func TestAppsDarwinCanceledColdPassKeepsIdentityOnly(t *testing.T) {
	reg := registry.New(&registry.Host{UpdateEvery: 1}, nil)
	a := &appsCollector{}
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	pid := int32(os.Getpid())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	original := appDarwinTaskAPI
	defer func() { appDarwinTaskAPI = original }()
	reader := &appDarwinTaskReader{secondsPerTick: 1e-9, info: func(id, _ int32, _ uint64, out *process.ProcTaskInfo, size int32) int32 {
		if id == pid {
			cancel()
		}
		out.Total_user = 1000000000
		return size
	}}
	appDarwinTaskAPI = func() (*appDarwinTaskReader, error) { return reader, nil }
	now := time.Now()
	if err := a.Collect(ctx, reg, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cold cancellation: %v", err)
	}
	st := a.pids[pid]
	if st == nil || st.name == "" || st.hasCPU || !st.seenAt.IsZero() || a.processes(nil).Total != 0 {
		t.Fatal("canceled cold pass lost identity cache or published partial counters")
	}
	appDarwinTaskAPI = original
	if err := a.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if a.pids[pid] != st || !st.hasCPU || a.processes(nil).Total == 0 {
		t.Fatal("next pass did not reuse cached identity and publish its first complete snapshot")
	}
}

func BenchmarkAppsDarwinTaskSample(b *testing.B) {
	ctx := context.Background()
	p := &process.Process{Pid: int32(os.Getpid())}
	if _, err := appDarwinTaskAPI(); err != nil {
		b.Fatal(err)
	}
	b.Run("separate", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := readAppProcessPortable(ctx, p, false); !got.ok {
				b.Fatal("sample unavailable")
			}
		}
	})
	b.Run("combined", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if got := readAppProcessSample(ctx, p, false); !got.ok {
				b.Fatal("sample unavailable")
			}
		}
	})
}
