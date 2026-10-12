package collect

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

// bluetoothConfig is the `collectors.modules.bluetooth` section. Every probe
// is optional: the collector uses whatever the host provides and records why
// the rest is missing instead of reporting zeros.
type bluetoothConfig struct {
	Timeout        time.Duration `yaml:"timeout"`
	Every          time.Duration `yaml:"every"`
	Bluetoothctl   string        `yaml:"bluetoothctl"`
	Hcitool        string        `yaml:"hcitool"`
	Hciconfig      string        `yaml:"hciconfig"`
	Pactl          string        `yaml:"pactl"`
	DBusSend       string        `yaml:"dbus_send"`
	SystemProfiler string        `yaml:"system_profiler"`
	PowerShell     string        `yaml:"powershell"`
	Dumpsys        string        `yaml:"dumpsys"`
	SnapshotDir    string        `yaml:"snapshot_dir"`
	LinkProbe      *bool         `yaml:"link_probe"`
	Include        []string      `yaml:"include"`
	Exclude        []string      `yaml:"exclude"`
	Max            int           `yaml:"max"`
}

// BluetoothAdapter is one local controller.
type BluetoothAdapter struct {
	ID            string   `json:"id"`
	Address       string   `json:"address,omitempty"`
	Name          string   `json:"name,omitempty"`
	Powered       bool     `json:"powered"`
	Discoverable  bool     `json:"discoverable,omitempty"`
	Bus           string   `json:"bus,omitempty"`
	Manufacturer  string   `json:"manufacturer,omitempty"`
	HCIVersion    string   `json:"hci_version,omitempty"`
	LMPVersion    string   `json:"lmp_version,omitempty"`
	LinkPolicy    []string `json:"link_policy,omitempty"`
	LinkMode      []string `json:"link_mode,omitempty"`
	RxBytes       float64  `json:"rx_bytes,omitempty"`
	TxBytes       float64  `json:"tx_bytes,omitempty"`
	RxACL         float64  `json:"rx_acl,omitempty"`
	TxACL         float64  `json:"tx_acl,omitempty"`
	RxSCO         float64  `json:"rx_sco,omitempty"`
	TxSCO         float64  `json:"tx_sco,omitempty"`
	Events        float64  `json:"hci_events,omitempty"`
	Commands      float64  `json:"hci_commands,omitempty"`
	RxErrors      float64  `json:"rx_errors,omitempty"`
	TxErrors      float64  `json:"tx_errors,omitempty"`
	HasStatistics bool     `json:"has_statistics"`
	// AirTxKbps is the controller's outbound rate over the sampling window.
	// It covers every link on this controller, not one device.
	AirTxKbps *float64 `json:"air_tx_kbps,omitempty"`
	AirRxKbps *float64 `json:"air_rx_kbps,omitempty"`
}

// BluetoothDevice is one remote device and the state of its link.
type BluetoothDevice struct {
	Address      string         `json:"address"`
	Name         string         `json:"name"`
	Adapter      string         `json:"adapter,omitempty"`
	DeviceType   string         `json:"device_type,omitempty"`
	ClassOfDev   string         `json:"class_of_device,omitempty"`
	MajorClass   string         `json:"major_class,omitempty"`
	MinorClass   string         `json:"minor_class,omitempty"`
	ServiceClass []string       `json:"service_classes,omitempty"`
	Transport    string         `json:"transport,omitempty"`
	LinkType     string         `json:"link_type,omitempty"`
	LinkMode     string         `json:"link_mode,omitempty"`
	Role         string         `json:"role,omitempty"`
	Encrypted    *bool          `json:"encrypted,omitempty"`
	IntervalMs   *float64       `json:"link_interval_ms,omitempty"`
	Handle       *float64       `json:"connection_handle,omitempty"`
	Connected    bool           `json:"connected"`
	Paired       bool           `json:"paired"`
	Profiles     []string       `json:"profiles,omitempty"`
	RSSI         *float64       `json:"rssi_dbm,omitempty"`
	RSSIKind     string         `json:"rssi_kind,omitempty"`
	LinkQuality  *float64       `json:"link_quality,omitempty"`
	TxPower      *float64       `json:"tx_power_dbm,omitempty"`
	Battery      *float64       `json:"battery_percent,omitempty"`
	AFHChannels  *float64       `json:"afh_channels_used,omitempty"`
	AFHMap       string         `json:"afh_map,omitempty"`
	Firmware     string         `json:"firmware,omitempty"`
	Manufacturer string         `json:"manufacturer,omitempty"`
	Audio        *btAudioStream `json:"audio,omitempty"`
	Score        *float64       `json:"quality_score,omitempty"`
	Grade        string         `json:"quality_grade,omitempty"`
	Findings     []string       `json:"findings,omitempty"`
	Sources      []string       `json:"sources,omitempty"`
}

// btSnapshot is one complete probe round.
type btSnapshot struct {
	At       time.Time          `json:"-"`
	Platform string             `json:"platform"`
	Adapters []BluetoothAdapter `json:"adapters"`
	Devices  []BluetoothDevice  `json:"devices"`
	Sources  []string           `json:"sources,omitempty"`
	Limits   []string           `json:"limits,omitempty"`
}

type btCounters struct {
	at                 time.Time
	rxBytes, txBytes   float64
	hasRx, hasTx, seen bool
}

type bluetoothCollector struct {
	cfg      bluetoothConfig
	goos     string
	run      func(ctx context.Context, name string, args ...string) ([]byte, error)
	charts   map[string]bool
	lastPoll time.Time
	counters map[string]btCounters

	// probeMu serializes probes: the on-demand function and the scheduler tick
	// both call probe, and a probe mutates the rate counters.
	probeMu sync.Mutex
	mu      sync.Mutex
	last    btSnapshot
}

func init() {
	Register("bluetooth", func() Collector { return &bluetoothCollector{} })
}

func (b *bluetoothCollector) Name() string { return "bluetooth" }

func (b *bluetoothCollector) Configure(decode func(v any) error) error {
	if err := decode(&b.cfg); err != nil {
		return err
	}
	b.cfg.Timeout = durDefault(b.cfg.Timeout, 3*time.Second)
	b.cfg.Every = durDefault(b.cfg.Every, 5*time.Second)
	for ptr, def := range map[*string]string{
		&b.cfg.Bluetoothctl:   "bluetoothctl",
		&b.cfg.Hcitool:        "hcitool",
		&b.cfg.Hciconfig:      "hciconfig",
		&b.cfg.Pactl:          "pactl",
		&b.cfg.DBusSend:       "dbus-send",
		&b.cfg.SystemProfiler: "system_profiler",
		&b.cfg.PowerShell:     "powershell",
		&b.cfg.Dumpsys:        "dumpsys",
	} {
		if *ptr == "" {
			*ptr = def
		}
	}
	if b.cfg.Max <= 0 {
		b.cfg.Max = 32
	}
	return nil
}

func (b *bluetoothCollector) linkProbe() bool {
	return b.cfg.LinkProbe == nil || *b.cfg.LinkProbe
}

func (b *bluetoothCollector) Init(reg *registry.Registry) error {
	if b.cfg.Timeout == 0 {
		if err := b.Configure(func(any) error { return nil }); err != nil {
			return err
		}
	}
	if b.goos == "" {
		b.goos = runtime.GOOS
	}
	b.charts = map[string]bool{}
	b.counters = map[string]btCounters{}
	snap := b.probe(context.Background())
	if len(snap.Adapters) == 0 && len(snap.Devices) == 0 {
		return fmt.Errorf("bluetooth: no controller or device reported on %s", b.goos)
	}
	b.store(snap)
	reg.AddChart(chartMeta(&registry.Chart{ID: "bluetooth.devices", Context: "bluetooth.devices",
		Title: "Bluetooth devices", Units: "devices", Priority: 65000,
		Dimensions: []*registry.Dimension{{ID: "connected"}, {ID: "paired"}}}, "bluetooth", "bluetooth", "bluetooth"))
	reg.AddChart(chartMeta(&registry.Chart{ID: "bluetooth.connected_by_type", Context: "bluetooth.connected_by_type",
		Title: "Connected Bluetooth devices by type", Units: "devices", Type: registry.Stacked, Priority: 65010,
		Dimensions: []*registry.Dimension{{ID: "audio"}, {ID: "input"}, {ID: "phone"}, {ID: "computer"}, {ID: "other"}}},
		"bluetooth", "bluetooth", "bluetooth"))
	reg.AddChart(chartMeta(&registry.Chart{ID: "bluetooth.audio_streams", Context: "bluetooth.audio_streams",
		Title: "Bluetooth audio streams by codec", Units: "streams", Type: registry.Stacked, Priority: 65020},
		"bluetooth", "bluetooth", "bluetooth"))
	return nil
}

func (b *bluetoothCollector) store(s btSnapshot) {
	b.mu.Lock()
	b.last = s
	b.mu.Unlock()
}

// Snapshot returns the most recent probe. Callers get a value copy; the slices
// inside are never mutated after a probe is stored.
func (b *bluetoothCollector) Snapshot() btSnapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.last
}

func (b *bluetoothCollector) Collect(ctx context.Context, reg *registry.Registry, now time.Time) error {
	if !sampleDue(&b.lastPoll, now, b.cfg.Every) {
		return nil
	}
	snap := b.probe(ctx)
	snap.At = now
	b.store(snap)

	var connected, paired float64
	byType := map[string]float64{"audio": 0, "input": 0, "phone": 0, "computer": 0, "other": 0}
	byCodec := map[string]float64{}
	for _, d := range snap.Devices {
		if d.Paired {
			paired++
		}
		if !d.Connected {
			continue
		}
		connected++
		byType[btTypeBucket(d.DeviceType)]++
		if d.Audio != nil && d.Audio.Codec != "" {
			byCodec[btSafeToken(d.Audio.Codec)]++
		}
		b.collectDevice(reg, now, d)
	}
	_ = reg.Collect("bluetooth.devices", now, map[string]float64{"connected": connected, "paired": paired})
	_ = reg.Collect("bluetooth.connected_by_type", now, byType)
	if len(byCodec) > 0 {
		if ch, ok := reg.Chart("bluetooth.audio_streams"); ok {
			for codec := range byCodec {
				ch.AddDimension(&registry.Dimension{ID: codec})
			}
		}
		_ = reg.Collect("bluetooth.audio_streams", now, byCodec)
	}
	for _, a := range snap.Adapters {
		b.collectAdapter(reg, now, a)
	}
	return nil
}

func (b *bluetoothCollector) collectAdapter(reg *registry.Registry, now time.Time, a BluetoothAdapter) {
	if !a.HasStatistics {
		return
	}
	id := btSafeToken(a.ID)
	b.ensureAdapterCharts(reg, id, a)
	_ = reg.Collect("bluetooth.adapter_traffic."+id, now, map[string]float64{"receive": a.RxBytes, "transmit": a.TxBytes})
	_ = reg.Collect("bluetooth.adapter_packets."+id, now, map[string]float64{
		"rx_acl": a.RxACL, "tx_acl": a.TxACL, "rx_sco": a.RxSCO, "tx_sco": a.TxSCO,
		"events": a.Events, "commands": a.Commands})
	_ = reg.Collect("bluetooth.adapter_errors."+id, now, map[string]float64{"receive": a.RxErrors, "transmit": a.TxErrors})
}

func (b *bluetoothCollector) ensureAdapterCharts(reg *registry.Registry, id string, a BluetoothAdapter) {
	if b.charts["adapter:"+id] {
		return
	}
	b.charts["adapter:"+id] = true
	labels := map[string]string{"adapter": a.ID, "address": a.Address}
	traffic := &registry.Chart{ID: "bluetooth.adapter_traffic." + id, Context: "bluetooth.adapter_traffic",
		Title: "Bluetooth controller traffic", Units: "B/s", Type: registry.Area, Priority: 65030, Labels: labels,
		Dimensions: []*registry.Dimension{
			{ID: "receive", Algorithm: registry.Incremental},
			{ID: "transmit", Algorithm: registry.Incremental, Multiplier: -1}}}
	packets := &registry.Chart{ID: "bluetooth.adapter_packets." + id, Context: "bluetooth.adapter_packets",
		Title: "Bluetooth controller packets", Units: "packets/s", Priority: 65040, Labels: labels,
		Dimensions: []*registry.Dimension{
			incDim("rx_acl"), incDim("tx_acl"), incDim("rx_sco"), incDim("tx_sco"), incDim("events"), incDim("commands")}}
	errs := &registry.Chart{ID: "bluetooth.adapter_errors." + id, Context: "bluetooth.adapter_errors",
		Title: "Bluetooth controller errors", Units: "errors/s", Priority: 65050, Labels: labels,
		Dimensions: []*registry.Dimension{incDim("receive"), incDim("transmit")}}
	for _, c := range []*registry.Chart{traffic, packets, errs} {
		reg.AddChart(chartMeta(c, "bluetooth", "bluetooth", "bluetooth"))
	}
}

func (b *bluetoothCollector) collectDevice(reg *registry.Registry, now time.Time, d BluetoothDevice) {
	id := btChartID(d)
	if id == "" {
		return
	}
	b.ensureDeviceCharts(reg, id, d)
	put := func(chart string, dim string, v *float64) {
		if v == nil {
			return
		}
		_ = reg.Collect("bluetooth."+chart+"."+id, now, map[string]float64{dim: *v})
	}
	put("device_rssi", "rssi", d.RSSI)
	put("device_tx_power", "tx_power", d.TxPower)
	put("device_battery", "battery", d.Battery)
	put("device_afh_channels", "channels", d.AFHChannels)
	put("device_quality_score", "score", d.Score)
	if d.LinkQuality != nil {
		pct := *d.LinkQuality / 255 * 100
		_ = reg.Collect("bluetooth.device_link_quality."+id, now, map[string]float64{"quality": pct})
	}
	if d.Audio != nil {
		if d.Audio.Bitrate > 0 {
			_ = reg.Collect("bluetooth.device_audio_bitrate."+id, now, map[string]float64{"bitrate": d.Audio.Bitrate})
		}
		if d.Audio.SampleRate > 0 {
			_ = reg.Collect("bluetooth.device_audio_sample_rate."+id, now, map[string]float64{"sample_rate": d.Audio.SampleRate / 1000})
		}
		if d.Audio.DelayMs > 0 {
			_ = reg.Collect("bluetooth.device_audio_delay."+id, now, map[string]float64{"delay": d.Audio.DelayMs})
		}
	}
}

func (b *bluetoothCollector) ensureDeviceCharts(reg *registry.Registry, id string, d BluetoothDevice) {
	key := "device:" + id
	if b.charts[key] {
		return
	}
	b.charts[key] = true
	labels := map[string]string{"address": d.Address, "device": d.Name, "device_type": d.DeviceType}
	if d.Adapter != "" {
		labels["adapter"] = d.Adapter
	}
	one := func(idSuffix, context, title, units string, prio int, dim string) *registry.Chart {
		return &registry.Chart{ID: "bluetooth." + idSuffix + "." + id, Context: "bluetooth." + context,
			Title: title, Units: units, Priority: prio, Labels: labels,
			Dimensions: []*registry.Dimension{{ID: dim}}}
	}
	charts := []*registry.Chart{
		one("device_rssi", "device_rssi", "Bluetooth device signal strength", "dBm", 65100, "rssi"),
		one("device_link_quality", "device_link_quality", "Bluetooth device link quality", "percentage", 65110, "quality"),
		one("device_tx_power", "device_tx_power", "Bluetooth device transmit power", "dBm", 65120, "tx_power"),
		one("device_afh_channels", "device_afh_channels", "Bluetooth device AFH channels in use", "channels", 65130, "channels"),
		one("device_quality_score", "device_quality_score", "Bluetooth device link quality score", "points", 65140, "score"),
		one("device_battery", "device_battery", "Bluetooth device battery", "percentage", 65150, "battery"),
	}
	if d.Audio != nil {
		charts = append(charts,
			one("device_audio_bitrate", "device_audio_bitrate", "Bluetooth audio bitrate", "kbps", 65160, "bitrate"),
			one("device_audio_sample_rate", "device_audio_sample_rate", "Bluetooth audio sample rate", "kHz", 65170, "sample_rate"),
			one("device_audio_delay", "device_audio_delay", "Bluetooth audio transport delay", "milliseconds", 65180, "delay"))
	}
	for _, c := range charts {
		reg.AddChart(chartMeta(c, "bluetooth", "bluetooth", "bluetooth"))
	}
}

// btSafeToken makes a chart-id-safe token without sanitizeID's collision
// hash: Bluetooth addresses and codec names are already unique, and a hash
// suffix would make every chart id unreadable.
func btSafeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// btChartID prefers the address, which is unique per device; devices seen
// without one fall back to the name and keep sanitizeID's collision hash.
func btChartID(d BluetoothDevice) string {
	if btLooksLikeAddress(d.Address) {
		return btSafeToken(btNormalizeAddress(d.Address))
	}
	return sanitizeID(d.Name)
}

func btTypeBucket(deviceType string) string {
	t := strings.ToLower(deviceType)
	switch {
	case strings.Contains(t, "headphone"), strings.Contains(t, "headset"), strings.Contains(t, "speaker"),
		strings.Contains(t, "loudspeaker"), strings.Contains(t, "audio"), strings.Contains(t, "microphone"),
		strings.Contains(t, "hands-free"), strings.Contains(t, "car"):
		return "audio"
	case strings.Contains(t, "keyboard"), strings.Contains(t, "mouse"), strings.Contains(t, "pointing"),
		strings.Contains(t, "gamepad"), strings.Contains(t, "joystick"), strings.Contains(t, "remote"),
		strings.Contains(t, "peripheral"), strings.Contains(t, "input"), strings.Contains(t, "tablet"),
		strings.Contains(t, "pen"):
		return "input"
	case strings.Contains(t, "phone"), strings.Contains(t, "smartphone"):
		return "phone"
	case strings.Contains(t, "computer"), strings.Contains(t, "laptop"), strings.Contains(t, "desktop"):
		return "computer"
	}
	return "other"
}

func (b *bluetoothCollector) Functions() []Function {
	return []Function{{
		Name:    "bluetooth-devices",
		Help:    "Connected Bluetooth devices: link mode, codec, bitrate and link quality",
		Timeout: 15,
		Run: func(ctx context.Context, args map[string]string) (any, error) {
			return b.table(ctx, args), nil
		},
	}}
}

// BluetoothRow is one row of the bluetooth-devices function table.
type BluetoothRow struct {
	Name        string   `json:"name"`
	Address     string   `json:"address"`
	DeviceType  string   `json:"device_type"`
	Transport   string   `json:"transport"`
	LinkMode    string   `json:"link_mode"`
	Role        string   `json:"role"`
	Profiles    string   `json:"profiles"`
	Codec       string   `json:"codec"`
	CodecVendor string   `json:"codec_vendor"`
	Bitrate     *float64 `json:"bitrate_kbps"`
	BitrateKind string   `json:"bitrate_kind"`
	CodecMax    *float64 `json:"codec_max_kbps"`
	SampleRate  *float64 `json:"sample_rate_khz"`
	BitDepth    *float64 `json:"bit_depth"`
	ChannelMode string   `json:"channel_mode"`
	RSSI        *float64 `json:"rssi_dbm"`
	RSSIKind    string   `json:"rssi_kind"`
	LinkQuality *float64 `json:"link_quality_percent"`
	TxPower     *float64 `json:"tx_power_dbm"`
	AFHChannels *float64 `json:"afh_channels_used"`
	Battery     *float64 `json:"battery_percent"`
	Score       *float64 `json:"quality_score"`
	Grade       string   `json:"quality_grade"`
	Findings    string   `json:"findings"`
	Sources     string   `json:"sources"`
}

var bluetoothColumns = []string{"name", "address", "device_type", "transport", "link_mode", "role", "profiles",
	"codec", "codec_vendor", "bitrate_kbps", "bitrate_kind", "codec_max_kbps", "sample_rate_khz", "bit_depth",
	"channel_mode", "rssi_dbm", "rssi_kind", "link_quality_percent", "tx_power_dbm", "afh_channels_used", "battery_percent",
	"quality_score", "quality_grade", "findings", "sources"}

// BluetoothReport is the payload of the bluetooth-devices function: the table
// plus the controller view and an explicit list of what this host could not
// measure, so an empty column is never read as a healthy zero.
type BluetoothReport struct {
	Table
	Platform string             `json:"platform"`
	Adapters []BluetoothAdapter `json:"adapters"`
	Sources  []string           `json:"sources,omitempty"`
	Limits   []string           `json:"limits,omitempty"`
}

func (b *bluetoothCollector) table(ctx context.Context, args map[string]string) BluetoothReport {
	snap := b.Snapshot()
	if args["refresh"] == "true" || snap.At.IsZero() {
		snap = b.probe(ctx)
		snap.At = time.Now()
		b.store(snap)
	}
	rows := make([]any, 0, len(snap.Devices))
	for _, d := range snap.Devices {
		if args["all"] != "true" && !d.Connected {
			continue
		}
		rows = append(rows, btRow(d))
	}
	out := BluetoothReport{Platform: snap.Platform, Adapters: snap.Adapters, Sources: snap.Sources, Limits: snap.Limits}
	out.Columns = bluetoothColumns
	out.Rows = rows
	out.Total = len(rows)
	if !snap.At.IsZero() {
		out.CollectedAt = snap.At.Unix()
	}
	return out
}

func btRow(d BluetoothDevice) BluetoothRow {
	r := BluetoothRow{
		Name: d.Name, Address: d.Address, DeviceType: d.DeviceType, Transport: d.Transport,
		LinkMode: d.LinkMode, Role: d.Role, Profiles: strings.Join(d.Profiles, ", "),
		RSSI: d.RSSI, RSSIKind: d.RSSIKind, TxPower: d.TxPower, AFHChannels: d.AFHChannels, Battery: d.Battery,
		Score: d.Score, Grade: d.Grade, Findings: strings.Join(d.Findings, "; "),
		Sources: strings.Join(d.Sources, ", "),
	}
	if d.LinkQuality != nil {
		pct := *d.LinkQuality / 255 * 100
		r.LinkQuality = &pct
	}
	if a := d.Audio; a != nil {
		r.Codec, r.CodecVendor, r.ChannelMode, r.BitrateKind = a.Codec, a.Vendor, a.ChannelMode, a.BitrateKind
		if a.Bitrate > 0 {
			v := a.Bitrate
			r.Bitrate = &v
		}
		if max := btCodecCeiling(a.Codec); max > 0 {
			r.CodecMax = &max
		}
		if a.SampleRate > 0 {
			khz := a.SampleRate / 1000
			r.SampleRate = &khz
		}
		if a.BitDepth > 0 {
			depth := float64(a.BitDepth)
			r.BitDepth = &depth
		}
	}
	return r
}

// btFinalize derives the per-device verdict once every probe has contributed.
func btFinalize(devices []BluetoothDevice) {
	for i := range devices {
		d := &devices[i]
		sort.Strings(d.Sources)
		d.Sources = btDedupe(d.Sources)
		d.Profiles = btDedupe(d.Profiles)
		if !d.Connected {
			continue
		}
		score, findings := btScore(*d)
		d.Findings = append(d.Findings, findings...)
		d.Score = &score
		d.Grade = btGrade(score)
	}
}

func btDedupe(in []string) []string {
	if len(in) < 2 {
		return in
	}
	seen := map[string]bool{}
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// btScore is a composite 0–100 link score. It is this project's own weighting,
// not a figure defined by the Bluetooth specification: signal strength (40),
// controller-reported link quality (25), codec headroom (25) and channel
// conditions (10). Inputs the host cannot measure are skipped and the score is
// rescaled over the remaining weight, so a missing probe lowers confidence
// rather than silently producing a bad grade.
func btScore(d BluetoothDevice) (float64, []string) {
	var got, weight float64
	var findings []string
	if d.RSSI != nil && d.RSSIKind != "golden-range" {
		weight += 40
		got += 40 * btRSSIFactor(*d.RSSI)
		if *d.RSSI <= -80 {
			findings = append(findings, fmt.Sprintf("信号强度 %.0f dBm 偏弱，易出现卡顿", *d.RSSI))
		}
	}
	if d.LinkQuality != nil {
		weight += 25
		got += 25 * (*d.LinkQuality / 255)
		if *d.LinkQuality < 200 {
			findings = append(findings, fmt.Sprintf("控制器链路质量 %.0f/255，低于典型的 255", *d.LinkQuality))
		}
	}
	if d.Audio != nil && d.Audio.Bitrate > 0 {
		weight += 25
		got += 25 * btCodecFactor(*d.Audio)
		if d.Audio.BitrateKind == btBitrateNominal {
			findings = append(findings, "码率取自编解码器标称值，本机未提供协商后的配置")
		}
	}
	if d.AFHChannels != nil {
		weight += 10
		got += 10 * btAFHFactor(*d.AFHChannels)
		if *d.AFHChannels < 40 {
			findings = append(findings, fmt.Sprintf("AFH 可用信道仅 %.0f/79，2.4 GHz 频段干扰较重", *d.AFHChannels))
		}
	}
	if weight == 0 {
		return 0, append(findings, "本机未提供任何可用于评分的链路指标")
	}
	return got / weight * 100, findings
}

func btRSSIFactor(rssi float64) float64 {
	switch {
	case rssi >= -55:
		return 1
	case rssi >= -70:
		return 0.5 + (rssi+70)/15*0.5
	case rssi >= -85:
		return (rssi + 85) / 15 * 0.5
	}
	return 0
}

func btCodecFactor(a btAudioStream) float64 {
	ceiling := btCodecCeiling(a.Codec)
	if ceiling <= 0 {
		ceiling = a.Bitrate
	}
	ratio := a.Bitrate / ceiling
	if ratio > 1 {
		ratio = 1
	}
	// Absolute rate matters as much as codec headroom: a codec running at its
	// own low ceiling is still a low-rate link.
	absolute := a.Bitrate / 660
	if absolute > 1 {
		absolute = 1
	}
	return 0.5*ratio + 0.5*absolute
}

func btAFHFactor(channels float64) float64 {
	if channels <= 0 {
		return 0
	}
	f := channels / 79
	if f > 1 {
		return 1
	}
	return f
}

func btGrade(score float64) string {
	switch {
	case score >= 85:
		return "excellent"
	case score >= 70:
		return "good"
	case score >= 50:
		return "fair"
	}
	return "poor"
}

func (b *bluetoothCollector) wanted(d BluetoothDevice) bool {
	for _, p := range b.cfg.Exclude {
		if globMatch(p, d.Name) || globMatch(p, d.Address) {
			return false
		}
	}
	if len(b.cfg.Include) == 0 {
		return true
	}
	for _, p := range b.cfg.Include {
		if globMatch(p, d.Name) || globMatch(p, d.Address) {
			return true
		}
	}
	return false
}

// probe runs every source available on this host and merges the result.
func (b *bluetoothCollector) probe(ctx context.Context) btSnapshot {
	goos := b.goos
	if goos == "" {
		goos = runtime.GOOS
	}
	b.probeMu.Lock()
	defer b.probeMu.Unlock()
	snap := btSnapshot{Platform: goos}
	platform := goos
	switch {
	case b.cfg.SnapshotDir != "":
		snap = b.probeSnapshotDir()
		platform = snap.Platform
	case goos == "linux", goos == "freebsd":
		snap = b.probeLinux(ctx)
	case goos == "android":
		snap = b.probeAndroid(ctx)
	case goos == "darwin":
		snap = b.probeDarwin(ctx)
	case goos == "windows":
		snap = b.probeWindows(ctx)
	default:
		snap.Limits = append(snap.Limits, "本平台暂无蓝牙采集来源")
	}
	snap.Platform = platform
	filtered := snap.Devices[:0]
	for _, d := range snap.Devices {
		if !b.wanted(d) {
			continue
		}
		filtered = append(filtered, d)
		if len(filtered) >= b.cfg.Max {
			snap.Limits = append(snap.Limits, fmt.Sprintf("设备数超过 max=%d，已截断", b.cfg.Max))
			break
		}
	}
	snap.Devices = filtered
	b.measureAirRate(&snap)
	btFinalize(snap.Devices)
	sort.SliceStable(snap.Devices, func(i, j int) bool {
		if snap.Devices[i].Connected != snap.Devices[j].Connected {
			return snap.Devices[i].Connected
		}
		return snap.Devices[i].Name < snap.Devices[j].Name
	})
	snap.Sources = btDedupe(snap.Sources)
	return snap
}

// measureAirRate converts the controller byte counters into a rate over the
// time between two probes. It is a controller-wide figure: with several active
// links it does not attribute traffic to one device.
func (b *bluetoothCollector) measureAirRate(snap *btSnapshot) {
	if b.counters == nil {
		b.counters = map[string]btCounters{}
	}
	now := time.Now()
	for i := range snap.Adapters {
		a := &snap.Adapters[i]
		if !a.HasStatistics {
			continue
		}
		prev, ok := b.counters[a.ID]
		b.counters[a.ID] = btCounters{at: now, rxBytes: a.RxBytes, txBytes: a.TxBytes, seen: true}
		if !ok || !prev.seen {
			continue
		}
		elapsed := now.Sub(prev.at).Seconds()
		if elapsed <= 0 || a.RxBytes < prev.rxBytes || a.TxBytes < prev.txBytes {
			continue
		}
		rx := (a.RxBytes - prev.rxBytes) * 8 / 1000 / elapsed
		tx := (a.TxBytes - prev.txBytes) * 8 / 1000 / elapsed
		a.AirRxKbps, a.AirTxKbps = &rx, &tx
	}
}

// BluetoothReportNow runs one probe outside the scheduler and returns the
// report, backing `monitord -bluetooth-report`. Two probes one second apart
// are taken so the controller byte counters produce an air rate.
func BluetoothReportNow(ctx context.Context, snapshotDir string) (BluetoothReport, error) {
	b := &bluetoothCollector{goos: runtime.GOOS, counters: map[string]btCounters{}}
	err := b.Configure(func(v any) error {
		if cfg, ok := v.(*bluetoothConfig); ok {
			cfg.SnapshotDir = snapshotDir
		}
		return nil
	})
	if err != nil {
		return BluetoothReport{}, err
	}
	b.store(b.probe(ctx))
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
	return b.table(ctx, map[string]string{"refresh": "true", "all": "true"}), nil
}

// FormatBluetoothReport renders the report for a terminal.
func FormatBluetoothReport(r BluetoothReport) string {
	var b strings.Builder
	fmt.Fprintf(&b, "蓝牙链路报告  平台: %s", r.Platform)
	if r.CollectedAt > 0 {
		fmt.Fprintf(&b, "  采样时间: %s", time.Unix(r.CollectedAt, 0).Format(time.RFC3339))
	}
	b.WriteString("\n\n")
	b.WriteString("控制器:\n")
	if len(r.Adapters) == 0 {
		b.WriteString("  （未发现本机蓝牙控制器）\n")
	}
	for _, a := range r.Adapters {
		fmt.Fprintf(&b, "  %s", strings.TrimSpace(a.ID+" "+a.Address))
		fmt.Fprintf(&b, "  电源: %s", btYesNo(a.Powered))
		if a.Bus != "" {
			fmt.Fprintf(&b, "  总线: %s", a.Bus)
		}
		if a.HasStatistics {
			fmt.Fprintf(&b, "  收/发字节: %.0f / %.0f  错误: 收 %.0f / 发 %.0f", a.RxBytes, a.TxBytes, a.RxErrors, a.TxErrors)
		}
		if a.AirTxKbps != nil && a.AirRxKbps != nil {
			fmt.Fprintf(&b, "  空口速率: 收 %.1f kbps / 发 %.1f kbps", *a.AirRxKbps, *a.AirTxKbps)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n设备 (%d):\n", r.Total)
	for i, row := range r.Rows {
		d, ok := row.(BluetoothRow)
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "  [%d] %s  %s\n", i+1, d.Name, d.Address)
		fmt.Fprintf(&b, "      类型: %s   传输: %s   链路模式: %s   角色: %s\n",
			btDash(d.DeviceType), btDash(d.Transport), btDash(d.LinkMode), btDash(d.Role))
		if d.Profiles != "" {
			fmt.Fprintf(&b, "      profile: %s\n", d.Profiles)
		}
		if d.Codec != "" {
			fmt.Fprintf(&b, "      编解码器: %s%s   码率: %s   采样: %s\n",
				d.Codec, btParen(d.CodecVendor), btBitrateText(d), btAudioFormatText(d))
		}
		fmt.Fprintf(&b, "      信号: %s   链路质量: %s   发射功率: %s   AFH 可用信道: %s   电量: %s\n",
			btRSSIText(d), btUnit(d.LinkQuality, "%", 0), btUnit(d.TxPower, "dBm", 0),
			btAFHText(d.AFHChannels), btUnit(d.Battery, "%", 0))
		if d.Score != nil {
			fmt.Fprintf(&b, "      链路评分: %.0f / 100 (%s)\n", *d.Score, d.Grade)
		}
		if d.Findings != "" {
			fmt.Fprintf(&b, "      提示: %s\n", d.Findings)
		}
		if d.Sources != "" {
			fmt.Fprintf(&b, "      数据来源: %s\n", d.Sources)
		}
	}
	if len(r.Limits) > 0 {
		b.WriteString("\n本机未能测量的项目:\n")
		for _, l := range r.Limits {
			fmt.Fprintf(&b, "  - %s\n", l)
		}
	}
	return b.String()
}

func btYesNo(v bool) string {
	if v {
		return "开"
	}
	return "关"
}

func btDash(s string) string {
	if s == "" {
		return "未知"
	}
	return s
}

func btParen(s string) string {
	if s == "" {
		return ""
	}
	return " (" + s + ")"
}

func btUnit(v *float64, unit string, decimals int) string {
	if v == nil {
		return "未提供"
	}
	return strconv.FormatFloat(*v, 'f', decimals, 64) + " " + unit
}

// btRSSIText keeps the BR/EDR golden-range reading distinguishable: HCI
// Read_RSSI returns the distance from the controller's golden receive power
// range on a classic link, so printing it as dBm would be wrong.
func btRSSIText(d BluetoothRow) string {
	if d.RSSI == nil {
		return "未提供"
	}
	if d.RSSIKind == "golden-range" {
		return fmt.Sprintf("%.0f（BR/EDR 黄金范围偏移，不是 dBm）", *d.RSSI)
	}
	return fmt.Sprintf("%.0f dBm", *d.RSSI)
}

func btAFHText(v *float64) string {
	if v == nil {
		return "未提供"
	}
	return fmt.Sprintf("%.0f/79", *v)
}

func btBitrateText(d BluetoothRow) string {
	if d.Bitrate == nil {
		return "未提供"
	}
	kind := map[string]string{
		btBitrateNegotiated: "协商值",
		btBitrateNominal:    "标称值",
		btBitrateMeasured:   "实测值",
	}[d.BitrateKind]
	out := fmt.Sprintf("%.1f kbps (%s", *d.Bitrate, btDash(kind))
	if d.CodecMax != nil {
		out += fmt.Sprintf("，编解码器上限 %.0f kbps", *d.CodecMax)
	}
	return out + ")"
}

func btAudioFormatText(d BluetoothRow) string {
	parts := make([]string, 0, 3)
	if d.SampleRate != nil {
		parts = append(parts, fmt.Sprintf("%.1f kHz", *d.SampleRate))
	}
	if d.BitDepth != nil {
		parts = append(parts, fmt.Sprintf("%.0f bit", *d.BitDepth))
	}
	if d.ChannelMode != "" {
		parts = append(parts, d.ChannelMode)
	}
	if len(parts) == 0 {
		return "未提供"
	}
	return strings.Join(parts, " / ")
}

func (b *bluetoothCollector) exec(ctx context.Context, name string, args ...string) ([]byte, error) {
	if b.run != nil {
		return b.run(ctx, name, args...)
	}
	return execRun(b.cfg.Timeout)(ctx, name, args...)
}
