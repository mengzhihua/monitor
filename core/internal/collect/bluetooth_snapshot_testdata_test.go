package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func bluetoothTestdataDir(t *testing.T, platform string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(file), "testdata", "bluetooth", platform)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("missing testdata snapshot %s: %v", dir, err)
	}
	return dir
}

func readBluetoothExpected(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "expected.json"))
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestBluetoothLinuxTestdataSnapshot(t *testing.T) {
	dir := bluetoothTestdataDir(t, "linux")
	want := readBluetoothExpected(t, dir)
	b := &bluetoothCollector{goos: "linux"}
	if err := b.Configure(func(v any) error {
		cfg := v.(*bluetoothConfig)
		cfg.SnapshotDir = dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Unix(1_760_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("bluetooth.devices")
	if !ok {
		t.Fatal("missing bluetooth.devices")
	}
	_, vals := ch.LastValues()
	if vals["connected"] != want["connected"].(float64) {
		t.Fatalf("connected=%v want %v", vals["connected"], want["connected"])
	}
	byType, ok := reg.Chart("bluetooth.connected_by_type")
	if !ok {
		t.Fatal("missing bluetooth.connected_by_type")
	}
	_, typeVals := byType.LastValues()
	if typeVals["audio"] != want["audio"].(float64) || typeVals["input"] != want["input"].(float64) {
		t.Fatalf("type buckets=%v want audio=%v input=%v", typeVals, want["audio"], want["input"])
	}
	speaker := want["speaker_address"].(string)
	if _, ok := reg.Chart("bluetooth.device_rssi." + speaker); !ok {
		t.Fatalf("missing per-device RSSI for %s", speaker)
	}
}

func TestBluetoothDarwinTestdataSnapshot(t *testing.T) {
	dir := bluetoothTestdataDir(t, "darwin")
	want := readBluetoothExpected(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "system_profiler.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapters, devices, err := parseSystemProfilerJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if float64(len(adapters)) != want["adapters"].(float64) {
		t.Fatalf("adapters=%d", len(adapters))
	}
	if float64(len(devices)) != want["devices"].(float64) {
		t.Fatalf("devices=%d", len(devices))
	}
	var speaker BluetoothDevice
	for _, d := range devices {
		if d.Name == want["speaker_name"].(string) {
			speaker = d
		}
	}
	if speaker.Address != want["speaker_address"].(string) || !speaker.Connected {
		t.Fatalf("%+v", speaker)
	}
	if speaker.RSSI == nil || float64(*speaker.RSSI) != want["rssi"].(float64) {
		t.Fatalf("rssi=%v", speaker.RSSI)
	}
	if speaker.Battery == nil || float64(*speaker.Battery) != want["battery"].(float64) {
		t.Fatalf("battery=%v", speaker.Battery)
	}
}

func TestBluetoothAndroidTestdataSnapshot(t *testing.T) {
	dir := bluetoothTestdataDir(t, "android")
	want := readBluetoothExpected(t, dir)
	raw, err := os.ReadFile(filepath.Join(dir, "dumpsys_bluetooth_manager.txt"))
	if err != nil {
		t.Fatal(err)
	}
	adapters, devices := parseDumpsysBluetooth(string(raw))
	if float64(len(adapters)) != want["adapters"].(float64) {
		t.Fatalf("adapters=%d", len(adapters))
	}
	if float64(len(devices)) != want["devices"].(float64) {
		t.Fatalf("devices=%d", len(devices))
	}
	var speaker *BluetoothDevice
	for i := range devices {
		if devices[i].Address == want["speaker_address"].(string) {
			speaker = &devices[i]
		}
	}
	if speaker == nil || !speaker.Connected || speaker.Audio == nil {
		t.Fatalf("%+v", devices)
	}
	if speaker.Audio.Codec != want["codec"].(string) {
		t.Fatalf("codec=%q", speaker.Audio.Codec)
	}
	if float64(speaker.Audio.SampleRate) != want["sample_rate"].(float64) || float64(speaker.Audio.BitDepth) != want["bit_depth"].(float64) {
		t.Fatalf("%+v", speaker.Audio)
	}
	if speaker.Audio.Bitrate != want["bitrate"].(float64) {
		t.Fatalf("bitrate=%v", speaker.Audio.Bitrate)
	}
}

func TestBluetoothLinuxAACAndAptXSnapshot(t *testing.T) {
	dir := bluetoothTestdataDir(t, "linux-aac-aptx")
	want := readBluetoothExpected(t, dir)
	b := &bluetoothCollector{goos: "linux"}
	if err := b.Configure(func(v any) error {
		cfg := v.(*bluetoothConfig)
		cfg.SnapshotDir = dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Unix(1_760_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("bluetooth.audio_streams")
	if !ok {
		t.Fatal("missing bluetooth.audio_streams")
	}
	_, vals := ch.LastValues()
	if vals[want["aac"].(string)] != 1 || vals[want["aptx"].(string)] != 1 {
		t.Fatalf("codecs=%v want AAC and aptX", vals)
	}
}

func TestBluetoothCollectorChartsFromTestdataDir(t *testing.T) {
	// Keep the older inline helper path exercised via the shared linux snapshot.
	b := newBluetoothSnapshotCollector(t)
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := b.Collect(context.Background(), reg, time.Unix(1_760_000_000, 0)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("bluetooth.devices"); !ok {
		t.Fatal("missing bluetooth.devices")
	}
}
