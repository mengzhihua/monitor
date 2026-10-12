package collect

import (
	"math"
	"testing"
)

const bluetoothctlInfoFixture = `Device AC:BF:71:11:22:33 (public)
	Name: JBL Flip 5
	Alias: JBL Flip 5
	Class: 0x00240414
	Icon: audio-card
	Paired: yes
	Bonded: yes
	Trusted: yes
	Blocked: no
	Connected: yes
	LegacyPairing: no
	UUID: Serial Port               (00001101-0000-1000-8000-00805f9b34fb)
	UUID: Audio Sink                (0000110b-0000-1000-8000-00805f9b34fb)
	UUID: A/V Remote Control Target (0000110c-0000-1000-8000-00805f9b34fb)
	UUID: Advanced Audio Distribu.. (0000110d-0000-1000-8000-00805f9b34fb)
	UUID: Handsfree                 (0000111e-0000-1000-8000-00805f9b34fb)
	UUID: Vendor specific           (f8d1fbe4-7966-4334-8024-ff96c9330e15)
	Modalias: bluetooth:v008Ap0001d0100
	RSSI: -47
	TxPower: 4
	Battery Percentage: 0x5a (90)
`

func TestParseBluetoothctlInfo(t *testing.T) {
	d := parseBluetoothctlInfo(bluetoothctlInfoFixture)
	if d.Address != "AC:BF:71:11:22:33" || d.Name != "JBL Flip 5" {
		t.Fatalf("address=%q name=%q", d.Address, d.Name)
	}
	if !d.Connected || !d.Paired {
		t.Fatalf("connected=%v paired=%v", d.Connected, d.Paired)
	}
	if d.DeviceType != "Loudspeaker" || d.MajorClass != "Audio/Video" {
		t.Fatalf("type=%q major=%q", d.DeviceType, d.MajorClass)
	}
	if d.Transport != "BR/EDR" {
		t.Fatalf("transport=%q", d.Transport)
	}
	if d.RSSI == nil || *d.RSSI != -47 || d.RSSIKind != "absolute" {
		t.Fatalf("rssi=%v kind=%q", d.RSSI, d.RSSIKind)
	}
	if d.TxPower == nil || *d.TxPower != 4 {
		t.Fatalf("tx power=%v", d.TxPower)
	}
	if d.Battery == nil || *d.Battery != 90 {
		t.Fatalf("battery=%v", d.Battery)
	}
	want := map[string]bool{"Audio Sink (A2DP)": true, "Advanced Audio Distribution (A2DP)": true, "Hands-Free (HFP)": true}
	for _, p := range d.Profiles {
		delete(want, p)
	}
	if len(want) != 0 {
		t.Fatalf("missing profiles %v in %v", want, d.Profiles)
	}
}

func TestParseBluetoothctlDevices(t *testing.T) {
	devices := parseBluetoothctlDevices("Device AC:BF:71:11:22:33 JBL Flip 5\nDevice 00:1A:7D:DA:71:13 Keychron K2\nnot a device line\n")
	if len(devices) != 2 || devices[0].Name != "JBL Flip 5" || devices[1].Address != "00:1A:7D:DA:71:13" {
		t.Fatalf("%+v", devices)
	}
}

const hciconfigFixture = `hci0:	Type: Primary  Bus: USB
	BD Address: 00:1A:7D:DA:71:13  ACL MTU: 1021:8  SCO MTU: 64:1
	UP RUNNING PSCAN ISCAN
	RX bytes:1509372 acl:2144 sco:0 events:1203 errors:0
	TX bytes:482019 acl:1980 sco:0 commands:812 errors:3
	Features: 0xbf 0xfe 0xcf 0xfe 0xdb 0xff 0x7b 0x87
	Packet type: DM1 DM3 DM5 DH1 DH3 DH5 HV1 HV2 HV3
	Link policy: RSWITCH SNIFF
	Link mode: SLAVE ACCEPT
	Name: 'monitor-host'
	Class: 0x6c010c
	HCI Version: 5.1 (0xa)  Revision: 0x100
	LMP Version: 5.1 (0xa)  Subversion: 0x100
	Manufacturer: Intel Corp. (2)
`

func TestParseHciconfig(t *testing.T) {
	adapters := parseHciconfig(hciconfigFixture)
	if len(adapters) != 1 {
		t.Fatalf("got %d adapters", len(adapters))
	}
	a := adapters[0]
	if a.ID != "hci0" || a.Address != "00:1A:7D:DA:71:13" || a.Bus != "USB" {
		t.Fatalf("%+v", a)
	}
	if !a.Powered || !a.Discoverable {
		t.Fatalf("powered=%v discoverable=%v", a.Powered, a.Discoverable)
	}
	if a.RxBytes != 1509372 || a.TxBytes != 482019 || a.RxACL != 2144 || a.TxACL != 1980 {
		t.Fatalf("counters %+v", a)
	}
	if a.Events != 1203 || a.Commands != 812 || a.RxErrors != 0 || a.TxErrors != 3 {
		t.Fatalf("events/errors %+v", a)
	}
	if !a.HasStatistics || a.Name != "monitor-host" || a.Manufacturer != "Intel Corp. (2)" {
		t.Fatalf("%+v", a)
	}
	if a.HCIVersion != "5.1 (0xa)  Revision: 0x100" {
		t.Fatalf("hci version %q", a.HCIVersion)
	}
}

func TestParseHcitoolConnections(t *testing.T) {
	conns := parseHcitoolConnections("Connections:\n\t< ACL AC:BF:71:11:22:33 handle 12 state 1 lm MASTER AUTH ENCRYPT \n\t> LE 00:11:22:33:44:55 handle 64 state 1 lm SLAVE \n")
	if len(conns) != 2 {
		t.Fatalf("got %d connections", len(conns))
	}
	if conns[0].LinkType != "ACL" || conns[0].Handle != 12 || conns[0].Role != "central (master)" {
		t.Fatalf("%+v", conns[0])
	}
	if conns[0].Encrypted == nil || !*conns[0].Encrypted || conns[0].LinkMode != "active" {
		t.Fatalf("%+v", conns[0])
	}
	if conns[1].LinkType != "LE" || conns[1].Role != "peripheral (slave)" || conns[1].Encrypted != nil {
		t.Fatalf("%+v", conns[1])
	}
	if _, ok := btLastNumber("Link quality: 245"); !ok {
		t.Fatal("link quality not read")
	}
	if v, _ := btLastNumber("RSSI return value: -3"); v != -3 {
		t.Fatalf("rssi = %v", v)
	}
	if m, ok := btParseAFHOutput("AFH mode: enabled\nAFH map: 0xFFFFFFFFFFFFFFFFFF7F\n"); !ok || m != "0xFFFFFFFFFFFFFFFFFF7F" {
		t.Fatalf("afh map %q ok=%v", m, ok)
	}
}

const pactlFixture = `Card #43
	Name: bluez_card.AC_BF_71_11_22_33
	Driver: module-bluez5-device.c
	Owner Module: 26
	Properties:
		device.description = "JBL Flip 5"
		device.string = "AC:BF:71:11:22:33"
		device.api = "bluez"
		bluez.path = "/org/bluez/hci0/dev_AC_BF_71_11_22_33"
		bluez.class = "0x240414"
		bluetooth.battery = "90%"
	Profiles:
		a2dp-sink-sbc: High Fidelity Playback (A2DP Sink, codec SBC) (sinks: 1, sources: 0, priority: 40, available: yes)
		a2dp-sink-ldac: High Fidelity Playback (A2DP Sink, codec LDAC) (sinks: 1, sources: 0, priority: 48, available: yes)
		headset-head-unit-msbc: Headset Head Unit (HSP/HFP, codec mSBC) (sinks: 1, sources: 1, priority: 30, available: yes)
		off: Off (sinks: 0, sources: 0, priority: 0, available: yes)
	Active Profile: a2dp-sink-ldac

Sink #52
	State: RUNNING
	Name: bluez_output.AC_BF_71_11_22_33.1
	Description: JBL Flip 5
	Driver: PipeWire
	Sample Specification: s24le 2ch 96000Hz
	Channel Map: front-left,front-right
	Properties:
		bluetooth.codec = "ldac"
		api.bluez5.codec = "ldac"
		api.bluez5.profile = "a2dp-sink"
		api.bluez5.address = "AC:BF:71:11:22:33"
		api.bluez5.transport = "/org/bluez/hci0/dev_AC_BF_71_11_22_33/fd3"
		device.string = "AC:BF:71:11:22:33"
`

func TestParsePactlBluetooth(t *testing.T) {
	cards := parsePactlBluetooth(pactlFixture)
	card, ok := cards["AC:BF:71:11:22:33"]
	if !ok {
		t.Fatalf("no card for the speaker, got %v", cards)
	}
	audio := card.Audio
	if card.Name != "JBL Flip 5" || card.Class != "0x240414" {
		t.Fatalf("name=%q class=%q", card.Name, card.Class)
	}
	if audio.Codec != "LDAC" {
		t.Fatalf("codec=%q", audio.Codec)
	}
	if audio.SampleRate != 96000 || audio.BitDepth != 24 || audio.ChannelMode != "stereo" {
		t.Fatalf("rate=%v depth=%d channels=%q", audio.SampleRate, audio.BitDepth, audio.ChannelMode)
	}
	if audio.Profile != "A2DP" {
		t.Fatalf("profile=%q", audio.Profile)
	}
	if audio.BitrateKind != btBitrateNominal || audio.Bitrate <= 0 {
		t.Fatalf("PulseAudio only names the codec, so the rate must be nominal: %.0f (%s)", audio.Bitrate, audio.BitrateKind)
	}
}

func TestParsePactlActiveProfileCodecFallback(t *testing.T) {
	block := "Card #1\n\tName: bluez_card.AA_BB_CC_DD_EE_FF\n\tProperties:\n\t\tdevice.string = \"AA:BB:CC:DD:EE:FF\"\n\tActive Profile: headset-head-unit-msbc\n"
	cards := parsePactlBluetooth(block)
	card, ok := cards["AA:BB:CC:DD:EE:FF"]
	if !ok || card.Audio.Codec != "mSBC" {
		t.Fatalf("%+v", cards)
	}
	audio := card.Audio
	if audio.Profile != "HFP/HSP" {
		t.Fatalf("profile=%q", audio.Profile)
	}
}

// dbusManagedObjectsFixture is the shape dbus-send prints for
// org.freedesktop.DBus.ObjectManager.GetManagedObjects against org.bluez.
const dbusManagedObjectsFixture = `method return time=1760000000.123456 sender=:1.7 -> destination=:1.99 serial=3 reply_serial=2
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
                  dict entry(
                     string "Discoverable"
                     variant                         boolean false
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
                     string "Icon"
                     variant                         string "audio-card"
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
                           string "0000110c-0000-1000-8000-00805f9b34fb"
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
                     variant                         string "0000110a-0000-1000-8000-00805f9b34fb"
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
                  dict entry(
                     string "Volume"
                     variant                         uint16 100
                  )
               ]
            )
         ]
      )
   ]
`

func TestParseDBusManagedObjects(t *testing.T) {
	objs := parseDBusManagedObjects(dbusManagedObjectsFixture)
	if len(objs) != 3 {
		t.Fatalf("got %d objects", len(objs))
	}
	device := objs[1].Interfaces["org.bluez.Device1"]
	if device["Address"].Str != "AC:BF:71:11:22:33" {
		t.Fatalf("address=%q", device["Address"].Str)
	}
	// A name containing brackets must not shift the depth tracking.
	if device["Alias"].Str != "JBL Flip 5 (kitchen)" {
		t.Fatalf("alias=%q", device["Alias"].Str)
	}
	if !device["Connected"].Bool || device["RSSI"].Num != -47 || device["Class"].Num != 2360340 {
		t.Fatalf("%+v", device)
	}
	if len(device["UUIDs"].Array) != 3 {
		t.Fatalf("uuids=%v", device["UUIDs"].Array)
	}
	if objs[1].Interfaces["org.bluez.Battery1"]["Percentage"].Num != 90 {
		t.Fatal("battery not decoded")
	}
	transport := objs[2].Interfaces["org.bluez.MediaTransport1"]
	if got := transport["Configuration"].Bytes; len(got) != 4 || got[0] != 33 || got[3] != 53 {
		t.Fatalf("configuration bytes = %v", got)
	}
	if transport["State"].Str != "active" || transport["Delay"].Num != 1500 {
		t.Fatalf("%+v", transport)
	}
}

func TestBluetoothFromDBus(t *testing.T) {
	adapters, devices := btFromDBus(parseDBusManagedObjects(dbusManagedObjectsFixture))
	if len(adapters) != 1 || adapters[0].ID != "hci0" || !adapters[0].Powered {
		t.Fatalf("%+v", adapters)
	}
	if len(devices) != 1 {
		t.Fatalf("got %d devices", len(devices))
	}
	d := devices[0]
	if !d.Connected || d.Name != "JBL Flip 5 (kitchen)" || d.Adapter != "hci0" {
		t.Fatalf("%+v", d)
	}
	if d.DeviceType != "Loudspeaker" || d.Transport != "BR/EDR" {
		t.Fatalf("type=%q transport=%q", d.DeviceType, d.Transport)
	}
	if d.Battery == nil || *d.Battery != 90 || d.RSSI == nil || *d.RSSI != -47 {
		t.Fatalf("battery=%v rssi=%v", d.Battery, d.RSSI)
	}
	if d.Audio == nil {
		t.Fatal("no audio stream decoded from MediaTransport1")
	}
	if d.Audio.Codec != "SBC" || math.Abs(d.Audio.Bitrate-328) > 1 || d.Audio.BitrateKind != btBitrateNegotiated {
		t.Fatalf("%+v", d.Audio)
	}
	if d.Audio.State != "active" || d.Audio.DelayMs != 150 {
		t.Fatalf("state=%q delay=%v (BlueZ reports 1/10 ms)", d.Audio.State, d.Audio.DelayMs)
	}
}

func TestBluetoothNormalizeAddress(t *testing.T) {
	for _, in := range []string{"ac:bf:71:11:22:33", "AC-BF-71-11-22-33", "AC_BF_71_11_22_33",
		"/org/bluez/hci0/dev_AC_BF_71_11_22_33", "/org/bluez/hci0/dev_AC_BF_71_11_22_33/fd3"} {
		if got := btNormalizeAddress(in); got != "AC:BF:71:11:22:33" {
			t.Fatalf("btNormalizeAddress(%q) = %q", in, got)
		}
	}
	if btLooksLikeAddress("JBL Flip 5") || btLooksLikeAddress("AC:BF:71:11:22") {
		t.Fatal("non-addresses must be rejected")
	}
}
