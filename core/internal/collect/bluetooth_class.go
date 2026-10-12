package collect

import (
	"fmt"
	"math/bits"
	"strconv"
	"strings"
)

// Class of Device is a 24-bit field (Bluetooth Assigned Numbers, Baseband):
// bits 0–1 format, bits 2–7 minor device class, bits 8–12 major device class
// and bits 13–23 major service classes. Decoding it is how a Bluetooth tester
// tells a speaker from a keyboard without trusting the advertised name.
var btMajorClasses = map[uint32]string{
	0:  "Miscellaneous",
	1:  "Computer",
	2:  "Phone",
	3:  "LAN/Network Access Point",
	4:  "Audio/Video",
	5:  "Peripheral",
	6:  "Imaging",
	7:  "Wearable",
	8:  "Toy",
	9:  "Health",
	31: "Uncategorized",
}

var btAudioMinorClasses = map[uint32]string{
	0:  "Uncategorized Audio",
	1:  "Wearable Headset",
	2:  "Hands-free Device",
	4:  "Microphone",
	5:  "Loudspeaker",
	6:  "Headphones",
	7:  "Portable Audio",
	8:  "Car Audio",
	9:  "Set-top Box",
	10: "HiFi Audio Device",
	11: "VCR",
	12: "Video Camera",
	13: "Camcorder",
	14: "Video Monitor",
	15: "Video Display and Loudspeaker",
	16: "Video Conferencing",
	18: "Gaming/Toy",
}

var btComputerMinorClasses = map[uint32]string{
	0: "Uncategorized Computer", 1: "Desktop Workstation", 2: "Server", 3: "Laptop",
	4: "Handheld PC/PDA", 5: "Palm-size PC/PDA", 6: "Wearable Computer", 7: "Tablet",
}

var btPhoneMinorClasses = map[uint32]string{
	0: "Uncategorized Phone", 1: "Cellular", 2: "Cordless", 3: "Smartphone",
	4: "Wired Modem/Voice Gateway", 5: "Common ISDN Access",
}

var btPeripheralMinorClasses = map[uint32]string{
	0: "Uncategorized Peripheral", 1: "Joystick", 2: "Gamepad", 3: "Remote Control",
	4: "Sensing Device", 5: "Digitizer Tablet", 6: "Card Reader", 7: "Digital Pen",
	8: "Handheld Scanner", 9: "Handheld Gestural Input",
}

var btWearableMinorClasses = map[uint32]string{
	1: "Wristwatch", 2: "Pager", 3: "Jacket", 4: "Helmet", 5: "Glasses",
}

var btServiceClasses = []struct {
	bit  uint
	name string
}{
	{13, "Limited Discoverable Mode"}, {14, "LE Audio"}, {16, "Positioning"},
	{17, "Networking"}, {18, "Rendering"}, {19, "Capturing"}, {20, "Object Transfer"},
	{21, "Audio"}, {22, "Telephony"}, {23, "Information"},
}

// btDecodeClass returns the human-readable major class, minor class and the
// service classes advertised in a Class of Device value.
func btDecodeClass(class uint32) (major, minor string, services []string) {
	majorID := (class >> 8) & 0x1F
	minorID := (class >> 2) & 0x3F
	major = btMajorClasses[majorID]
	if major == "" {
		major = fmt.Sprintf("Major 0x%02X", majorID)
	}
	switch majorID {
	case 1:
		minor = btComputerMinorClasses[minorID]
	case 2:
		minor = btPhoneMinorClasses[minorID]
	case 4:
		minor = btAudioMinorClasses[minorID]
	case 5:
		// The top two bits of the peripheral minor class are keyboard and
		// pointing-device flags; the low nibble names the device.
		switch minorID >> 4 {
		case 1:
			minor = "Keyboard"
		case 2:
			minor = "Pointing Device"
		case 3:
			minor = "Combo Keyboard/Pointing Device"
		}
		if name := btPeripheralMinorClasses[minorID&0x0F]; name != "" && name != "Uncategorized Peripheral" {
			if minor == "" {
				minor = name
			} else {
				minor += " / " + name
			}
		}
	case 7:
		minor = btWearableMinorClasses[minorID]
	}
	if minor == "" {
		minor = fmt.Sprintf("Minor 0x%02X", minorID)
	}
	for _, s := range btServiceClasses {
		if class&(1<<s.bit) != 0 {
			services = append(services, s.name)
		}
	}
	return major, minor, services
}

// btParseClass accepts the textual Class of Device forms the stacks print:
// "0x240414", "0x00240414" and macOS's "0x04 0x01 0x240414".
func btParseClass(s string) (uint32, bool) {
	fields := strings.Fields(strings.TrimSpace(s))
	if len(fields) == 0 {
		return 0, false
	}
	raw := strings.TrimPrefix(strings.ToLower(fields[len(fields)-1]), "0x")
	v, err := strconv.ParseUint(raw, 16, 32)
	if err != nil {
		return 0, false
	}
	return uint32(v), true
}

// btProfileUUIDs maps the 16-bit service UUIDs that identify the profiles a
// connected device actually offers.
var btProfileUUIDs = map[uint16]string{
	0x1101: "Serial Port",
	0x1105: "OBEX Object Push",
	0x1106: "OBEX File Transfer",
	0x1108: "Headset (HSP)",
	0x110A: "Audio Source (A2DP)",
	0x110B: "Audio Sink (A2DP)",
	0x110C: "AVRCP Target",
	0x110D: "Advanced Audio Distribution (A2DP)",
	0x110E: "AVRCP Controller",
	0x1112: "Headset Audio Gateway",
	0x111E: "Hands-Free (HFP)",
	0x111F: "Hands-Free Audio Gateway",
	0x1124: "Human Interface Device (HID)",
	0x112F: "Phonebook Access Server",
	0x1132: "Message Access Server",
	0x1200: "PnP Information",
	0x1800: "Generic Access",
	0x180A: "Device Information",
	0x180F: "Battery Service",
	0x1812: "HID over GATT (HOGP)",
	0x1844: "Volume Control (LE Audio)",
	0x184D: "Microphone Control (LE Audio)",
	0x184E: "Audio Stream Control (LE Audio)",
	0x1850: "Published Audio Capabilities (LE Audio)",
	0x1853: "Common Audio Service (LE Audio)",
	0x1854: "Hearing Access (LE Audio)",
}

// btProfileForUUID names a 128-bit Bluetooth base UUID when it is one of the
// assigned profiles; vendor UUIDs return "".
func btProfileForUUID(uuid string) string {
	u := strings.ToLower(strings.TrimSpace(uuid))
	if !strings.HasSuffix(u, "-0000-1000-8000-00805f9b34fb") || len(u) < 8 {
		return ""
	}
	v, err := strconv.ParseUint(u[4:8], 16, 32)
	if err != nil || u[:4] != "0000" {
		return ""
	}
	return btProfileUUIDs[uint16(v)]
}

// btTransportForUUIDs infers which radio the device is using. BlueZ does not
// expose the transport directly, so classic audio/HID profiles mean BR/EDR and
// GATT-only services mean LE.
func btTransportForUUIDs(profiles []string, addressType string) string {
	classic, le := false, false
	for _, p := range profiles {
		switch {
		case strings.Contains(p, "A2DP"), strings.Contains(p, "HFP"), strings.Contains(p, "HSP"),
			strings.Contains(p, "AVRCP"), p == "Human Interface Device (HID)", p == "Serial Port":
			classic = true
		case strings.Contains(p, "LE Audio"), strings.Contains(p, "HOGP"), p == "Battery Service":
			le = true
		}
	}
	switch {
	case classic && le:
		return "dual (BR/EDR + LE)"
	case classic:
		return "BR/EDR"
	case le:
		return "LE"
	case strings.EqualFold(addressType, "random"):
		return "LE"
	case strings.EqualFold(addressType, "public"):
		return "BR/EDR"
	}
	return ""
}

// btDeviceTypeFromIcon maps the freedesktop icon BlueZ assigns from the Class
// of Device, used when the raw class is unavailable.
var btDeviceTypeFromIcon = map[string]string{
	"audio-headset":    "Headset",
	"audio-headphones": "Headphones",
	"audio-card":       "Loudspeaker",
	"audio-speakers":   "Loudspeaker",
	"input-keyboard":   "Keyboard",
	"input-mouse":      "Pointing Device",
	"input-tablet":     "Digitizer Tablet",
	"input-gaming":     "Gamepad",
	"phone":            "Phone",
	"computer":         "Computer",
	"camera-photo":     "Camera",
	"camera-video":     "Video Camera",
	"printer":          "Printer",
	"scanner":          "Scanner",
	"video-display":    "Video Monitor",
	"multimedia-playe": "Portable Audio",
}

// btAFHChannels counts the usable channels in an AFH channel map. The map is
// 10 bytes carrying 79 channel bits; fewer usable channels means the adapter
// is avoiding more of the 2.4 GHz band because of interference.
func btAFHChannels(hexMap string) (float64, bool) {
	s := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(hexMap)), "0x")
	s = strings.ReplaceAll(s, " ", "")
	if len(s) < 2 || len(s)%2 != 0 {
		return 0, false
	}
	total := 0
	for i := 0; i+1 < len(s); i += 2 {
		v, err := strconv.ParseUint(s[i:i+2], 16, 8)
		if err != nil {
			return 0, false
		}
		total += bits.OnesCount8(uint8(v))
	}
	if total > 79 {
		total = 79
	}
	return float64(total), true
}
