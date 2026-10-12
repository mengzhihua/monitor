package collect

import (
	"path"
	"strconv"
	"strings"
)

// btDBusValue is one decoded property value from a dbus-send reply.
type btDBusValue struct {
	Kind  string
	Str   string
	Num   float64
	Bool  bool
	Bytes []byte
	Array []string
}

// btDBusObject is one entry of org.freedesktop.DBus.ObjectManager's reply.
type btDBusObject struct {
	Path       string
	Interfaces map[string]map[string]btDBusValue
}

// parseDBusManagedObjects reads the textual reply dbus-send prints for
// GetManagedObjects. The reply nests as
//
//	array [ dict entry( object path, array [ dict entry( interface,
//	    array [ dict entry( property, variant value ) ] ) ] ) ]
//
// so the brace depth of a line says whether it names an object, an interface
// or a property. Quoted text is removed before counting, otherwise a device
// named "Speaker (left)" would shift every following depth.
func parseDBusManagedObjects(out string) []btDBusObject {
	var objs []btDBusObject
	curIdx := -1
	iface, key := "", ""
	depth := 0
	// An array value spans several lines; collect its elements until the
	// reply returns to the depth the array was opened at.
	var pending btDBusValue
	pendingKey, pendingIface, pendingIdx, pendingDepth := "", "", -1, -1

	publish := func() {
		if pendingIdx >= 0 && pendingIface != "" {
			objs[pendingIdx].Interfaces[pendingIface][pendingKey] = pending
		}
		pendingIdx, pendingDepth, pendingIface, pendingKey = -1, -1, "", ""
		pending = btDBusValue{}
	}

	for _, raw := range strings.Split(out, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "method return") {
			continue
		}
		stripped := btStripQuoted(line)
		before := depth
		depth += strings.Count(stripped, "[") + strings.Count(stripped, "(")
		depth -= strings.Count(stripped, "]") + strings.Count(stripped, ")")

		if pendingDepth >= 0 {
			if before >= pendingDepth {
				btAppendArrayElement(&pending, line)
				continue
			}
			publish()
		}
		switch {
		case before == 2 && strings.HasPrefix(line, "object path "):
			objs = append(objs, btDBusObject{Path: btDBusQuoted(line), Interfaces: map[string]map[string]btDBusValue{}})
			curIdx = len(objs) - 1
			iface, key = "", ""
		case before == 4 && strings.HasPrefix(line, "string "):
			iface = btDBusQuoted(line)
			if curIdx >= 0 && iface != "" {
				objs[curIdx].Interfaces[iface] = map[string]btDBusValue{}
			}
			key = ""
		case before == 5 && strings.HasPrefix(line, "dict entry("):
			key = ""
		case before == 6 && key == "" && strings.HasPrefix(line, "string "):
			key = btDBusQuoted(line)
		case before == 6 && key != "" && strings.HasPrefix(line, "variant"):
			if curIdx < 0 || iface == "" {
				key = ""
				continue
			}
			v, open := btParseDBusValue(strings.TrimSpace(strings.TrimPrefix(line, "variant")))
			objs[curIdx].Interfaces[iface][key] = v
			if open {
				pending, pendingKey, pendingIface, pendingIdx, pendingDepth = v, key, iface, curIdx, depth
			}
			key = ""
		}
	}
	if pendingDepth >= 0 {
		publish()
	}
	return objs
}

// btAppendArrayElement adds one element line to an array-valued property.
func btAppendArrayElement(v *btDBusValue, line string) {
	switch {
	case strings.HasPrefix(line, "string "), strings.HasPrefix(line, "object path "):
		v.Array = append(v.Array, btDBusQuoted(line))
	case strings.HasPrefix(line, "byte "):
		if n, err := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "byte ")), 10, 8); err == nil {
			v.Bytes = append(v.Bytes, byte(n))
		}
	default:
		v.Bytes = append(v.Bytes, btParseByteList(line)...)
	}
}

// btParseByteList reads the "33 21 2 53" form dbus-send uses for byte arrays.
// Hexadecimal is accepted too, since some builds print 0x-prefixed bytes.
func btParseByteList(line string) []byte {
	fields := strings.Fields(strings.Trim(line, "[],"))
	if len(fields) == 0 {
		return nil
	}
	out := make([]byte, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSuffix(f, ",")
		base := 10
		if strings.HasPrefix(f, "0x") || strings.HasPrefix(f, "0X") {
			f, base = f[2:], 16
		}
		n, err := strconv.ParseUint(f, base, 16)
		if err != nil || n > 255 {
			return nil
		}
		out = append(out, byte(n))
	}
	return out
}

// btParseDBusValue decodes a scalar; open reports that the value is an array
// whose elements follow on the next lines.
func btParseDBusValue(rest string) (v btDBusValue, open bool) {
	switch {
	case strings.HasPrefix(rest, "string "):
		return btDBusValue{Kind: "string", Str: btDBusQuoted(rest)}, false
	case strings.HasPrefix(rest, "object path "):
		return btDBusValue{Kind: "object_path", Str: btDBusQuoted(rest)}, false
	case strings.HasPrefix(rest, "boolean "):
		return btDBusValue{Kind: "boolean", Bool: strings.TrimSpace(strings.TrimPrefix(rest, "boolean")) == "true"}, false
	case strings.HasPrefix(rest, "array of bytes ["), strings.HasPrefix(rest, "array ["):
		v = btDBusValue{Kind: "array"}
		if i := strings.Index(rest, "["); i >= 0 && strings.Contains(rest[i:], "]") {
			inner := rest[i+1 : strings.LastIndex(rest, "]")]
			v.Bytes = btParseByteList(inner)
			return v, false
		}
		return v, true
	}
	for _, prefix := range []string{"byte ", "uint16 ", "int16 ", "uint32 ", "int32 ", "uint64 ", "int64 ", "double "} {
		if !strings.HasPrefix(rest, prefix) {
			continue
		}
		n, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(rest, prefix)), 64)
		if err != nil {
			return btDBusValue{Kind: "unknown", Str: rest}, false
		}
		return btDBusValue{Kind: "number", Num: n}, false
	}
	return btDBusValue{Kind: "unknown", Str: rest}, false
}

func btDBusQuoted(line string) string {
	first := strings.Index(line, `"`)
	last := strings.LastIndex(line, `"`)
	if first < 0 || last <= first {
		return ""
	}
	return line[first+1 : last]
}

func btStripQuoted(line string) string {
	var b strings.Builder
	inQuotes := false
	for i := 0; i < len(line); i++ {
		if line[i] == '"' {
			inQuotes = !inQuotes
			continue
		}
		if !inQuotes {
			b.WriteByte(line[i])
		}
	}
	return b.String()
}

// btFromDBus turns the BlueZ object tree into adapters and devices. The
// MediaTransport1 interface carries the codec and the raw configuration the
// two endpoints agreed on, which is what makes a reported bitrate a
// measurement of this link rather than a table lookup.
func btFromDBus(objs []btDBusObject) ([]BluetoothAdapter, []BluetoothDevice) {
	var adapters []BluetoothAdapter
	index := newBTIndex()
	byPath := map[string]string{} // object path → device address

	for _, obj := range objs {
		if props, ok := obj.Interfaces["org.bluez.Adapter1"]; ok {
			adapters = append(adapters, BluetoothAdapter{
				ID:           path.Base(obj.Path),
				Address:      props["Address"].Str,
				Name:         btPickString(props["Alias"].Str, props["Name"].Str),
				Powered:      props["Powered"].Bool,
				Discoverable: props["Discoverable"].Bool,
			})
		}
		props, ok := obj.Interfaces["org.bluez.Device1"]
		if !ok {
			continue
		}
		d := BluetoothDevice{
			Address:   btNormalizeAddress(btPickString(props["Address"].Str, obj.Path)),
			Name:      btPickString(props["Alias"].Str, props["Name"].Str),
			Connected: props["Connected"].Bool,
			Paired:    props["Paired"].Bool || props["Bonded"].Bool,
			Adapter:   path.Base(props["Adapter"].Str),
			Sources:   []string{"bluez-dbus"},
		}
		if v, ok := props["Class"]; ok && v.Num > 0 {
			class := uint32(v.Num)
			d.ClassOfDev = "0x" + strconv.FormatUint(uint64(class), 16)
			d.MajorClass, d.MinorClass, d.ServiceClass = btDecodeClass(class)
		}
		for _, uuid := range props["UUIDs"].Array {
			if name := btProfileForUUID(uuid); name != "" {
				d.Profiles = append(d.Profiles, name)
			}
		}
		d.DeviceType = btDeviceType(d.MinorClass, props["Icon"].Str, d.Profiles)
		d.Transport = btTransportForUUIDs(d.Profiles, props["AddressType"].Str)
		if v, ok := props["RSSI"]; ok && v.Kind == "number" {
			rssi := v.Num
			d.RSSI, d.RSSIKind = &rssi, "absolute"
		}
		if v, ok := props["TxPower"]; ok && v.Kind == "number" {
			tx := v.Num
			d.TxPower = &tx
		}
		if batt, ok := obj.Interfaces["org.bluez.Battery1"]; ok {
			if v, ok := batt["Percentage"]; ok && v.Kind == "number" {
				pct := v.Num
				d.Battery = &pct
			}
		}
		byPath[obj.Path] = d.Address
		index.merge(d)
	}

	for _, obj := range objs {
		props, ok := obj.Interfaces["org.bluez.MediaTransport1"]
		if !ok {
			continue
		}
		devicePath := props["Device"].Str
		addr, ok := byPath[devicePath]
		if !ok {
			addr = btNormalizeAddress(devicePath)
		}
		codecByte := byte(props["Codec"].Num)
		stream := btDecodeA2DPConfig(codecByte, props["Configuration"].Bytes)
		stream.State = props["State"].Str
		if v, ok := props["Delay"]; ok && v.Num > 0 {
			stream.DelayMs = v.Num / 10 // BlueZ reports 1/10 ms
		}
		if v, ok := props["Volume"]; ok && v.Kind == "number" {
			stream.Volume, stream.HasVolume = v.Num/127*100, true
		}
		if uuid := props["UUID"].Str; uuid != "" {
			if name := btProfileForUUID(uuid); name != "" {
				stream.Profile = btPactlProfileName(name)
			}
		}
		index.applyAudio(addr, &stream, "bluez-dbus")
	}
	return adapters, index.list()
}
