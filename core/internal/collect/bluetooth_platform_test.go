package collect

import (
	"strings"
	"testing"
)

const systemProfilerJSONFixture = `{
  "SPBluetoothDataType": [
    {
      "controller_properties": {
        "controller_address": "F0:18:98:11:22:33",
        "controller_chipset": "BCM_4388",
        "controller_discoverable": "attrib_off",
        "controller_state": "attrib_on",
        "controller_transport": "PCIe"
      },
      "device_connected": [
        {
          "JBL Flip 5": {
            "device_address": "AC-BF-71-11-22-33",
            "device_addressType": "Public",
            "device_batteryLevelMain": "90%",
            "device_classOfDevice": "0x04 0x01 0x240414",
            "device_firmwareVersion": "1.4.0",
            "device_isConnected": "attrib_Yes",
            "device_isPaired": "attrib_Yes",
            "device_minorType": "Speaker",
            "device_majorType": "Audio",
            "device_rssi": -47,
            "device_role": "attrib_Master",
            "device_services": "Audio Sink, AVRCP Target"
          }
        }
      ],
      "device_not_connected": [
        {
          "Magic Keyboard": {
            "device_address": "A0-B1-C2-D3-E4-F5",
            "device_minorType": "Keyboard",
            "device_isPaired": "attrib_Yes"
          }
        }
      ]
    }
  ]
}`

func TestParseSystemProfilerJSON(t *testing.T) {
	adapters, devices, err := parseSystemProfilerJSON([]byte(systemProfilerJSONFixture))
	if err != nil {
		t.Fatal(err)
	}
	if len(adapters) != 1 || adapters[0].Address != "F0:18:98:11:22:33" || !adapters[0].Powered {
		t.Fatalf("%+v", adapters)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices", len(devices))
	}
	var speaker, keyboard BluetoothDevice
	for _, d := range devices {
		switch d.Name {
		case "JBL Flip 5":
			speaker = d
		case "Magic Keyboard":
			keyboard = d
		}
	}
	if !speaker.Connected || speaker.Address != "AC:BF:71:11:22:33" {
		t.Fatalf("%+v", speaker)
	}
	if speaker.DeviceType != "Loudspeaker" || speaker.Role != "central (master)" {
		t.Fatalf("type=%q role=%q", speaker.DeviceType, speaker.Role)
	}
	if speaker.RSSI == nil || *speaker.RSSI != -47 || speaker.Battery == nil || *speaker.Battery != 90 {
		t.Fatalf("rssi=%v battery=%v", speaker.RSSI, speaker.Battery)
	}
	if keyboard.Connected || keyboard.DeviceType != "Keyboard" {
		t.Fatalf("%+v", keyboard)
	}
}

const systemProfilerTextFixture = `Bluetooth:

      Apple Bluetooth Software Version: 10.0.0
      Devices (Paired, Configured, etc.):
          JBL Flip 5:
              Address: AC-BF-71-11-22-33
              Major Type: Audio
              Minor Type: Speaker
              Services: Audio Sink, AVRCP Target, Headset
              Paired: Yes
              Configured: Yes
              Connected: Yes
              Manufacturer: Cambridge Silicon Radio (0x1D, 0x2BA)
              Firmware Version: 0x1C4
              Class of Device: 0x04 0x01 0x240414
              AFH: On
              AFH Map: 0xFFFFFFFFFFFFFFFFFF7F
              RSSI: -53
              Role: Master
              Connection Mode: Sniff Mode
              Interval: 11.25 ms
              Link Type: ACL
              Transmit Power Level: 4
              EDR Supported: Yes
              eSCO Supported: Yes
`

func TestParseSystemProfilerText(t *testing.T) {
	devices := parseSystemProfilerText(systemProfilerTextFixture)
	if len(devices) != 1 {
		t.Fatalf("got %d devices: %+v", len(devices), devices)
	}
	d := devices[0]
	if d.Name != "JBL Flip 5" || d.Address != "AC:BF:71:11:22:33" || !d.Connected {
		t.Fatalf("%+v", d)
	}
	if d.LinkMode != "sniff" || d.Role != "central (master)" || d.LinkType != "ACL" {
		t.Fatalf("link mode=%q role=%q type=%q", d.LinkMode, d.Role, d.LinkType)
	}
	if d.IntervalMs == nil || *d.IntervalMs != 11.25 {
		t.Fatalf("interval=%v", d.IntervalMs)
	}
	if d.RSSI == nil || *d.RSSI != -53 || d.TxPower == nil || *d.TxPower != 4 {
		t.Fatalf("rssi=%v tx=%v", d.RSSI, d.TxPower)
	}
	if d.AFHChannels == nil || *d.AFHChannels != 79 {
		t.Fatalf("afh channels=%v", d.AFHChannels)
	}
	if d.DeviceType != "Loudspeaker" {
		t.Fatalf("type=%q", d.DeviceType)
	}
}

func TestParseWindowsPnpBluetooth(t *testing.T) {
	fixture := `[{"Name":"Intel(R) Wireless Bluetooth(R)","InstanceId":"USB\\VID_8087&PID_0026\\5&1","Status":"OK","Battery":null},` +
		`{"Name":"JBL Flip 5","InstanceId":"BTHENUM\\DEV_ACBF71112233\\7&2A","Status":"OK","Battery":90},` +
		`{"Name":"Keychron K2","InstanceId":"BTHLE\\DEV_A0B1C2D3E4F5\\8&1","Status":"Unknown","Battery":null}]`
	adapters, devices := parseWindowsPnpBluetooth([]byte(fixture))
	if len(adapters) != 1 || !adapters[0].Powered {
		t.Fatalf("%+v", adapters)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices: %+v", len(devices), devices)
	}
	if devices[0].Address != "AC:BF:71:11:22:33" || !devices[0].Connected || devices[0].Transport != "BR/EDR" {
		t.Fatalf("%+v", devices[0])
	}
	if devices[0].Battery == nil || *devices[0].Battery != 90 {
		t.Fatalf("battery=%v", devices[0].Battery)
	}
	if devices[1].Connected || devices[1].Transport != "LE" {
		t.Fatalf("%+v", devices[1])
	}
	if _, _, err := func() ([]BluetoothAdapter, []BluetoothDevice, error) {
		a, d := parseWindowsPnpBluetooth([]byte(""))
		return a, d, nil
	}(); err != nil {
		t.Fatal(err)
	}
}

const dumpsysFixture = `Bluetooth Status
  enabled: true
  state: ON
  address: F0:18:98:AA:BB:CC
  name: Pixel 8

Bonded devices:
  AC:BF:71:11:22:33 [ AUDIO_VIDEO_LOUDSPEAKER ] JBL Flip 5
  A0:B1:C2:D3:E4:F5 [ PERIPHERAL_KEYBOARD ] Keychron K2

Profile: A2dpService
  mCurrentDevice: AC:BF:71:11:22:33
  mCodecStatus:
    Current Codec: {codecName:LDAC,mCodecType:4,mCodecPriority:1000000,mSampleRate:0x4(96000),mBitsPerSample:0x2(24),mChannelMode:0x2(STEREO),mCodecSpecific1:1001,mCodecSpecific2:0,mCodecSpecific3:0,mCodecSpecific4:0}
`

func TestParseDumpsysBluetooth(t *testing.T) {
	adapters, devices := parseDumpsysBluetooth(dumpsysFixture)
	if len(adapters) != 1 || !adapters[0].Powered || adapters[0].Address != "F0:18:98:AA:BB:CC" {
		t.Fatalf("%+v", adapters)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices: %+v", len(devices), devices)
	}
	var speaker *BluetoothDevice
	for i := range devices {
		if devices[i].Address == "AC:BF:71:11:22:33" {
			speaker = &devices[i]
		}
	}
	if speaker == nil || !speaker.Connected || speaker.DeviceType != "Loudspeaker" {
		t.Fatalf("%+v", devices)
	}
	if speaker.Audio == nil {
		t.Fatal("no codec decoded from the A2DP dump")
	}
	if speaker.Audio.Codec != "LDAC" || speaker.Audio.SampleRate != 96000 || speaker.Audio.BitDepth != 24 {
		t.Fatalf("%+v", speaker.Audio)
	}
	if speaker.Audio.Bitrate != 660 || speaker.Audio.BitrateKind != btBitrateNegotiated {
		t.Fatalf("EQMID 1001 at 96 kHz must resolve to 660 kbps, got %.0f (%s)", speaker.Audio.Bitrate, speaker.Audio.BitrateKind)
	}
	if !strings.Contains(speaker.Audio.QualityMode, "660") {
		t.Fatalf("quality mode=%q", speaker.Audio.QualityMode)
	}
}

func TestParseAndroidCodecABR(t *testing.T) {
	s := parseAndroidCodecConfig("Current Codec: {codecName:LDAC,mSampleRate:0x1(44100),mBitsPerSample:0x1(16),mChannelMode:0x2(STEREO),mCodecSpecific1:1003}")
	if s == nil || s.QualityMode != "adaptive (ABR)" {
		t.Fatalf("%+v", s)
	}
	if s.BitrateKind != btBitrateNominal {
		t.Fatalf("ABR has no fixed rate, so the reported one must stay nominal, got %q", s.BitrateKind)
	}
	if len(s.Notes) == 0 {
		t.Fatal("ABR mode must be called out in the notes")
	}
	if parseAndroidCodecConfig("no codec here") != nil {
		t.Fatal("unrelated line must not produce a stream")
	}
}
