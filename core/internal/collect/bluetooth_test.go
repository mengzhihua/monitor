package collect

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// dbusSnapshotFixture is a BlueZ object tree with a connected speaker
// streaming SBC and a connected LE keyboard.
const dbusSnapshotFixture = `method return time=1760000000.123456 sender=:1.7 -> destination=:1.99 serial=3 reply_serial=2
   array [
      dict entry(
         object path "/org/bluez/hci0"
         array [
            dict entry(
               string "org.bluez.Adapter1"
               array [
                  dict entry(
                     string "Address"
                     variant                         string "00:1A:7D:DA:71:13"
                  )
                  dict entry(
                     string "Alias"
                     variant                         string "monitor-host"
                  )
                  dict entry(
                     string "Powered"
                     variant                         boolean true
                  )
               ]
            )
         ]
      )
      dict entry(
         object path "/org/bluez/hci0/dev_AC_BF_71_11_22_33"
         array [
            dict entry(
               string "org.bluez.Device1"
               array [
                  dict entry(
                     string "Address"
                     variant                         string "AC:BF:71:11:22:33"
                  )
                  dict entry(
                     string "AddressType"
                     variant                         string "public"
                  )
                  dict entry(
                     string "Alias"
                     variant                         string "JBL Flip 5 (kitchen)"
                  )
                  dict entry(
                     string "Class"
                     variant                         uint32 2360340
                  )
                  dict entry(
                     string "Connected"
                     variant                         boolean true
                  )
                  dict entry(
                     string "Paired"
                     variant                         boolean true
                  )
                  dict entry(
                     string "RSSI"
                     variant                         int16 -47
                  )
                  dict entry(
                     string "Adapter"
                     variant                         object path "/org/bluez/hci0"
                  )
                  dict entry(
                     string "UUIDs"
                     variant                         array [
                           string "0000110b-0000-1000-8000-00805f9b34fb"
                           string "0000110d-0000-1000-8000-00805f9b34fb"
                           string "0000110e-0000-1000-8000-00805f9b34fb"
                        ]
                  )
               ]
            )
            dict entry(
               string "org.bluez.Battery1"
               array [
                  dict entry(
                     string "Percentage"
                     variant                         byte 90
                  )
               ]
            )
         ]
      )
      dict entry(
         object path "/org/bluez/hci0/dev_AC_BF_71_11_22_33/fd3"
         array [
            dict entry(
               string "org.bluez.MediaTransport1"
               array [
                  dict entry(
                     string "Device"
                     variant                         object path "/org/bluez/hci0/dev_AC_BF_71_11_22_33"
                  )
                  dict entry(
                     string "UUID"
                     variant                         string "0000110b-0000-1000-8000-00805f9b34fb"
                  )
                  dict entry(
                     string "Codec"
                     variant                         byte 0
                  )
                  dict entry(
                     string "Configuration"
                     variant                         array of bytes [
                           33 21 2 53
                        ]
                  )
                  dict entry(
                     string "State"
                     variant                         string "active"
                  )
                  dict entry(
                     string "Delay"
                     variant                         uint16 1500
                  )
               ]
            )
         ]
      )
      dict entry(
         object path "/org/bluez/hci0/dev_A0_B1_C2_D3_E4_F5"
         array [
            dict entry(
               string "org.bluez.Device1"
               array [
                  dict entry(
                     string "Address"
                     variant                         string "A0:B1:C2:D3:E4:F5"
                  )
                  dict entry(
                     string "AddressType"
                     variant                         string "random"
                  )
                  dict entry(
                     string "Alias"
                     variant                         string "Keychron K2"
                  )
                  dict entry(
                     string "Class"
                     variant                         uint32 1344
                  )
                  dict entry(
                     string "Connected"
                     variant                         boolean true
                  )
                  dict entry(
                     string "Paired"
                     variant                         boolean true
                  )
                  dict entry(
                     string "Adapter"
                     variant                         object path "/org/bluez/hci0"
                  )
                  dict entry(
                     string "UUIDs"
                     variant                         array [
                           string "00001812-0000-1000-8000-00805f9b34fb"
                           string "0000180f-0000-1000-8000-00805f9b34fb"
                        ]
                  )
               ]
            )
         ]
      )
   ]
`

// pactlSnapshotFixture is the PipeWire pulse view of the same speaker.
const pactlSnapshotFixture = `Card #43
	Name: bluez_card.AC_BF_71_11_22_33
	Driver: module-bluez5-device.c
	Properties:
		device.description = "JBL Flip 5"
		device.string = "AC:BF:71:11:22:33"
		device.api = "bluez"
		bluez.path = "/org/bluez/hci0/dev_AC_BF_71_11_22_33"
	Active Profile: a2dp-sink-sbc

Sink #52
	State: RUNNING
	Name: bluez_output.AC_BF_71_11_22_33.1
	Description: JBL Flip 5
	Driver: PipeWire
	Sample Specification: s16le 2ch 44100Hz
	Properties:
		bluetooth.codec = "sbc"
		api.bluez5.profile = "a2dp-sink"
		api.bluez5.address = "AC:BF:71:11:22:33"
`

// writeBluetoothSnapshot records the command output a Linux host with one
// connected speaker and one connected keyboard would produce.
func writeBluetoothSnapshot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"platform":      "linux\n",
		"dbus-send.txt": dbusSnapshotFixture,
		"hciconfig.txt": hciconfigFixture,
		"pactl.txt":     pactlSnapshotFixture,
		"hcitool_con.txt": "Connections:\n\t< ACL AC:BF:71:11:22:33 handle 12 state 1 lm MASTER AUTH ENCRYPT \n" +
			"\t< LE A0:B1:C2:D3:E4:F5 handle 64 state 1 lm MASTER ENCRYPT \n",
		"hcitool_lq.txt":   "Link quality: 245\n",
		"hcitool_tpl.txt":  "Current transmit power level: 4\n",
		"hcitool_rssi.txt": "RSSI return value: 0\n",
		"hcitool_afh.txt":  "AFH mode: enabled\nAFH map: 0xFFFFFFFFFFFFFFFFFF7F\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newBluetoothSnapshotCollector(t *testing.T) *bluetoothCollector {
	t.Helper()
	b := &bluetoothCollector{goos: "linux"}
	dir := writeBluetoothSnapshot(t)
	if err := b.Configure(func(v any) error {
		cfg, ok := v.(*bluetoothConfig)
		if !ok {
			t.Fatalf("unexpected config target %T", v)
		}
		cfg.SnapshotDir = dir
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBluetoothCollectorChartsFromSnapshot(t *testing.T) {
	b := newBluetoothSnapshotCollector(t)
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_760_000_000, 0)
	if err := b.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	ch, ok := reg.Chart("bluetooth.devices")
	if !ok {
		t.Fatal("missing bluetooth.devices")
	}
	_, vals := ch.LastValues()
	if vals["connected"] != 2 {
		t.Fatalf("connected devices = %v", vals)
	}
	byType, ok := reg.Chart("bluetooth.connected_by_type")
	if !ok {
		t.Fatal("missing bluetooth.connected_by_type")
	}
	_, typeVals := byType.LastValues()
	if typeVals["audio"] != 1 || typeVals["input"] != 1 {
		t.Fatalf("type buckets = %v", typeVals)
	}
	speaker := "AC_BF_71_11_22_33"
	rssi, ok := reg.Chart("bluetooth.device_rssi." + speaker)
	if !ok {
		t.Fatal("missing per-device RSSI chart")
	}
	if _, v := rssi.LastValues(); v["rssi"] != -47 {
		t.Fatalf("rssi = %v", v)
	}
	if rssi.Labels["device"] != "JBL Flip 5 (kitchen)" || rssi.Labels["device_type"] != "Loudspeaker" {
		t.Fatalf("labels = %v", rssi.Labels)
	}
	quality, ok := reg.Chart("bluetooth.device_link_quality." + speaker)
	if !ok {
		t.Fatal("missing link quality chart")
	}
	// hcitool reports 245 of 255; the chart publishes it as a percentage.
	if _, v := quality.LastValues(); math.Abs(v["quality"]-96.08) > 0.1 {
		t.Fatalf("link quality = %v", v)
	}
	bitrate, ok := reg.Chart("bluetooth.device_audio_bitrate." + speaker)
	if !ok {
		t.Fatal("missing audio bitrate chart")
	}
	if _, v := bitrate.LastValues(); math.Abs(v["bitrate"]-328) > 1 {
		t.Fatalf("bitrate = %v", v)
	}
	rate, ok := reg.Chart("bluetooth.device_audio_sample_rate." + speaker)
	if !ok {
		t.Fatal("missing sample rate chart")
	}
	if _, v := rate.LastValues(); v["sample_rate"] != 44.1 {
		t.Fatalf("sample rate = %v", v)
	}
	afh, ok := reg.Chart("bluetooth.device_afh_channels." + speaker)
	if !ok {
		t.Fatal("missing AFH chart")
	}
	if _, v := afh.LastValues(); v["channels"] != 79 {
		t.Fatalf("afh channels = %v", v)
	}
	traffic, ok := reg.Chart("bluetooth.adapter_traffic.hci0")
	if !ok {
		t.Fatal("missing controller traffic chart")
	}
	if traffic.Context != "bluetooth.adapter_traffic" {
		t.Fatalf("context = %q", traffic.Context)
	}
	if _, ok := reg.Chart("bluetooth.audio_streams"); !ok {
		t.Fatal("missing codec breakdown chart")
	}
}

func TestBluetoothFunctionReportsCodecAndLimits(t *testing.T) {
	b := newBluetoothSnapshotCollector(t)
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err != nil {
		t.Fatal(err)
	}
	fns := b.Functions()
	if len(fns) != 1 || fns[0].Name != "bluetooth-devices" {
		t.Fatalf("%+v", fns)
	}
	out, err := fns[0].Run(context.Background(), map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	report, ok := out.(BluetoothReport)
	if !ok {
		t.Fatalf("unexpected payload %T", out)
	}
	if report.Total != 2 || len(report.Rows) != 2 {
		t.Fatalf("rows = %d", report.Total)
	}
	var speaker BluetoothRow
	for _, row := range report.Rows {
		r := row.(BluetoothRow)
		if r.Address == "AC:BF:71:11:22:33" {
			speaker = r
		}
	}
	if speaker.Codec != "SBC" || speaker.BitrateKind != btBitrateNegotiated {
		t.Fatalf("codec=%q kind=%q", speaker.Codec, speaker.BitrateKind)
	}
	if speaker.Bitrate == nil || math.Abs(*speaker.Bitrate-328) > 1 {
		t.Fatalf("bitrate = %v", speaker.Bitrate)
	}
	if speaker.CodecMax == nil || *speaker.CodecMax != 452 {
		t.Fatalf("codec ceiling = %v", speaker.CodecMax)
	}
	if speaker.DeviceType != "Loudspeaker" || speaker.Transport != "BR/EDR" {
		t.Fatalf("type=%q transport=%q", speaker.DeviceType, speaker.Transport)
	}
	if speaker.LinkMode != "active" || speaker.Role != "central (master)" {
		t.Fatalf("link mode=%q role=%q", speaker.LinkMode, speaker.Role)
	}
	if speaker.Grade == "" || speaker.Score == nil || *speaker.Score <= 0 {
		t.Fatalf("score=%v grade=%q", speaker.Score, speaker.Grade)
	}
	if !strings.Contains(strings.Join(report.Sources, ","), "bluez-dbus") {
		t.Fatalf("sources = %v", report.Sources)
	}
	// The snapshot is replayed output, and that has to be stated rather than
	// presented as a live reading.
	if !strings.Contains(strings.Join(report.Limits, " "), "snapshot_dir") {
		t.Fatalf("limits = %v", report.Limits)
	}
	if len(report.Columns) != 25 {
		t.Fatalf("columns = %v", report.Columns)
	}
}

func TestBluetoothCollectorDisablesWithoutRadio(t *testing.T) {
	b := &bluetoothCollector{goos: "linux"}
	if err := b.Configure(func(any) error { return nil }); err != nil {
		t.Fatal(err)
	}
	b.run = func(context.Context, string, ...string) ([]byte, error) { return nil, os.ErrNotExist }
	reg := registry.New(&registry.Host{Hostname: "t", UpdateEvery: 1}, nil)
	if err := b.Init(reg); err == nil {
		t.Fatal("a host without a controller must disable the collector")
	}
}

func TestBluetoothScoreWeighting(t *testing.T) {
	strong := -45.0
	quality := 255.0
	channels := 79.0
	audio := &btAudioStream{Codec: "LDAC", Bitrate: 990, BitrateKind: btBitrateNegotiated}
	score, findings := btScore(BluetoothDevice{RSSI: &strong, RSSIKind: "absolute",
		LinkQuality: &quality, AFHChannels: &channels, Audio: audio})
	if score < 95 {
		t.Fatalf("an ideal link scored %.1f, want >= 95 (%v)", score, findings)
	}
	weak := -88.0
	poorQuality := 120.0
	fewChannels := 20.0
	sbc := &btAudioStream{Codec: "SBC", Bitrate: 200, BitrateKind: btBitrateNegotiated}
	bad, badFindings := btScore(BluetoothDevice{RSSI: &weak, RSSIKind: "absolute",
		LinkQuality: &poorQuality, AFHChannels: &fewChannels, Audio: sbc})
	if bad > 40 {
		t.Fatalf("a degraded link scored %.1f, want <= 40", bad)
	}
	if len(badFindings) < 3 {
		t.Fatalf("degraded link findings = %v", badFindings)
	}
	if btGrade(bad) != "poor" || btGrade(score) != "excellent" {
		t.Fatalf("grades %q / %q", btGrade(bad), btGrade(score))
	}
	// A BR/EDR golden-range reading is not dBm and must not drive the score.
	golden := 0.0
	partial, notes := btScore(BluetoothDevice{RSSI: &golden, RSSIKind: "golden-range", LinkQuality: &quality})
	if partial != 100 {
		t.Fatalf("golden-range RSSI must be excluded, score = %.1f", partial)
	}
	if len(notes) != 0 {
		t.Fatalf("unexpected findings %v", notes)
	}
	if empty, notes := btScore(BluetoothDevice{}); empty != 0 || len(notes) != 1 {
		t.Fatalf("score=%v notes=%v", empty, notes)
	}
}

func TestBluetoothDeviceFilters(t *testing.T) {
	b := &bluetoothCollector{}
	if err := b.Configure(func(v any) error {
		cfg := v.(*bluetoothConfig)
		cfg.Exclude = []string{"*keyboard*"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if b.wanted(BluetoothDevice{Name: "Keychron Keyboard"}) {
		t.Fatal("excluded device accepted")
	}
	if !b.wanted(BluetoothDevice{Name: "JBL Flip 5"}) {
		t.Fatal("unrelated device rejected")
	}
}

func TestBluetoothAirRateFromCounters(t *testing.T) {
	b := &bluetoothCollector{counters: map[string]btCounters{}}
	snap := btSnapshot{Adapters: []BluetoothAdapter{{ID: "hci0", HasStatistics: true, RxBytes: 1000, TxBytes: 2000}}}
	b.measureAirRate(&snap)
	if snap.Adapters[0].AirTxKbps != nil {
		t.Fatal("the first sample has no window to measure over")
	}
	b.counters["hci0"] = btCounters{at: time.Now().Add(-time.Second), rxBytes: 1000, txBytes: 2000, seen: true}
	next := btSnapshot{Adapters: []BluetoothAdapter{{ID: "hci0", HasStatistics: true, RxBytes: 2000, TxBytes: 43000}}}
	b.measureAirRate(&next)
	if next.Adapters[0].AirTxKbps == nil || *next.Adapters[0].AirTxKbps < 300 {
		t.Fatalf("air tx = %v kbps, want roughly 328", next.Adapters[0].AirTxKbps)
	}
	// A controller reset rewinds the counters; a negative delta must be
	// dropped instead of reported as a huge rate.
	b.counters["hci0"] = btCounters{at: time.Now().Add(-time.Second), rxBytes: 9000, txBytes: 9000, seen: true}
	reset := btSnapshot{Adapters: []BluetoothAdapter{{ID: "hci0", HasStatistics: true, RxBytes: 10, TxBytes: 10}}}
	b.measureAirRate(&reset)
	if reset.Adapters[0].AirTxKbps != nil {
		t.Fatalf("counter reset produced %v", *reset.Adapters[0].AirTxKbps)
	}
}
