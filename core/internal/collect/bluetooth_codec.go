package collect

import (
	"fmt"
	"math"
	"strings"
)

// A2DP media codec type values (Bluetooth Assigned Numbers, Audio/Video).
const (
	a2dpCodecSBC    = 0x00
	a2dpCodecMPEG12 = 0x01
	a2dpCodecAAC    = 0x02
	a2dpCodecATRAC  = 0x04
	a2dpCodecVendor = 0xFF
)

// Bitrate provenance. Only "negotiated" comes from the codec configuration the
// two endpoints actually agreed on; "nominal" is this table's typical value for
// the codec and "measured" is derived from controller byte counters.
const (
	btBitrateNegotiated = "negotiated"
	btBitrateNominal    = "nominal"
	btBitrateMeasured   = "measured"
)

// btCodecProfile is the reference data industry Bluetooth audio testers show
// next to a stream: the transport profile the codec belongs to, its typical
// operating bitrate and the ceiling it can reach. Values are the codec vendors'
// published figures for stereo at the codec's headline sample rate; a live
// stream is reported from its negotiated configuration whenever the host stack
// exposes one, and falls back to Typical only when it does not.
type btCodecProfile struct {
	Name       string
	Profile    string // A2DP, HFP, LE Audio
	Typical    float64
	Max        float64
	SampleRate float64
	BitDepth   int
	Note       string
}

// btCodecProfiles is keyed by canonical codec name.
var btCodecProfiles = map[string]btCodecProfile{
	"SBC":              {Name: "SBC", Profile: "A2DP", Typical: 328, Max: 452, SampleRate: 44100, BitDepth: 16, Note: "A2DP 强制编解码器，联合立体声 bitpool 53 约 328 kbps"},
	"SBC XQ":           {Name: "SBC XQ", Profile: "A2DP", Typical: 452, Max: 551, SampleRate: 44100, BitDepth: 16, Note: "提高 bitpool 的 SBC，非标准配置，需双方支持"},
	"AAC":              {Name: "AAC", Profile: "A2DP", Typical: 256, Max: 320, SampleRate: 44100, BitDepth: 16, Note: "MPEG-2/4 AAC-LC，苹果设备常用 256 kbps VBR"},
	"MPEG-1,2 Audio":   {Name: "MPEG-1,2 Audio", Profile: "A2DP", Typical: 192, Max: 320, SampleRate: 44100, BitDepth: 16, Note: "MP3 over A2DP，实际设备少见"},
	"ATRAC":            {Name: "ATRAC", Profile: "A2DP", Typical: 132, Max: 352, SampleRate: 44100, BitDepth: 16, Note: "索尼 ATRAC，历史编解码器"},
	"aptX":             {Name: "aptX", Profile: "A2DP", Typical: 352, Max: 384, SampleRate: 44100, BitDepth: 16, Note: "固定 4:1 压缩，44.1 kHz 约 352 kbps"},
	"aptX HD":          {Name: "aptX HD", Profile: "A2DP", Typical: 576, Max: 576, SampleRate: 48000, BitDepth: 24, Note: "48 kHz / 24 bit 固定 576 kbps"},
	"aptX Low Latency": {Name: "aptX Low Latency", Profile: "A2DP", Typical: 352, Max: 384, SampleRate: 44100, BitDepth: 16, Note: "与 aptX 同码率，目标端到端延迟约 40 ms"},
	"aptX Adaptive":    {Name: "aptX Adaptive", Profile: "A2DP", Typical: 420, Max: 620, SampleRate: 48000, BitDepth: 24, Note: "279–420 kbps 自适应，部分设备可达 620 kbps"},
	"LDAC":             {Name: "LDAC", Profile: "A2DP", Typical: 660, Max: 990, SampleRate: 96000, BitDepth: 24, Note: "330 / 660 / 990 kbps 三档加自适应（ABR）"},
	"LHDC":             {Name: "LHDC", Profile: "A2DP", Typical: 560, Max: 900, SampleRate: 96000, BitDepth: 24, Note: "400 / 560 / 900 kbps 档位"},
	"LHDC V5":          {Name: "LHDC V5", Profile: "A2DP", Typical: 600, Max: 1000, SampleRate: 192000, BitDepth: 24, Note: "最高约 1 Mbps"},
	"LC3plus HR":       {Name: "LC3plus HR", Profile: "A2DP", Typical: 400, Max: 800, SampleRate: 96000, BitDepth: 24, Note: "Fraunhofer LC3plus 高解析模式"},
	"Samsung Scalable": {Name: "Samsung Scalable", Profile: "A2DP", Typical: 512, Max: 512, SampleRate: 48000, BitDepth: 24, Note: "三星自适应 88–512 kbps"},
	"Samsung HD":       {Name: "Samsung HD", Profile: "A2DP", Typical: 384, Max: 384, SampleRate: 48000, BitDepth: 24, Note: "三星 UHQ 之前的高码率编解码器"},
	"FastStream":       {Name: "FastStream", Profile: "A2DP", Typical: 212, Max: 212, SampleRate: 44100, BitDepth: 16, Note: "双向 SBC 变体，常用于低延迟对讲"},
	"Opus":             {Name: "Opus", Profile: "A2DP", Typical: 256, Max: 510, SampleRate: 48000, BitDepth: 16, Note: "PipeWire 自定义 A2DP 承载，非蓝牙标准编解码器"},
	"CVSD":             {Name: "CVSD", Profile: "HFP", Typical: 64, Max: 64, SampleRate: 8000, BitDepth: 16, Note: "HFP 窄带语音，8 kHz 单声道"},
	"mSBC":             {Name: "mSBC", Profile: "HFP", Typical: 64, Max: 64, SampleRate: 16000, BitDepth: 16, Note: "HFP 宽带语音（WBS），16 kHz 单声道"},
	"LC3-SWB":          {Name: "LC3-SWB", Profile: "HFP", Typical: 64, Max: 64, SampleRate: 32000, BitDepth: 16, Note: "HFP 1.9 超宽带语音，32 kHz 单声道"},
	"LC3":              {Name: "LC3", Profile: "LE Audio", Typical: 160, Max: 320, SampleRate: 48000, BitDepth: 24, Note: "LE Audio 强制编解码器，48 kHz 立体声约 160 kbps"},
}

// btCodecAliases maps the codec tokens host stacks print (PipeWire /
// PulseAudio profile and property names, Android codecName, BlueZ) onto the
// canonical names used in btCodecProfiles.
var btCodecAliases = map[string]string{
	"sbc":                "SBC",
	"sbc_xq":             "SBC XQ",
	"sbcxq":              "SBC XQ",
	"sbc-xq":             "SBC XQ",
	"aac":                "AAC",
	"mpeg":               "MPEG-1,2 Audio",
	"mp3":                "MPEG-1,2 Audio",
	"atrac":              "ATRAC",
	"aptx":               "aptX",
	"aptx_hd":            "aptX HD",
	"aptxhd":             "aptX HD",
	"aptx-hd":            "aptX HD",
	"aptx_ll":            "aptX Low Latency",
	"aptx_ll_duplex":     "aptX Low Latency",
	"aptxll":             "aptX Low Latency",
	"aptx_adaptive":      "aptX Adaptive",
	"aptxadaptive":       "aptX Adaptive",
	"aptx_adaptive_sink": "aptX Adaptive",
	"ldac":               "LDAC",
	"lhdc":               "LHDC",
	"lhdc_v1":            "LHDC",
	"lhdc_v2":            "LHDC",
	"lhdc_v3":            "LHDC",
	"lhdc_v5":            "LHDC V5",
	"lc3plus_hr":         "LC3plus HR",
	"lc3plus":            "LC3plus HR",
	"lc3":                "LC3",
	"lc3_swb":            "LC3-SWB",
	"lc3swb":             "LC3-SWB",
	"opus":               "Opus",
	"opus_05":            "Opus",
	"opus_05_duplex":     "Opus",
	"opus_05_pro":        "Opus",
	"cvsd":               "CVSD",
	"msbc":               "mSBC",
	"faststream":         "FastStream",
	"faststream_duplex":  "FastStream",
	"samsung_scalable":   "Samsung Scalable",
	"ssc":                "Samsung Scalable",
	"samsung_hd":         "Samsung HD",
}

// btCanonicalCodec resolves a stack-specific codec token. Unknown tokens are
// returned upper-cased rather than dropped, so an unfamiliar vendor codec is
// still reported instead of silently becoming "unknown".
func btCanonicalCodec(token string) string {
	t := strings.TrimSpace(token)
	if t == "" {
		return ""
	}
	key := strings.ToLower(t)
	key = strings.TrimPrefix(key, "a2dp-sink-")
	key = strings.TrimPrefix(key, "a2dp-source-")
	key = strings.TrimPrefix(key, "headset-head-unit-")
	key = strings.ReplaceAll(key, " ", "_")
	if name, ok := btCodecAliases[key]; ok {
		return name
	}
	if _, ok := btCodecProfiles[t]; ok {
		return t
	}
	return strings.ToUpper(t)
}

// btVendorCodec is one entry of the vendor-specific A2DP codec registry
// (codec type 0xFF carries a 4-byte vendor ID plus a 2-byte vendor codec ID).
type btVendorCodec struct {
	VendorID uint32
	CodecID  uint16
	Name     string
	Vendor   string
}

var btVendorCodecs = []btVendorCodec{
	{0x0000004F, 0x0001, "aptX", "APT Licensing"},
	{0x000000D7, 0x0024, "aptX HD", "Qualcomm"},
	{0x000000D7, 0x00AD, "aptX Adaptive", "Qualcomm"},
	{0x0000000A, 0x0001, "FastStream", "Cambridge Silicon Radio"},
	{0x0000000A, 0x0002, "aptX Low Latency", "Cambridge Silicon Radio"},
	{0x0000012D, 0x00AA, "LDAC", "Sony"},
	{0x0000053A, 0x4C32, "LHDC", "Savitech"},
	{0x0000053A, 0x4C33, "LHDC", "Savitech"},
	{0x0000053A, 0x4C35, "LHDC V5", "Savitech"},
	{0x000008A9, 0x0001, "LC3plus HR", "Fraunhofer IIS"},
	{0x00000075, 0x0102, "Samsung HD", "Samsung"},
	{0x00000075, 0x0103, "Samsung Scalable", "Samsung"},
}

func btLookupVendorCodec(vendor uint32, codec uint16) (btVendorCodec, bool) {
	for _, v := range btVendorCodecs {
		if v.VendorID == vendor && v.CodecID == codec {
			return v, true
		}
	}
	return btVendorCodec{}, false
}

// btAudioStream is the decoded state of one audio link: which codec is in use,
// how it is parameterised and how fast it is pushing data over the air.
type btAudioStream struct {
	Codec       string   `json:"codec"`
	CodecID     string   `json:"codec_id,omitempty"`
	Vendor      string   `json:"codec_vendor,omitempty"`
	Profile     string   `json:"profile,omitempty"`
	SampleRate  float64  `json:"sample_rate_hz,omitempty"`
	BitDepth    int      `json:"bit_depth,omitempty"`
	ChannelMode string   `json:"channel_mode,omitempty"`
	Bitrate     float64  `json:"bitrate_kbps,omitempty"`
	BitrateKind string   `json:"bitrate_kind,omitempty"`
	MinBitpool  int      `json:"sbc_min_bitpool,omitempty"`
	MaxBitpool  int      `json:"sbc_max_bitpool,omitempty"`
	Blocks      int      `json:"sbc_blocks,omitempty"`
	Subbands    int      `json:"sbc_subbands,omitempty"`
	Allocation  string   `json:"sbc_allocation,omitempty"`
	VBR         bool     `json:"vbr,omitempty"`
	QualityMode string   `json:"codec_quality_mode,omitempty"`
	State       string   `json:"transport_state,omitempty"`
	DelayMs     float64  `json:"transport_delay_ms,omitempty"`
	Volume      float64  `json:"volume_percent,omitempty"`
	HasVolume   bool     `json:"-"`
	Notes       []string `json:"notes,omitempty"`
}

// btDecodeA2DPConfig turns the raw A2DP Media Codec Specific Information
// Element (BlueZ MediaTransport1.Configuration) into a stream description.
// Capabilities advertise every supported option; a configuration has exactly
// one bit set per field, so a multi-bit field means the caller handed us
// capabilities and the widest option is reported.
func btDecodeA2DPConfig(codecType byte, cfg []byte) btAudioStream {
	s := btAudioStream{Profile: "A2DP", CodecID: fmt.Sprintf("0x%02X", codecType)}
	switch codecType {
	case a2dpCodecSBC:
		s.Codec = "SBC"
		btDecodeSBC(&s, cfg)
	case a2dpCodecAAC:
		s.Codec = "AAC"
		btDecodeAAC(&s, cfg)
	case a2dpCodecMPEG12:
		s.Codec = "MPEG-1,2 Audio"
	case a2dpCodecATRAC:
		s.Codec = "ATRAC"
	case a2dpCodecVendor:
		btDecodeVendor(&s, cfg)
	default:
		s.Codec = fmt.Sprintf("Unknown (0x%02X)", codecType)
	}
	if s.Bitrate == 0 {
		btApplyNominalBitrate(&s)
	}
	return s
}

var btSBCFrequencies = []struct {
	mask byte
	hz   float64
}{{0x80, 16000}, {0x40, 32000}, {0x20, 44100}, {0x10, 48000}}

var btSBCChannelModes = []struct {
	mask byte
	name string
}{{0x08, "mono"}, {0x04, "dual channel"}, {0x02, "stereo"}, {0x01, "joint stereo"}}

func btDecodeSBC(s *btAudioStream, cfg []byte) {
	if len(cfg) < 4 {
		s.Notes = append(s.Notes, "SBC 配置长度不足，无法解析")
		return
	}
	for _, f := range btSBCFrequencies {
		if cfg[0]&f.mask != 0 {
			s.SampleRate = f.hz
		}
	}
	for _, c := range btSBCChannelModes {
		if cfg[0]&c.mask != 0 {
			s.ChannelMode = c.name
		}
	}
	switch {
	case cfg[1]&0x10 != 0:
		s.Blocks = 16
	case cfg[1]&0x20 != 0:
		s.Blocks = 12
	case cfg[1]&0x40 != 0:
		s.Blocks = 8
	case cfg[1]&0x80 != 0:
		s.Blocks = 4
	}
	switch {
	case cfg[1]&0x04 != 0:
		s.Subbands = 8
	case cfg[1]&0x08 != 0:
		s.Subbands = 4
	}
	switch {
	case cfg[1]&0x01 != 0:
		s.Allocation = "loudness"
	case cfg[1]&0x02 != 0:
		s.Allocation = "SNR"
	}
	s.MinBitpool, s.MaxBitpool = int(cfg[2]), int(cfg[3])
	s.BitDepth = 16
	if s.MaxBitpool > 53 && s.ChannelMode != "mono" {
		s.Codec = "SBC XQ"
	}
	if kbps := btSBCBitrate(s.SampleRate, s.ChannelMode, s.Blocks, s.Subbands, s.MaxBitpool); kbps > 0 {
		s.Bitrate = kbps
		s.BitrateKind = btBitrateNegotiated
	}
}

// btSBCBitrate implements the frame-length formula from the A2DP specification
// (section 12.9, "Calculation of Bit Rate and Frame Length"). This is the same
// arithmetic bitpool-aware testers use to turn an SBC configuration into kbps.
func btSBCBitrate(sampleRate float64, channelMode string, blocks, subbands, bitpool int) float64 {
	if sampleRate <= 0 || blocks <= 0 || subbands <= 0 || bitpool <= 0 {
		return 0
	}
	channels := 2
	if channelMode == "mono" {
		channels = 1
	}
	header := 4 + (4*subbands*channels)/8
	var payload int
	switch channelMode {
	case "mono", "dual channel":
		payload = int(math.Ceil(float64(blocks*channels*bitpool) / 8))
	case "joint stereo":
		payload = int(math.Ceil(float64(subbands+blocks*bitpool) / 8))
	default: // stereo
		payload = int(math.Ceil(float64(blocks*bitpool) / 8))
	}
	frame := float64(header + payload)
	return 8 * frame * sampleRate / float64(subbands*blocks) / 1000
}

// btAACFrequencies is the 12-bit sampling frequency field, MSB first.
var btAACFrequencies = []float64{8000, 11025, 12000, 16000, 22050, 24000, 32000, 44100, 48000, 64000, 88200, 96000}

func btDecodeAAC(s *btAudioStream, cfg []byte) {
	if len(cfg) < 6 {
		s.Notes = append(s.Notes, "AAC 配置长度不足，无法解析")
		return
	}
	freqBits := uint16(cfg[1])<<4 | uint16(cfg[2])>>4
	for i, hz := range btAACFrequencies {
		if freqBits&(1<<(11-uint(i))) != 0 {
			s.SampleRate = hz
		}
	}
	switch {
	case cfg[2]&0x04 != 0:
		s.ChannelMode = "stereo"
	case cfg[2]&0x08 != 0:
		s.ChannelMode = "mono"
	}
	s.BitDepth = 16
	s.VBR = cfg[3]&0x80 != 0
	bits := uint32(cfg[3]&0x7F)<<16 | uint32(cfg[4])<<8 | uint32(cfg[5])
	if bits > 0 {
		s.Bitrate = float64(bits) / 1000
		s.BitrateKind = btBitrateNegotiated
	}
	switch {
	case cfg[0]&0x80 != 0:
		s.Notes = append(s.Notes, "MPEG-2 AAC LC")
	case cfg[0]&0x40 != 0:
		s.Notes = append(s.Notes, "MPEG-4 AAC LC")
	case cfg[0]&0x20 != 0:
		s.Notes = append(s.Notes, "MPEG-4 AAC LTP")
	case cfg[0]&0x10 != 0:
		s.Notes = append(s.Notes, "MPEG-4 AAC Scalable")
	}
}

var btLDACFrequencies = []struct {
	mask byte
	hz   float64
}{{0x20, 44100}, {0x10, 48000}, {0x08, 88200}, {0x04, 96000}, {0x02, 176400}, {0x01, 192000}}

var btAptXFrequencies = []struct {
	mask byte
	hz   float64
}{{0x80, 16000}, {0x40, 32000}, {0x20, 44100}, {0x10, 48000}}

func btDecodeVendor(s *btAudioStream, cfg []byte) {
	if len(cfg) < 6 {
		s.Codec = "Vendor specific"
		s.Notes = append(s.Notes, "厂商编解码器配置长度不足，无法识别")
		return
	}
	// Vendor ID is little-endian per the A2DP vendor-specific codec format.
	vendorID := uint32(cfg[0]) | uint32(cfg[1])<<8 | uint32(cfg[2])<<16 | uint32(cfg[3])<<24
	codecID := uint16(cfg[4]) | uint16(cfg[5])<<8
	s.CodecID = fmt.Sprintf("0xFF vendor 0x%08X:0x%04X", vendorID, codecID)
	known, ok := btLookupVendorCodec(vendorID, codecID)
	if !ok {
		s.Codec = fmt.Sprintf("Vendor 0x%08X:0x%04X", vendorID, codecID)
		return
	}
	s.Codec, s.Vendor = known.Name, known.Vendor
	payload := cfg[6:]
	switch known.Name {
	case "LDAC":
		if len(payload) >= 2 {
			for _, f := range btLDACFrequencies {
				if payload[0]&f.mask != 0 {
					s.SampleRate = f.hz
				}
			}
			switch {
			case payload[1]&0x04 != 0:
				s.ChannelMode = "mono"
			case payload[1]&0x02 != 0:
				s.ChannelMode = "dual channel"
			case payload[1]&0x01 != 0:
				s.ChannelMode = "stereo"
			}
			s.BitDepth = 24
		}
	case "aptX", "aptX HD", "aptX Low Latency", "aptX Adaptive":
		if len(payload) >= 1 {
			for _, f := range btAptXFrequencies {
				if payload[0]&f.mask != 0 {
					s.SampleRate = f.hz
				}
			}
			for _, c := range btSBCChannelModes {
				if payload[0]&c.mask != 0 {
					s.ChannelMode = c.name
				}
			}
		}
		s.BitDepth = 16
		if known.Name == "aptX HD" || known.Name == "aptX Adaptive" {
			s.BitDepth = 24
		}
		if kbps := btAptXBitrate(known.Name, s.SampleRate, s.ChannelMode); kbps > 0 {
			s.Bitrate, s.BitrateKind = kbps, btBitrateNegotiated
		}
	}
}

// btAptXBitrate derives aptX family rates from the fixed compression ratio
// (4:1 on 16-bit samples, 48 kHz/24-bit at 576 kbps for aptX HD).
func btAptXBitrate(codec string, sampleRate float64, channelMode string) float64 {
	if sampleRate <= 0 {
		return 0
	}
	channels := 2.0
	if channelMode == "mono" {
		channels = 1
	}
	switch codec {
	case "aptX", "aptX Low Latency":
		return sampleRate * 16 * channels / 4 / 1000
	case "aptX HD":
		return sampleRate * 24 * channels / 4 / 1000
	}
	return 0
}

// btLDACBitrate maps the LDAC encode quality index (EQMID) onto the published
// rates. Android reports the index in mCodecSpecific1.
func btLDACBitrate(qualityIndex int64, sampleRate float64) (float64, string) {
	base44 := sampleRate == 44100 || sampleRate == 88200 || sampleRate == 176400
	switch qualityIndex {
	case 1000:
		if base44 {
			return 909, "high (909 kbps)"
		}
		return 990, "high (990 kbps)"
	case 1001:
		if base44 {
			return 606, "standard (606 kbps)"
		}
		return 660, "standard (660 kbps)"
	case 1002:
		if base44 {
			return 303, "mobile (303 kbps)"
		}
		return 330, "mobile (330 kbps)"
	case 1003:
		return 0, "adaptive (ABR)"
	}
	return 0, ""
}

// btApplyNominalBitrate fills in the reference rate for codecs whose live
// configuration does not carry one, and marks the value as nominal so readers
// never mistake it for a measurement.
func btApplyNominalBitrate(s *btAudioStream) {
	ref, ok := btCodecProfiles[s.Codec]
	if !ok {
		return
	}
	if s.Profile == "" {
		s.Profile = ref.Profile
	}
	if s.SampleRate == 0 {
		s.SampleRate = ref.SampleRate
	}
	if s.BitDepth == 0 {
		s.BitDepth = ref.BitDepth
	}
	s.Bitrate = ref.Typical
	s.BitrateKind = btBitrateNominal
}

// btCodecCeiling is the codec's published maximum rate, used to show how much
// of the codec's capability the current configuration is using.
func btCodecCeiling(codec string) float64 {
	if ref, ok := btCodecProfiles[codec]; ok {
		return ref.Max
	}
	return 0
}
