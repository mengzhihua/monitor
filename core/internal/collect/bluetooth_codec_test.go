package collect

import (
	"math"
	"testing"
)

func TestBluetoothSBCBitrate(t *testing.T) {
	// The reference A2DP configuration (44.1 kHz, joint stereo, 16 blocks,
	// 8 subbands, bitpool 53) is the 328 kbps figure every Bluetooth audio
	// tester quotes for SBC.
	got := btSBCBitrate(44100, "joint stereo", 16, 8, 53)
	if math.Abs(got-328) > 1 {
		t.Fatalf("joint stereo bitpool 53 = %.2f kbps, want ~328", got)
	}
	// "SBC XQ" raises the bitpool; dual channel at bitpool 38 is the widely
	// published 452 kbps variant at 44.1 kHz.
	if xq := btSBCBitrate(44100, "dual channel", 16, 8, 38); math.Abs(xq-452) > 3 {
		t.Fatalf("dual channel bitpool 38 = %.2f kbps, want ~452", xq)
	}
	if mono := btSBCBitrate(16000, "mono", 15, 8, 26); mono <= 0 || mono > 100 {
		t.Fatalf("mSBC-like configuration = %.2f kbps, want a positive narrow-band rate", mono)
	}
	if btSBCBitrate(0, "stereo", 16, 8, 53) != 0 || btSBCBitrate(44100, "stereo", 16, 8, 0) != 0 {
		t.Fatal("incomplete configuration must not produce a bitrate")
	}
}

func TestBluetoothDecodeSBCConfiguration(t *testing.T) {
	s := btDecodeA2DPConfig(a2dpCodecSBC, []byte{0x21, 0x15, 0x02, 0x35})
	if s.Codec != "SBC" || s.SampleRate != 44100 || s.ChannelMode != "joint stereo" {
		t.Fatalf("codec=%q rate=%v channels=%q", s.Codec, s.SampleRate, s.ChannelMode)
	}
	if s.Blocks != 16 || s.Subbands != 8 || s.Allocation != "loudness" {
		t.Fatalf("blocks=%d subbands=%d allocation=%q", s.Blocks, s.Subbands, s.Allocation)
	}
	if s.MinBitpool != 2 || s.MaxBitpool != 53 {
		t.Fatalf("bitpool %d..%d", s.MinBitpool, s.MaxBitpool)
	}
	if s.BitrateKind != btBitrateNegotiated || math.Abs(s.Bitrate-328) > 1 {
		t.Fatalf("bitrate %.2f (%s), want ~328 negotiated", s.Bitrate, s.BitrateKind)
	}
	// A bitpool above the classic 53 is what distinguishes SBC XQ.
	if xq := btDecodeA2DPConfig(a2dpCodecSBC, []byte{0x21, 0x15, 0x02, 0x4C}); xq.Codec != "SBC XQ" {
		t.Fatalf("bitpool 76 reported as %q, want SBC XQ", xq.Codec)
	}
	if short := btDecodeA2DPConfig(a2dpCodecSBC, []byte{0x21}); short.BitrateKind != btBitrateNominal {
		t.Fatalf("truncated configuration must fall back to the nominal rate, got %q", short.BitrateKind)
	}
}

func TestBluetoothDecodeAACConfiguration(t *testing.T) {
	// MPEG-2 AAC LC, 44.1 kHz, stereo, VBR at 264600 bps.
	s := btDecodeA2DPConfig(a2dpCodecAAC, []byte{0x80, 0x01, 0x04, 0x84, 0x09, 0x98})
	if s.Codec != "AAC" || s.SampleRate != 44100 || s.ChannelMode != "stereo" {
		t.Fatalf("codec=%q rate=%v channels=%q", s.Codec, s.SampleRate, s.ChannelMode)
	}
	if !s.VBR {
		t.Fatal("VBR flag not decoded")
	}
	if math.Abs(s.Bitrate-264.6) > 0.1 || s.BitrateKind != btBitrateNegotiated {
		t.Fatalf("bitrate %.2f (%s), want 264.6 negotiated", s.Bitrate, s.BitrateKind)
	}
}

func TestBluetoothDecodeVendorCodecs(t *testing.T) {
	// LDAC: Sony vendor id 0x0000012D, codec id 0x00AA, 96 kHz stereo.
	ldac := btDecodeA2DPConfig(a2dpCodecVendor, []byte{0x2D, 0x01, 0x00, 0x00, 0xAA, 0x00, 0x04, 0x01})
	if ldac.Codec != "LDAC" || ldac.Vendor != "Sony" {
		t.Fatalf("codec=%q vendor=%q", ldac.Codec, ldac.Vendor)
	}
	if ldac.SampleRate != 96000 || ldac.ChannelMode != "stereo" || ldac.BitDepth != 24 {
		t.Fatalf("rate=%v channels=%q depth=%d", ldac.SampleRate, ldac.ChannelMode, ldac.BitDepth)
	}
	if ldac.BitrateKind != btBitrateNominal {
		t.Fatalf("LDAC carries no rate in its configuration, want nominal, got %q", ldac.BitrateKind)
	}
	// aptX HD: Qualcomm vendor id 0x000000D7, codec id 0x0024, 48 kHz stereo
	// is the published 576 kbps.
	hd := btDecodeA2DPConfig(a2dpCodecVendor, []byte{0xD7, 0x00, 0x00, 0x00, 0x24, 0x00, 0x12})
	if hd.Codec != "aptX HD" || math.Abs(hd.Bitrate-576) > 0.5 || hd.BitrateKind != btBitrateNegotiated {
		t.Fatalf("codec=%q bitrate=%.2f (%s)", hd.Codec, hd.Bitrate, hd.BitrateKind)
	}
	unknown := btDecodeA2DPConfig(a2dpCodecVendor, []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06})
	if unknown.Codec != "Vendor 0x04030201:0x0605" {
		t.Fatalf("unknown vendor codec reported as %q", unknown.Codec)
	}
}

func TestBluetoothLDACQualityIndex(t *testing.T) {
	cases := []struct {
		index int64
		rate  float64
		kbps  float64
	}{{1000, 96000, 990}, {1001, 96000, 660}, {1002, 96000, 330}, {1000, 44100, 909}, {1003, 96000, 0}}
	for _, c := range cases {
		kbps, mode := btLDACBitrate(c.index, c.rate)
		if kbps != c.kbps || mode == "" {
			t.Fatalf("EQMID %d at %.0f Hz = %.0f kbps (%q), want %.0f", c.index, c.rate, kbps, mode, c.kbps)
		}
	}
	if _, mode := btLDACBitrate(7, 48000); mode != "" {
		t.Fatalf("unknown quality index must not be named, got %q", mode)
	}
}

func TestBluetoothCanonicalCodecNames(t *testing.T) {
	cases := map[string]string{
		"ldac": "LDAC", "aptx_hd": "aptX HD", "a2dp-sink-aptx_adaptive": "aptX Adaptive",
		"sbc_xq": "SBC XQ", "msbc": "mSBC", "LDAC": "LDAC", "lc3": "LC3", "": "",
	}
	for in, want := range cases {
		if got := btCanonicalCodec(in); got != want {
			t.Fatalf("btCanonicalCodec(%q) = %q, want %q", in, got, want)
		}
	}
	if got := btCanonicalCodec("some-new-codec"); got != "SOME-NEW-CODEC" {
		t.Fatalf("unknown codec must be kept, got %q", got)
	}
}

func TestBluetoothClassOfDeviceDecoding(t *testing.T) {
	// 0x240414 is the class a portable Bluetooth speaker reports.
	major, minor, services := btDecodeClass(0x240414)
	if major != "Audio/Video" || minor != "Loudspeaker" {
		t.Fatalf("major=%q minor=%q", major, minor)
	}
	if len(services) != 2 || services[0] != "Rendering" || services[1] != "Audio" {
		t.Fatalf("services=%v", services)
	}
	if _, minor, _ := btDecodeClass(0x000540); minor != "Keyboard" {
		t.Fatalf("keyboard minor class = %q", minor)
	}
	if _, minor, _ := btDecodeClass(0x240404); minor != "Wearable Headset" {
		t.Fatalf("headset minor class = %q", minor)
	}
	if v, ok := btParseClass("0x04 0x01 0x240414"); !ok || v != 0x240414 {
		t.Fatalf("macOS class form parsed as 0x%X (ok=%v)", v, ok)
	}
}

func TestBluetoothAFHChannelCount(t *testing.T) {
	// A full map marks all 79 hopping channels usable.
	if n, ok := btAFHChannels("0xFFFFFFFFFFFFFFFFFF7F"); !ok || n != 79 {
		t.Fatalf("full AFH map = %v (ok=%v), want 79", n, ok)
	}
	// Wi-Fi interference removes channels from the map.
	if n, ok := btAFHChannels("0x00000000FFFFFFFFFF7F"); !ok || n != 47 {
		t.Fatalf("partial AFH map = %v (ok=%v), want 47", n, ok)
	}
	if _, ok := btAFHChannels("zz"); ok {
		t.Fatal("malformed AFH map must be rejected")
	}
}

func TestBluetoothProfileUUIDs(t *testing.T) {
	if got := btProfileForUUID("0000110b-0000-1000-8000-00805f9b34fb"); got != "Audio Sink (A2DP)" {
		t.Fatalf("A2DP sink UUID = %q", got)
	}
	if got := btProfileForUUID("f8d1fbe4-7966-4334-8024-ff96c9330e15"); got != "" {
		t.Fatalf("vendor UUID must not be named a profile, got %q", got)
	}
	if got := btTransportForUUIDs([]string{"Audio Sink (A2DP)", "AVRCP Target"}, "public"); got != "BR/EDR" {
		t.Fatalf("classic audio transport = %q", got)
	}
	if got := btTransportForUUIDs([]string{"HID over GATT (HOGP)"}, "random"); got != "LE" {
		t.Fatalf("GATT-only transport = %q", got)
	}
}
