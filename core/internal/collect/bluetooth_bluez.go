package collect

import (
	"context"
	"strconv"
	"strings"
)

// probeLinux collects from the BlueZ stack. D-Bus is the authoritative source
// (it carries the negotiated A2DP codec configuration), bluetoothctl covers
// hosts where dbus-send is unavailable, PulseAudio/PipeWire names the codec
// actually in use by the audio server, and the HCI tools add the controller
// counters and per-link radio measurements.
func (b *bluetoothCollector) probeLinux(ctx context.Context) btSnapshot {
	snap := btSnapshot{Platform: "linux"}
	index := newBTIndex()

	if out, err := b.exec(ctx, b.cfg.DBusSend, "--system", "--print-reply", "--dest=org.bluez",
		"/", "org.freedesktop.DBus.ObjectManager.GetManagedObjects"); err == nil {
		adapters, devices := btFromDBus(parseDBusManagedObjects(string(out)))
		snap.Adapters = append(snap.Adapters, adapters...)
		for _, d := range devices {
			index.merge(d)
		}
		snap.Sources = append(snap.Sources, "bluez-dbus")
	} else {
		snap.Limits = append(snap.Limits, "dbus-send 不可用，未读取 BlueZ 协商后的编解码器配置："+btErr(err))
		if out, err := b.exec(ctx, b.cfg.Bluetoothctl, "devices", "Connected"); err == nil {
			for _, basic := range parseBluetoothctlDevices(string(out)) {
				info, err := b.exec(ctx, b.cfg.Bluetoothctl, "info", basic.Address)
				if err != nil {
					index.merge(basic)
					continue
				}
				d := parseBluetoothctlInfo(string(info))
				if d.Address == "" {
					d.Address = basic.Address
				}
				index.merge(d)
			}
			snap.Sources = append(snap.Sources, "bluetoothctl")
		} else {
			snap.Limits = append(snap.Limits, "bluetoothctl 不可用："+btErr(err))
		}
	}

	if out, err := b.exec(ctx, b.cfg.Hciconfig, "-a"); err == nil {
		stats := parseHciconfig(string(out))
		if len(snap.Adapters) == 0 {
			snap.Adapters = stats
		} else {
			btMergeAdapterStats(snap.Adapters, stats)
		}
		snap.Sources = append(snap.Sources, "hciconfig")
	} else {
		snap.Limits = append(snap.Limits, "hciconfig 不可用，没有控制器收发字节与错误计数："+btErr(err))
	}

	if out, err := b.exec(ctx, b.cfg.Pactl, "list"); err == nil {
		for addr, card := range parsePactlBluetooth(string(out)) {
			index.applyAudio(addr, card.Audio, "pulseaudio")
			d := index.get(addr)
			// A bluez5 card exists only while the device is connected.
			d.Connected = true
			if d.Name == "" {
				d.Name = card.Name
			}
			if d.MajorClass == "" && card.Class != "" {
				d.ClassOfDev = card.Class
				if class, ok := btParseClass(card.Class); ok {
					d.MajorClass, d.MinorClass, d.ServiceClass = btDecodeClass(class)
					d.DeviceType = btDeviceType(d.MinorClass, "", d.Profiles)
				}
			}
		}
		snap.Sources = append(snap.Sources, "pulseaudio")
	} else {
		snap.Limits = append(snap.Limits, "pactl 不可用，未读取音频服务端当前编解码器："+btErr(err))
	}

	if b.linkProbe() {
		b.probeHCILinks(ctx, index, &snap)
	} else {
		snap.Limits = append(snap.Limits, "link_probe 已关闭，未读取链路质量、发射功率和 AFH 信道图")
	}
	snap.Devices = index.list()
	return snap
}

// probeHCILinks reads the per-link radio measurements. These issue raw HCI
// commands, so they need CAP_NET_RAW (usually root); without it the values are
// reported as unavailable rather than guessed.
func (b *bluetoothCollector) probeHCILinks(ctx context.Context, index *btIndex, snap *btSnapshot) {
	out, err := b.exec(ctx, b.cfg.Hcitool, "con")
	if err != nil {
		snap.Limits = append(snap.Limits, "hcitool 不可用，未读取链路质量、发射功率和 AFH 信道图（通常需要 root 或 CAP_NET_RAW）："+btErr(err))
		return
	}
	conns := parseHcitoolConnections(string(out))
	if len(conns) == 0 {
		return
	}
	snap.Sources = append(snap.Sources, "hcitool")
	denied := false
	for _, c := range conns {
		d := index.get(c.Address)
		d.Connected = true
		d.LinkType = c.LinkType
		if c.Role != "" {
			d.Role = c.Role
		}
		if c.Encrypted != nil {
			d.Encrypted = c.Encrypted
		}
		if c.Handle > 0 {
			h := float64(c.Handle)
			d.Handle = &h
		}
		if c.LinkMode != "" {
			d.LinkMode = c.LinkMode
		}
		d.Sources = append(d.Sources, "hcitool")
		for _, probe := range []struct {
			arg    string
			assign func(float64)
		}{
			{"lq", func(v float64) { d.LinkQuality = &v }},
			{"tpl", func(v float64) { d.TxPower = &v }},
		} {
			raw, err := b.exec(ctx, b.cfg.Hcitool, probe.arg, c.Address)
			if err != nil {
				denied = true
				continue
			}
			if v, ok := btLastNumber(string(raw)); ok {
				probe.assign(v)
			}
		}
		// Read_RSSI on a BR/EDR link returns the distance from the golden
		// receive power range, not an absolute level; only LE links report
		// dBm. Mixing the two is a common way to misread a link, so the kind
		// travels with the value and the score ignores golden-range readings.
		if raw, err := b.exec(ctx, b.cfg.Hcitool, "rssi", c.Address); err == nil {
			if v, ok := btLastNumber(string(raw)); ok {
				if c.LinkType == "LE" {
					d.RSSI, d.RSSIKind = &v, "absolute"
				} else if d.RSSI == nil {
					d.RSSI, d.RSSIKind = &v, "golden-range"
				}
			}
		} else {
			denied = true
		}
		if raw, err := b.exec(ctx, b.cfg.Hcitool, "afh", c.Address); err == nil {
			if hexMap, ok := btParseAFHOutput(string(raw)); ok {
				d.AFHMap = hexMap
				if n, ok := btAFHChannels(hexMap); ok {
					d.AFHChannels = &n
				}
			}
		} else {
			denied = true
		}
	}
	if denied {
		snap.Limits = append(snap.Limits, "部分 HCI 查询失败，链路质量 / 发射功率 / AFH 信道图可能缺失（通常需要 root 或 CAP_NET_RAW）")
	}
}

func btErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// btIndex merges the per-source views of the same device by address.
type btIndex struct {
	byAddr map[string]*BluetoothDevice
	order  []string
}

func newBTIndex() *btIndex { return &btIndex{byAddr: map[string]*BluetoothDevice{}} }

func (ix *btIndex) get(addr string) *BluetoothDevice {
	key := btNormalizeAddress(addr)
	if d, ok := ix.byAddr[key]; ok {
		return d
	}
	d := &BluetoothDevice{Address: key}
	ix.byAddr[key] = d
	ix.order = append(ix.order, key)
	return d
}

func (ix *btIndex) merge(in BluetoothDevice) {
	d := ix.get(in.Address)
	if in.Name != "" {
		d.Name = in.Name
	}
	for _, f := range []struct{ dst, src *string }{
		{&d.Adapter, &in.Adapter}, {&d.DeviceType, &in.DeviceType}, {&d.ClassOfDev, &in.ClassOfDev},
		{&d.MajorClass, &in.MajorClass}, {&d.MinorClass, &in.MinorClass}, {&d.Transport, &in.Transport},
		{&d.LinkType, &in.LinkType}, {&d.LinkMode, &in.LinkMode}, {&d.Role, &in.Role},
		{&d.RSSIKind, &in.RSSIKind}, {&d.Firmware, &in.Firmware}, {&d.Manufacturer, &in.Manufacturer},
		{&d.AFHMap, &in.AFHMap},
	} {
		if *f.src != "" {
			*f.dst = *f.src
		}
	}
	d.Connected = d.Connected || in.Connected
	d.Paired = d.Paired || in.Paired
	d.Profiles = append(d.Profiles, in.Profiles...)
	d.ServiceClass = append(d.ServiceClass, in.ServiceClass...)
	d.Sources = append(d.Sources, in.Sources...)
	d.Findings = append(d.Findings, in.Findings...)
	for _, f := range []struct{ dst, src **float64 }{
		{&d.RSSI, &in.RSSI}, {&d.LinkQuality, &in.LinkQuality}, {&d.TxPower, &in.TxPower},
		{&d.Battery, &in.Battery}, {&d.AFHChannels, &in.AFHChannels}, {&d.IntervalMs, &in.IntervalMs},
		{&d.Handle, &in.Handle},
	} {
		if *f.src != nil {
			*f.dst = *f.src
		}
	}
	if in.Encrypted != nil {
		d.Encrypted = in.Encrypted
	}
	if in.Audio != nil {
		d.Audio = in.Audio
	}
}

// applyAudio attaches codec information, keeping whichever source carries the
// negotiated configuration rather than a table lookup.
func (ix *btIndex) applyAudio(addr string, audio *btAudioStream, source string) {
	if audio == nil {
		return
	}
	d := ix.get(addr)
	d.Sources = append(d.Sources, source)
	if d.Audio == nil {
		d.Audio = audio
		return
	}
	cur := d.Audio
	if cur.BitrateKind != btBitrateNegotiated && audio.BitrateKind == btBitrateNegotiated {
		audio.State, audio.DelayMs = btPickString(audio.State, cur.State), btPickFloat(audio.DelayMs, cur.DelayMs)
		d.Audio = audio
		return
	}
	cur.Codec = btPickString(cur.Codec, audio.Codec)
	cur.Vendor = btPickString(cur.Vendor, audio.Vendor)
	cur.Profile = btPickString(cur.Profile, audio.Profile)
	cur.ChannelMode = btPickString(cur.ChannelMode, audio.ChannelMode)
	cur.State = btPickString(cur.State, audio.State)
	cur.QualityMode = btPickString(cur.QualityMode, audio.QualityMode)
	cur.SampleRate = btPickFloat(cur.SampleRate, audio.SampleRate)
	cur.DelayMs = btPickFloat(cur.DelayMs, audio.DelayMs)
	if cur.BitDepth == 0 {
		cur.BitDepth = audio.BitDepth
	}
	if cur.Bitrate == 0 && audio.Bitrate > 0 {
		cur.Bitrate, cur.BitrateKind = audio.Bitrate, audio.BitrateKind
	}
	cur.Notes = append(cur.Notes, audio.Notes...)
}

func btPickString(cur, next string) string {
	if cur != "" {
		return cur
	}
	return next
}

func btPickFloat(cur, next float64) float64 {
	if cur != 0 {
		return cur
	}
	return next
}

func (ix *btIndex) list() []BluetoothDevice {
	out := make([]BluetoothDevice, 0, len(ix.order))
	for _, k := range ix.order {
		out = append(out, *ix.byAddr[k])
	}
	return out
}

// btNormalizeAddress accepts every spelling the stacks use: colons, dashes,
// PulseAudio underscores and BlueZ object paths.
func btNormalizeAddress(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "dev_"); i >= 0 {
		s = s[i+len("dev_"):]
		if j := strings.IndexByte(s, '/'); j >= 0 {
			s = s[:j]
		}
	}
	s = strings.NewReplacer("_", ":", "-", ":").Replace(s)
	return strings.ToUpper(s)
}

func btLooksLikeAddress(s string) bool {
	parts := strings.Split(btNormalizeAddress(s), ":")
	if len(parts) != 6 {
		return false
	}
	for _, p := range parts {
		if len(p) != 2 {
			return false
		}
		if _, err := strconv.ParseUint(p, 16, 8); err != nil {
			return false
		}
	}
	return true
}

// parseBluetoothctlDevices reads "Device AA:BB:CC:DD:EE:FF Name" lines.
func parseBluetoothctlDevices(out string) []BluetoothDevice {
	var devices []BluetoothDevice
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 || fields[0] != "Device" || !btLooksLikeAddress(fields[1]) {
			continue
		}
		d := BluetoothDevice{Address: btNormalizeAddress(fields[1]), Connected: true,
			Sources: []string{"bluetoothctl"}}
		if len(fields) > 2 {
			d.Name = strings.Join(fields[2:], " ")
		}
		devices = append(devices, d)
	}
	return devices
}

// parseBluetoothctlInfo reads the key/value block of `bluetoothctl info`.
func parseBluetoothctlInfo(out string) BluetoothDevice {
	d := BluetoothDevice{Sources: []string{"bluetoothctl"}}
	addressType := ""
	icon := ""
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Device ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				d.Address = btNormalizeAddress(fields[1])
			}
			if len(fields) >= 3 {
				addressType = strings.Trim(fields[2], "()")
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Name":
			d.Name = value
		case "Alias":
			if d.Name == "" {
				d.Name = value
			}
		case "Icon":
			icon = value
		case "Class":
			d.ClassOfDev = value
			if class, ok := btParseClass(value); ok {
				d.MajorClass, d.MinorClass, d.ServiceClass = btDecodeClass(class)
			}
		case "Paired", "Bonded":
			d.Paired = d.Paired || value == "yes"
		case "Connected":
			d.Connected = value == "yes"
		case "UUID":
			if open := strings.LastIndex(value, "("); open >= 0 {
				if name := btProfileForUUID(strings.Trim(value[open:], "()")); name != "" {
					d.Profiles = append(d.Profiles, name)
				}
			}
		case "RSSI":
			if v, err := strconv.ParseFloat(value, 64); err == nil {
				d.RSSI, d.RSSIKind = &v, "absolute"
			}
		case "TxPower":
			if v, err := strconv.ParseFloat(value, 64); err == nil {
				d.TxPower = &v
			}
		case "Battery Percentage":
			if v, ok := btBatteryPercent(value); ok {
				d.Battery = &v
			}
		case "Modalias":
			d.Firmware = value
		}
	}
	d.DeviceType = btDeviceType(d.MinorClass, icon, d.Profiles)
	d.Transport = btTransportForUUIDs(d.Profiles, addressType)
	return d
}

// btBatteryPercent reads bluetoothctl's "0x5a (90)" and plain "90%" forms.
func btBatteryPercent(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if open := strings.LastIndex(s, "("); open >= 0 {
		if close := strings.LastIndex(s, ")"); close > open {
			if v, err := strconv.ParseFloat(strings.TrimSpace(s[open+1:close]), 64); err == nil {
				return v, true
			}
		}
	}
	v, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "%"), 64)
	return v, err == nil
}

// btDeviceType prefers the decoded minor class, then the BlueZ icon, then the
// profiles the device exposes.
func btDeviceType(minorClass, icon string, profiles []string) string {
	if minorClass != "" && !strings.HasPrefix(minorClass, "Minor 0x") {
		return minorClass
	}
	if t, ok := btDeviceTypeFromIcon[icon]; ok {
		return t
	}
	for _, p := range profiles {
		switch {
		case strings.Contains(p, "A2DP"):
			return "Audio"
		case strings.Contains(p, "HID"), strings.Contains(p, "HOGP"):
			return "Input"
		}
	}
	return ""
}

// parseHciconfig reads `hciconfig -a`.
func parseHciconfig(out string) []BluetoothAdapter {
	var adapters []BluetoothAdapter
	var cur *BluetoothAdapter
	flush := func() {
		if cur != nil {
			adapters = append(adapters, *cur)
			cur = nil
		}
	}
	for _, raw := range strings.Split(out, "\n") {
		if raw == "" {
			continue
		}
		if !strings.HasPrefix(raw, "\t") && !strings.HasPrefix(raw, " ") {
			flush()
			id, rest, _ := strings.Cut(strings.TrimSpace(raw), ":")
			cur = &BluetoothAdapter{ID: strings.TrimSpace(id)}
			if bus := btFieldAfter(rest, "Bus:"); bus != "" {
				cur.Bus = bus
			}
			continue
		}
		if cur == nil {
			continue
		}
		line := strings.TrimSpace(raw)
		switch {
		case strings.HasPrefix(line, "BD Address:"):
			cur.Address = btFieldAfter(line, "BD Address:")
		case strings.HasPrefix(line, "RX bytes:"):
			cur.RxBytes = btNumberAfter(line, "bytes:")
			cur.RxACL = btNumberAfter(line, "acl:")
			cur.RxSCO = btNumberAfter(line, "sco:")
			cur.Events = btNumberAfter(line, "events:")
			cur.RxErrors = btNumberAfter(line, "errors:")
			cur.HasStatistics = true
		case strings.HasPrefix(line, "TX bytes:"):
			cur.TxBytes = btNumberAfter(line, "bytes:")
			cur.TxACL = btNumberAfter(line, "acl:")
			cur.TxSCO = btNumberAfter(line, "sco:")
			cur.Commands = btNumberAfter(line, "commands:")
			cur.TxErrors = btNumberAfter(line, "errors:")
			cur.HasStatistics = true
		case strings.HasPrefix(line, "Name:"):
			cur.Name = strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "Name:")), "'")
		case strings.HasPrefix(line, "Link policy:"):
			cur.LinkPolicy = strings.Fields(strings.TrimPrefix(line, "Link policy:"))
		case strings.HasPrefix(line, "Link mode:"):
			cur.LinkMode = strings.Fields(strings.TrimPrefix(line, "Link mode:"))
		case strings.HasPrefix(line, "HCI Version:"):
			cur.HCIVersion = strings.TrimSpace(strings.TrimPrefix(line, "HCI Version:"))
		case strings.HasPrefix(line, "LMP Version:"):
			cur.LMPVersion = strings.TrimSpace(strings.TrimPrefix(line, "LMP Version:"))
		case strings.HasPrefix(line, "Manufacturer:"):
			cur.Manufacturer = strings.TrimSpace(strings.TrimPrefix(line, "Manufacturer:"))
		case strings.HasPrefix(line, "UP "), line == "UP", strings.HasPrefix(line, "DOWN"):
			cur.Powered = strings.HasPrefix(line, "UP")
			cur.Discoverable = strings.Contains(line, "ISCAN")
		}
	}
	flush()
	return adapters
}

// btMergeAdapterStats copies controller counters onto adapters discovered over
// D-Bus, matching on the controller address.
func btMergeAdapterStats(dst []BluetoothAdapter, stats []BluetoothAdapter) {
	for i := range dst {
		for _, s := range stats {
			if !strings.EqualFold(dst[i].Address, s.Address) && dst[i].ID != s.ID {
				continue
			}
			if dst[i].ID == "" {
				dst[i].ID = s.ID
			}
			dst[i].Bus, dst[i].Manufacturer = s.Bus, s.Manufacturer
			dst[i].HCIVersion, dst[i].LMPVersion = s.HCIVersion, s.LMPVersion
			dst[i].LinkPolicy, dst[i].LinkMode = s.LinkPolicy, s.LinkMode
			dst[i].RxBytes, dst[i].TxBytes = s.RxBytes, s.TxBytes
			dst[i].RxACL, dst[i].TxACL, dst[i].RxSCO, dst[i].TxSCO = s.RxACL, s.TxACL, s.RxSCO, s.TxSCO
			dst[i].Events, dst[i].Commands = s.Events, s.Commands
			dst[i].RxErrors, dst[i].TxErrors = s.RxErrors, s.TxErrors
			dst[i].HasStatistics = s.HasStatistics
			break
		}
	}
}

func btFieldAfter(line, key string) string {
	i := strings.Index(line, key)
	if i < 0 {
		return ""
	}
	return strings.Fields(line[i+len(key):])[0]
}

func btNumberAfter(line, key string) float64 {
	i := strings.Index(line, key)
	if i < 0 {
		return 0
	}
	fields := strings.Fields(line[i+len(key):])
	if len(fields) == 0 {
		return 0
	}
	v, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0
	}
	return v
}

// btLastNumber reads the value out of hcitool's one-line answers, e.g.
// "Link quality: 255" or "RSSI return value: -3".
func btLastNumber(s string) (float64, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	for i := len(fields) - 1; i >= 0; i-- {
		if v, err := strconv.ParseFloat(fields[i], 64); err == nil {
			return v, true
		}
	}
	return 0, false
}

// btParseAFHOutput reads the channel map from `hcitool afh`.
func btParseAFHOutput(s string) (string, bool) {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "AFH map:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "AFH map:")), true
	}
	return "", false
}

type btConnection struct {
	Address   string
	LinkType  string
	Handle    int
	Role      string
	LinkMode  string
	Encrypted *bool
}

// parseHcitoolConnections reads `hcitool con`:
//
//	< ACL AC:BF:71:11:22:33 handle 12 state 1 lm MASTER AUTH ENCRYPT
func parseHcitoolConnections(out string) []btConnection {
	var conns []btConnection
	for _, raw := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(raw))
		if len(fields) < 3 || (fields[0] != "<" && fields[0] != ">") {
			continue
		}
		c := btConnection{LinkType: fields[1], Address: btNormalizeAddress(fields[2])}
		if !btLooksLikeAddress(c.Address) {
			continue
		}
		var modes []string
		for i := 3; i < len(fields); i++ {
			switch fields[i] {
			case "handle":
				if i+1 < len(fields) {
					c.Handle = atoi(fields[i+1])
				}
			case "lm":
				modes = fields[i+1:]
			}
		}
		for _, m := range modes {
			switch m {
			case "MASTER":
				c.Role = "central (master)"
			case "SLAVE":
				c.Role = "peripheral (slave)"
			case "ENCRYPT":
				yes := true
				c.Encrypted = &yes
			case "SNIFF":
				c.LinkMode = "sniff"
			case "HOLD":
				c.LinkMode = "hold"
			case "PARK":
				c.LinkMode = "park"
			}
		}
		if c.LinkMode == "" {
			c.LinkMode = "active"
		}
		conns = append(conns, c)
	}
	return conns
}

// btPactlCard is what the audio server knows about one connected device.
type btPactlCard struct {
	Name  string
	Class string
	Audio *btAudioStream
}

// parsePactlBluetooth extracts the codec the audio server negotiated for each
// Bluetooth card or stream from `pactl list`. PipeWire's pulse server prints
// the same properties, so this covers both audio servers.
func parsePactlBluetooth(out string) map[string]*btPactlCard {
	result := map[string]*btPactlCard{}
	for _, block := range btSplitBlocks(out) {
		props := btPactlProperties(block)
		addr := ""
		for _, key := range []string{"api.bluez5.address", "device.string", "bluez.path"} {
			if v, ok := props[key]; ok && btLooksLikeAddress(v) {
				addr = btNormalizeAddress(v)
				break
			}
		}
		if addr == "" {
			if v, ok := props["bluez.path"]; ok && strings.Contains(v, "dev_") {
				addr = btNormalizeAddress(v)
			}
		}
		if addr == "" || !btLooksLikeAddress(addr) {
			continue
		}
		card := result[addr]
		if card == nil {
			card = &btPactlCard{Audio: &btAudioStream{}}
			result[addr] = card
		}
		audio := card.Audio
		card.Name = btPickString(card.Name, btPickString(props["bluez.alias"], props["device.description"]))
		card.Class = btPickString(card.Class, props["bluez.class"])
		codec := btPickString(props["bluetooth.codec"], props["api.bluez5.codec"])
		if codec == "" {
			codec = btPactlActiveProfileCodec(block)
		}
		if codec != "" {
			name := btCanonicalCodec(codec)
			if audio.Codec == "" || audio.BitrateKind != btBitrateNegotiated {
				audio.Codec = name
				btApplyNominalBitrate(audio)
			}
		}
		if profile, ok := props["api.bluez5.profile"]; ok {
			audio.Profile = btPactlProfileName(profile)
		} else if p := btPactlField(block, "Active Profile:"); p != "" {
			audio.Profile = btPactlProfileName(p)
		}
		if spec := btPactlField(block, "Sample Specification:"); spec != "" {
			rate, depth, channels := btParseSampleSpec(spec)
			if rate > 0 {
				audio.SampleRate = rate
			}
			if depth > 0 {
				audio.BitDepth = depth
			}
			if channels > 0 && audio.ChannelMode == "" {
				audio.ChannelMode = map[int]string{1: "mono", 2: "stereo"}[channels]
			}
		}
		if state := btPactlField(block, "State:"); state != "" && audio.State == "" {
			audio.State = strings.ToLower(state)
		}
	}
	return result
}

// btPactlProfileName turns "a2dp-sink-ldac" or "headset-head-unit-msbc" into
// the transport profile the stream runs on.
func btPactlProfileName(raw string) string {
	v := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(v, "a2dp"):
		return "A2DP"
	case strings.HasPrefix(v, "headset"), strings.HasPrefix(v, "handsfree"), strings.HasPrefix(v, "hfp"), strings.HasPrefix(v, "hsp"):
		return "HFP/HSP"
	case strings.HasPrefix(v, "bap"), strings.Contains(v, "le-audio"):
		return "LE Audio"
	}
	return raw
}

// btPactlActiveProfileCodec takes the codec suffix of the active profile, used
// when the server does not publish a bluetooth.codec property.
func btPactlActiveProfileCodec(block string) string {
	active := btPactlField(block, "Active Profile:")
	if active == "" || active == "off" {
		return ""
	}
	for _, prefix := range []string{"a2dp-sink-", "a2dp-source-", "headset-head-unit-", "headset-audio-gateway-"} {
		if strings.HasPrefix(active, prefix) {
			return strings.TrimPrefix(active, prefix)
		}
	}
	if active == "headset-head-unit" || active == "headset-audio-gateway" {
		return "cvsd"
	}
	if active == "a2dp-sink" || active == "a2dp-source" {
		return "sbc"
	}
	return ""
}

// btSplitBlocks splits pactl output on its unindented section headers.
func btSplitBlocks(out string) []string {
	var blocks []string
	var cur strings.Builder
	for _, line := range strings.Split(out, "\n") {
		if line != "" && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			if cur.Len() > 0 {
				blocks = append(blocks, cur.String())
				cur.Reset()
			}
		}
		cur.WriteString(line)
		cur.WriteByte('\n')
	}
	if cur.Len() > 0 {
		blocks = append(blocks, cur.String())
	}
	return blocks
}

// btPactlProperties collects the `key = "value"` property lines of a block.
func btPactlProperties(block string) map[string]string {
	props := map[string]string{}
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		key, value, ok := strings.Cut(line, " = ")
		if !ok || strings.Contains(key, " ") {
			continue
		}
		props[key] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return props
}

// btPactlField reads a `Key: value` line, ignoring the property lines.
func btPactlField(block, key string) string {
	for _, raw := range strings.Split(block, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, key) {
			return strings.TrimSpace(strings.TrimPrefix(line, key))
		}
	}
	return ""
}

// btParseSampleSpec reads PulseAudio's "s16le 2ch 44100Hz".
func btParseSampleSpec(spec string) (rate float64, depth, channels int) {
	for _, f := range strings.Fields(spec) {
		switch {
		case strings.HasSuffix(f, "Hz"):
			rate = firstFloat(strings.TrimSuffix(f, "Hz"))
		case strings.HasSuffix(f, "ch"):
			channels = atoi(strings.TrimSuffix(f, "ch"))
		case strings.HasPrefix(f, "s"), strings.HasPrefix(f, "u"), strings.HasPrefix(f, "f"):
			depth = int(firstFloat(f))
		}
	}
	if depth == 24 || depth == 32 || depth == 16 || depth == 8 {
		return rate, depth, channels
	}
	return rate, 0, channels
}
