package collect

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// probeDarwin reads system_profiler. The JSON report is preferred; the text
// report is kept because it still carries the radio fields (role, connection
// mode, AFH map, transmit power) that the JSON report dropped.
func (b *bluetoothCollector) probeDarwin(ctx context.Context) btSnapshot {
	snap := btSnapshot{Platform: "darwin"}
	index := newBTIndex()
	gotAny := false
	if out, err := b.exec(ctx, b.cfg.SystemProfiler, "-json", "SPBluetoothDataType"); err == nil {
		adapters, devices, err := parseSystemProfilerJSON(out)
		if err == nil {
			snap.Adapters = adapters
			for _, d := range devices {
				index.merge(d)
			}
			snap.Sources = append(snap.Sources, "system_profiler-json")
			gotAny = true
		}
	}
	if out, err := b.exec(ctx, b.cfg.SystemProfiler, "SPBluetoothDataType"); err == nil {
		devices := parseSystemProfilerText(string(out))
		for _, d := range devices {
			index.merge(d)
		}
		if len(devices) > 0 {
			snap.Sources = append(snap.Sources, "system_profiler-text")
			gotAny = true
		}
	} else if !gotAny {
		snap.Limits = append(snap.Limits, "system_profiler 不可用："+btErr(err))
	}
	snap.Devices = index.list()
	snap.Limits = append(snap.Limits,
		"macOS 未提供查询当前音频编解码器的命令行接口：编解码器与码率不在此报告中，请按住 Option 点击菜单栏蓝牙图标核对",
		"macOS 不提供控制器收发字节计数，没有控制器吞吐曲线")
	return snap
}

// parseSystemProfilerJSON reads `system_profiler -json SPBluetoothDataType`.
func parseSystemProfilerJSON(raw []byte) ([]BluetoothAdapter, []BluetoothDevice, error) {
	var doc struct {
		Data []map[string]any `json:"SPBluetoothDataType"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, err
	}
	var adapters []BluetoothAdapter
	var devices []BluetoothDevice
	for _, section := range doc.Data {
		if props := nestMap(section, "controller_properties"); props != nil {
			adapters = append(adapters, BluetoothAdapter{
				ID:           "controller",
				Address:      nestString(props, "controller_address"),
				Name:         nestString(props, "controller_chipset"),
				Powered:      btDarwinBool(nestString(props, "controller_state")),
				Discoverable: btDarwinBool(nestString(props, "controller_discoverable")),
				Bus:          nestString(props, "controller_transport"),
			})
		}
		for key, connected := range map[string]bool{"device_connected": true, "device_not_connected": false} {
			for _, entry := range nestSlice(section, key) {
				m, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				for name, v := range m {
					props, ok := v.(map[string]any)
					if !ok {
						continue
					}
					devices = append(devices, btDarwinDevice(name, props, connected))
				}
			}
		}
	}
	return adapters, devices, nil
}

// btDarwinBool reads the "attrib_on" / "attrib_Yes" style values.
func btDarwinBool(v string) bool {
	switch strings.ToLower(strings.TrimPrefix(v, "attrib_")) {
	case "on", "yes", "true", "1":
		return true
	}
	return false
}

func btDarwinDevice(name string, props map[string]any, connected bool) BluetoothDevice {
	d := BluetoothDevice{
		Name:         name,
		Address:      btNormalizeAddress(nestString(props, "device_address")),
		Connected:    connected || btDarwinBool(nestString(props, "device_isConnected")),
		Paired:       btDarwinBool(nestString(props, "device_isPaired")),
		DeviceType:   nestString(props, "device_minorType"),
		MajorClass:   nestString(props, "device_majorType"),
		MinorClass:   nestString(props, "device_minorType"),
		Firmware:     nestString(props, "device_firmwareVersion"),
		Manufacturer: nestString(props, "device_manufacturer"),
		Role:         btDarwinRole(nestString(props, "device_role")),
		LinkMode:     btDarwinLinkMode(nestString(props, "device_connectionMode")),
		Sources:      []string{"system_profiler"},
	}
	if services := nestString(props, "device_services"); services != "" {
		for _, s := range strings.Split(services, ",") {
			if s = strings.TrimSpace(s); s != "" {
				d.Profiles = append(d.Profiles, s)
			}
		}
	}
	if raw := nestString(props, "device_classOfDevice"); raw != "" {
		d.ClassOfDev = raw
		if class, ok := btParseClass(raw); ok {
			d.MajorClass, d.MinorClass, d.ServiceClass = btDecodeClass(class)
			d.DeviceType = d.MinorClass
		}
	}
	for _, key := range []string{"device_rssi", "device_RSSI"} {
		if v := nestFloat(props, key); v != 0 {
			rssi := v
			d.RSSI, d.RSSIKind = &rssi, "absolute"
			break
		}
	}
	if v := nestFloat(props, "device_transmitPowerLevel"); v != 0 {
		tx := v
		d.TxPower = &tx
	}
	for _, key := range []string{"device_batteryLevelMain", "device_batteryLevel", "device_batteryLevelLeft", "device_batteryLevelCase"} {
		if raw := nestString(props, key); raw != "" {
			if v, ok := btBatteryPercent(raw); ok {
				d.Battery = &v
				break
			}
		}
	}
	if raw := nestString(props, "device_addressType"); raw != "" && d.Transport == "" {
		d.Transport = btTransportForUUIDs(d.Profiles, raw)
	}
	return d
}

func btDarwinRole(v string) string {
	switch strings.ToLower(strings.TrimPrefix(v, "attrib_")) {
	case "master", "central":
		return "central (master)"
	case "slave", "peripheral":
		return "peripheral (slave)"
	}
	return ""
}

func btDarwinLinkMode(v string) string {
	s := strings.ToLower(strings.TrimPrefix(v, "attrib_"))
	switch {
	case strings.Contains(s, "sniff"):
		return "sniff"
	case strings.Contains(s, "hold"):
		return "hold"
	case strings.Contains(s, "park"):
		return "park"
	case strings.Contains(s, "active"):
		return "active"
	}
	return ""
}

// parseSystemProfilerText reads the indented report. A device is a header line
// ending in ":" whose nested keys include an address.
func parseSystemProfilerText(out string) []BluetoothDevice {
	var devices []BluetoothDevice
	header, headerIndent := "", -1
	fields := map[string]string{}
	flush := func() {
		if header != "" && fields["Address"] != "" {
			devices = append(devices, btDarwinTextDevice(header, fields))
		}
		header, headerIndent, fields = "", -1, map[string]string{}
	}
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		body := strings.TrimSpace(line)
		key, value, hasValue := strings.Cut(body, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		// A key with no value opens a section; the report nests the device
		// name under "Devices (Paired, Configured, etc.):", so the closest
		// preceding header owns the fields that follow it.
		if hasValue && value == "" {
			flush()
			header, headerIndent = key, indent
			continue
		}
		if !hasValue || headerIndent < 0 {
			continue
		}
		if indent <= headerIndent {
			flush()
			continue
		}
		fields[key] = value
	}
	flush()
	return devices
}

func btDarwinTextDevice(name string, f map[string]string) BluetoothDevice {
	d := BluetoothDevice{
		Name:         name,
		Address:      btNormalizeAddress(f["Address"]),
		Connected:    strings.EqualFold(f["Connected"], "Yes"),
		Paired:       strings.EqualFold(f["Paired"], "Yes"),
		MajorClass:   f["Major Type"],
		MinorClass:   f["Minor Type"],
		DeviceType:   f["Minor Type"],
		LinkType:     f["Link Type"],
		Role:         btDarwinRole(f["Role"]),
		LinkMode:     btDarwinLinkMode(f["Connection Mode"]),
		Firmware:     f["Firmware Version"],
		Manufacturer: f["Manufacturer"],
		Sources:      []string{"system_profiler"},
	}
	if raw := f["Class of Device"]; raw != "" {
		d.ClassOfDev = raw
		if class, ok := btParseClass(raw); ok {
			d.MajorClass, d.MinorClass, d.ServiceClass = btDecodeClass(class)
			d.DeviceType = d.MinorClass
		}
	}
	for _, s := range strings.Split(f["Services"], ",") {
		if s = strings.TrimSpace(s); s != "" {
			d.Profiles = append(d.Profiles, s)
		}
	}
	if v, err := strconv.ParseFloat(f["RSSI"], 64); err == nil {
		d.RSSI, d.RSSIKind = &v, "absolute"
	}
	if v, err := strconv.ParseFloat(f["Transmit Power Level"], 64); err == nil {
		d.TxPower = &v
	}
	if raw := f["Interval"]; raw != "" {
		if v := firstFloat(raw); v > 0 {
			d.IntervalMs = &v
		}
	}
	if raw := f["AFH Map"]; raw != "" {
		d.AFHMap = raw
		if n, ok := btAFHChannels(raw); ok {
			d.AFHChannels = &n
		}
	}
	if raw := f["Battery Level"]; raw != "" {
		if v, ok := btBatteryPercent(raw); ok {
			d.Battery = &v
		}
	}
	d.Transport = btTransportForUUIDs(d.Profiles, "")
	if d.Transport == "" && d.LinkType != "" {
		if strings.EqualFold(d.LinkType, "LE") {
			d.Transport = "LE"
		} else {
			d.Transport = "BR/EDR"
		}
	}
	return d
}

// windowsBluetoothScript lists present Bluetooth PnP devices with the battery
// property when the driver publishes one.
const windowsBluetoothScript = `Get-PnpDevice -Class Bluetooth -PresentOnly | ForEach-Object { ` +
	`$b = (Get-PnpDeviceProperty -InstanceId $_.InstanceId -KeyName '{104EA319-6EE2-4701-BD47-8DDBF425BBE5} 2' -ErrorAction SilentlyContinue).Data; ` +
	`[pscustomobject]@{ Name = $_.FriendlyName; InstanceId = $_.InstanceId; Status = $_.Status; Battery = $b } } | ConvertTo-Json -Compress`

func (b *bluetoothCollector) probeWindows(ctx context.Context) btSnapshot {
	snap := btSnapshot{Platform: "windows"}
	out, err := b.exec(ctx, b.cfg.PowerShell, "-NoProfile", "-NonInteractive", "-Command", windowsBluetoothScript)
	if err != nil {
		snap.Limits = append(snap.Limits, "PowerShell 查询失败："+btErr(err))
		return snap
	}
	adapters, devices := parseWindowsPnpBluetooth(out)
	snap.Adapters, snap.Devices = adapters, devices
	snap.Sources = append(snap.Sources, "powershell-pnp")
	snap.Limits = append(snap.Limits,
		"Windows 即插即用接口不提供音频编解码器、RSSI、链路质量和控制器字节计数，本平台只报告设备清单、连接状态与电量")
	return snap
}

type windowsPnpDevice struct {
	Name       string `json:"Name"`
	InstanceID string `json:"InstanceId"`
	Status     string `json:"Status"`
	Battery    *int   `json:"Battery"`
}

func parseWindowsPnpBluetooth(raw []byte) ([]BluetoothAdapter, []BluetoothDevice) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil, nil
	}
	var rows []windowsPnpDevice
	if strings.HasPrefix(trimmed, "{") {
		var one windowsPnpDevice
		if err := json.Unmarshal([]byte(trimmed), &one); err != nil {
			return nil, nil
		}
		rows = []windowsPnpDevice{one}
	} else if err := json.Unmarshal([]byte(trimmed), &rows); err != nil {
		return nil, nil
	}
	var adapters []BluetoothAdapter
	var devices []BluetoothDevice
	for _, r := range rows {
		id := strings.ToUpper(r.InstanceID)
		if strings.HasPrefix(id, "USB\\") || strings.HasPrefix(id, "PCI\\") {
			adapters = append(adapters, BluetoothAdapter{ID: r.Name, Name: r.Name, Powered: r.Status == "OK"})
			continue
		}
		addr := btWindowsAddress(id)
		if addr == "" {
			continue
		}
		d := BluetoothDevice{
			Name: r.Name, Address: addr, Paired: true,
			Connected: strings.EqualFold(r.Status, "OK"),
			Sources:   []string{"powershell-pnp"},
		}
		if strings.Contains(id, "\\DEV_") {
			d.Transport = "BR/EDR"
		}
		if strings.HasPrefix(id, "BTHLE") {
			d.Transport = "LE"
		}
		if r.Battery != nil {
			v := float64(*r.Battery)
			d.Battery = &v
		}
		devices = append(devices, d)
	}
	return adapters, devices
}

// btWindowsAddress pulls the address out of "BTHENUM\DEV_ACBF71112233\...".
func btWindowsAddress(instanceID string) string {
	i := strings.Index(instanceID, "DEV_")
	if i < 0 {
		return ""
	}
	rest := instanceID[i+len("DEV_"):]
	if j := strings.IndexAny(rest, `\&`); j >= 0 {
		rest = rest[:j]
	}
	if len(rest) != 12 {
		return ""
	}
	var parts []string
	for k := 0; k < 12; k += 2 {
		parts = append(parts, rest[k:k+2])
	}
	addr := strings.ToUpper(strings.Join(parts, ":"))
	if !btLooksLikeAddress(addr) {
		return ""
	}
	return addr
}

// probeAndroid reads the Bluetooth manager dump, then falls back to the Linux
// tools for anything the dump does not carry.
func (b *bluetoothCollector) probeAndroid(ctx context.Context) btSnapshot {
	snap := btSnapshot{Platform: "android"}
	out, err := b.exec(ctx, b.cfg.Dumpsys, "bluetooth_manager")
	if err != nil {
		snap.Limits = append(snap.Limits, "dumpsys bluetooth_manager 不可用（通常需要 shell 权限）："+btErr(err))
		linux := b.probeLinux(ctx)
		linux.Platform = "android"
		linux.Limits = append(snap.Limits, linux.Limits...)
		return linux
	}
	adapters, devices := parseDumpsysBluetooth(string(out))
	snap.Adapters, snap.Devices = adapters, devices
	snap.Sources = append(snap.Sources, "dumpsys")
	snap.Limits = append(snap.Limits, "Android 的 dumpsys 不提供 RSSI、链路质量与控制器字节计数，评分仅基于编解码器配置")
	return snap
}

// parseDumpsysBluetooth reads `dumpsys bluetooth_manager`: the bonded device
// list plus the A2DP codec status block, which is Android's own view of the
// negotiated codec, sample rate, bit depth and LDAC quality index.
func parseDumpsysBluetooth(out string) ([]BluetoothAdapter, []BluetoothDevice) {
	index := newBTIndex()
	adapter := BluetoothAdapter{ID: "bluetooth_manager"}
	lastAddress := ""
	inBonded := false
	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		key, value, hasValue := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch {
		case strings.HasPrefix(line, "Bonded devices:"):
			inBonded = true
			continue
		case hasValue && key == "enabled":
			adapter.Powered = value == "true"
		case hasValue && key == "state" && adapter.Name == "":
			adapter.Powered = adapter.Powered || strings.EqualFold(value, "ON")
		case hasValue && key == "address" && adapter.Address == "":
			adapter.Address = btNormalizeAddress(value)
		case hasValue && key == "name" && adapter.Name == "":
			adapter.Name = value
		}
		if inBonded {
			if d, ok := btParseBondedLine(line); ok {
				index.merge(d)
				continue
			}
			inBonded = false
		}
		if addr, ok := btFirstAddress(line); ok {
			lastAddress = addr
			if strings.Contains(line, "mCurrentDevice") || strings.Contains(line, "Connected") {
				d := index.get(addr)
				d.Connected = true
				d.Sources = append(d.Sources, "dumpsys")
			}
		}
		if strings.Contains(line, "codecName:") {
			stream := parseAndroidCodecConfig(line)
			target := lastAddress
			if target == "" {
				target = btSingleAudioAddress(index)
			}
			if target != "" && stream != nil {
				index.applyAudio(target, stream, "dumpsys")
				d := index.get(target)
				d.Connected = true
			}
		}
	}
	return []BluetoothAdapter{adapter}, index.list()
}

// btParseBondedLine reads "AC:BF:71:11:22:33 [ AUDIO_VIDEO_LOUDSPEAKER ] Flip 5".
func btParseBondedLine(line string) (BluetoothDevice, bool) {
	fields := strings.Fields(line)
	if len(fields) == 0 || !btLooksLikeAddress(fields[0]) {
		return BluetoothDevice{}, false
	}
	d := BluetoothDevice{Address: btNormalizeAddress(fields[0]), Paired: true, Sources: []string{"dumpsys"}}
	rest := strings.TrimSpace(strings.TrimPrefix(line, fields[0]))
	if open := strings.Index(rest, "["); open >= 0 {
		if close := strings.Index(rest, "]"); close > open {
			d.DeviceType = btAndroidDeviceType(strings.TrimSpace(rest[open+1 : close]))
			rest = strings.TrimSpace(rest[close+1:])
		}
	}
	d.Name = rest
	return d, true
}

// btAndroidDeviceType turns AOSP's BluetoothClass constant names into the same
// vocabulary the Class of Device decoder produces.
func btAndroidDeviceType(constant string) string {
	name := strings.TrimPrefix(strings.TrimPrefix(constant, "AUDIO_VIDEO_"), "PERIPHERAL_")
	name = strings.TrimSuffix(name, "_DEVICE")
	name = strings.ReplaceAll(strings.ToLower(name), "_", " ")
	switch name {
	case "loudspeaker":
		return "Loudspeaker"
	case "headphones":
		return "Headphones"
	case "wearable headset":
		return "Wearable Headset"
	case "handsfree":
		return "Hands-free Device"
	case "portable audio":
		return "Portable Audio"
	case "car audio":
		return "Car Audio"
	case "keyboard":
		return "Keyboard"
	case "pointing":
		return "Pointing Device"
	}
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}

func btFirstAddress(line string) (string, bool) {
	for _, f := range strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '=' || r == ',' || r == '[' || r == ']' || r == '{' || r == '}'
	}) {
		if btLooksLikeAddress(f) {
			return btNormalizeAddress(f), true
		}
	}
	return "", false
}

func btSingleAudioAddress(index *btIndex) string {
	found := ""
	for _, addr := range index.order {
		d := index.byAddr[addr]
		if btTypeBucket(d.DeviceType) != "audio" {
			continue
		}
		if found != "" {
			return ""
		}
		found = addr
	}
	return found
}

// parseAndroidCodecConfig reads one AOSP BluetoothCodecConfig dump line.
func parseAndroidCodecConfig(line string) *btAudioStream {
	fields := btAndroidFields(line)
	name := fields["codecName"]
	if name == "" {
		return nil
	}
	s := &btAudioStream{Codec: btCanonicalCodec(name), Profile: "A2DP"}
	s.SampleRate = btAndroidValue(fields["mSampleRate"])
	s.BitDepth = int(btAndroidValue(fields["mBitsPerSample"]))
	switch strings.ToUpper(btAndroidLabel(fields["mChannelMode"])) {
	case "STEREO":
		s.ChannelMode = "stereo"
	case "MONO":
		s.ChannelMode = "mono"
	}
	btApplyNominalBitrate(s)
	if s.Codec == "LDAC" {
		if q, err := strconv.ParseInt(strings.TrimSpace(fields["mCodecSpecific1"]), 10, 64); err == nil {
			if kbps, mode := btLDACBitrate(q, s.SampleRate); mode != "" {
				s.QualityMode = mode
				if kbps > 0 {
					s.Bitrate, s.BitrateKind = kbps, btBitrateNegotiated
				} else {
					s.Notes = append(s.Notes, "LDAC 处于自适应码率（ABR）模式，实际码率随链路变化")
				}
			}
		}
	}
	return s
}

// btAndroidFields splits "key:value" pairs out of a dumped config object.
func btAndroidFields(line string) map[string]string {
	start := strings.Index(line, "{")
	if start >= 0 {
		line = line[start+1:]
	}
	line = strings.TrimRight(strings.TrimSpace(line), "}")
	out := map[string]string{}
	for _, part := range strings.Split(line, ",") {
		key, value, ok := strings.Cut(part, ":")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	return out
}

// btAndroidValue reads "0x4(96000)" style fields, preferring the decoded value
// in parentheses.
func btAndroidValue(raw string) float64 {
	if open := strings.Index(raw, "("); open >= 0 {
		if close := strings.Index(raw, ")"); close > open {
			return firstFloat(raw[open+1 : close])
		}
	}
	return firstFloat(raw)
}

func btAndroidLabel(raw string) string {
	if open := strings.Index(raw, "("); open >= 0 {
		if close := strings.Index(raw, ")"); close > open {
			return strings.TrimSpace(raw[open+1 : close])
		}
	}
	return strings.TrimSpace(raw)
}

// probeSnapshotDir replays command output captured on another machine. Files
// are named after the command and its arguments, e.g. hcitool_lq_AC_BF_71.txt,
// so every probe path behaves exactly as it does against a live host. It makes
// a Bluetooth report reproducible on a server that has no radio of its own.
func (b *bluetoothCollector) probeSnapshotDir() btSnapshot {
	dir := b.cfg.SnapshotDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		return btSnapshot{Platform: "snapshot", Limits: []string{"snapshot_dir 无法读取：" + err.Error()}}
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	saved := b.run
	b.run = btSnapshotRunner(dir, names)
	defer func() { b.run = saved }()
	platform := btSnapshotPlatform(dir, names)
	var snap btSnapshot
	switch platform {
	case "darwin":
		snap = b.probeDarwin(context.Background())
	case "windows":
		snap = b.probeWindows(context.Background())
	case "android":
		snap = b.probeAndroid(context.Background())
	default:
		snap = b.probeLinux(context.Background())
	}
	snap.Platform = platform + " (snapshot)"
	snap.Limits = append(snap.Limits, "数据来自 snapshot_dir 录制的命令输出，不是本机实时采集")
	return snap
}

func btSnapshotPlatform(dir string, names []string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "platform")); err == nil {
		if p := strings.TrimSpace(string(b)); p != "" {
			return p
		}
	}
	for _, n := range names {
		switch {
		case strings.HasPrefix(n, "system_profiler"):
			return "darwin"
		case strings.HasPrefix(n, "dumpsys"):
			return "android"
		case strings.HasPrefix(n, "powershell"):
			return "windows"
		}
	}
	return "linux"
}

// btSnapshotRunner resolves a command invocation to a recorded file: the most
// specific name wins, so hcitool_lq_AC_BF_71_11_22_33 is preferred over
// hcitool_lq and then over hcitool.
func btSnapshotRunner(dir string, names []string) func(context.Context, string, ...string) ([]byte, error) {
	return func(_ context.Context, name string, args ...string) ([]byte, error) {
		parts := append([]string{filepath.Base(name)}, args...)
		for i := len(parts); i > 0; i-- {
			candidate := btSafeToken(strings.Join(parts[:i], "_"))
			for _, n := range names {
				base := strings.TrimSuffix(n, filepath.Ext(n))
				if strings.EqualFold(base, candidate) || strings.EqualFold(n, candidate) {
					return os.ReadFile(filepath.Join(dir, n))
				}
			}
		}
		return nil, fmt.Errorf("snapshot: no recorded output for %q", strings.Join(parts, " "))
	}
}
