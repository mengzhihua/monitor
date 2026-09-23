package collect

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/mengzhihua/monitor/core/internal/registry"
)

func TestMongoBSONRoundTrip(t *testing.T) {
	doc := bsonDoc(
		bsonDouble("ok", 1),
		bsonInt32("uptime", 42),
		bsonEmbed(0x03, "opcounters", bsonDoc(bsonInt32("insert", 3), bsonInt32("query", 7))),
		bsonCString("errmsg", ""),
	)
	m := bsonDecode(doc)
	if mathish(m["ok"]) != 1 || mathish(m["uptime"]) != 42 {
		t.Fatalf("%v", m)
	}
	ops, _ := m["opcounters"].(map[string]any)
	if mathish(ops["insert"]) != 3 {
		t.Fatalf("%v", m)
	}
	nums := bsonNumbers(m, "")
	if nums["opcounters.insert"] != 3 || nums["uptime"] != 42 {
		t.Fatalf("%v", nums)
	}
}

func mathish(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int32:
		return float64(t)
	case int64:
		return float64(t)
	}
	return 0
}

func TestMongoCollector(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		if _, err := mongoReadOPMSG(c); err != nil {
			return
		}
		body := bsonDoc(
			bsonDouble("ok", 1),
			bsonInt32("uptime", 9),
			bsonEmbed(0x03, "opcounters", bsonDoc(
				bsonInt32("insert", 1), bsonInt32("query", 2), bsonInt32("update", 3),
				bsonInt32("delete", 4), bsonInt32("getmore", 5), bsonInt32("command", 6))),
			bsonEmbed(0x03, "connections", bsonDoc(bsonInt32("current", 8), bsonInt32("available", 100))),
			bsonEmbed(0x03, "mem", bsonDoc(bsonInt32("resident", 50), bsonInt32("virtual", 200))),
			bsonEmbed(0x03, "network", bsonDoc(bsonInt32("bytesIn", 10), bsonInt32("bytesOut", 20))),
			bsonEmbed(0x03, "metrics", bsonDoc(bsonEmbed(0x03, "document", bsonDoc(
				bsonInt32("inserted", 1), bsonInt32("deleted", 2), bsonInt32("returned", 3), bsonInt32("updated", 4))))),
		)
		_ = mongoWriteOPMSG(c, 2, body)
	}()
	m := &mongodbCollector{cfg: mongodbConfig{Address: ln.Addr().String(), Timeout: time.Second, Database: "admin"}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := m.Init(reg); err != nil {
		t.Fatal(err)
	}
}

func TestParseChrony(t *testing.T) {
	tr := `Reference ID    : 1.2.3.4 (ntp)
Stratum         : 3
System time     : 0.001 seconds slow of NTP time
Last offset     : +0.0002 seconds
RMS offset      : 0.0003 seconds
Frequency       : 5.5 ppm slow
Residual freq   : -0.1 ppm
Skew            : 0.2 ppm
Root delay      : 0.01 seconds
Root dispersion : 0.002 seconds
Update interval : 64.0 seconds
Leap status     : Normal
`
	s := parseChronyTracking(tr)
	if s.stratum != 3 || s.correction >= 0 || s.lastOff != 0.0002 || s.freq >= 0 || s.leap != "normal" {
		t.Fatalf("%+v", s)
	}
	parseChronyActivity("8 sources online\n2 sources offline\n0 sources doing burst (return to online)\n1 sources with unknown address\n", &s)
	if s.online != 8 || s.offline != 2 || s.unresolved != 1 {
		t.Fatalf("%+v", s)
	}
	c := &chronyCollector{
		cfg: chronyConfig{Command: "chronyc", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[len(args)-1] == "activity" {
				return []byte("8 sources online\n"), nil
			}
			return []byte(tr), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := c.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := c.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestParseNTPReply(t *testing.T) {
	t1 := time.Unix(1_700_000_000, 0)
	t4 := t1.Add(20 * time.Millisecond)
	t2 := t1.Add(5 * time.Millisecond)
	t3 := t1.Add(15 * time.Millisecond)
	buf := make([]byte, 48)
	buf[0] = 0x24
	buf[1] = 3
	buf[3] = 0xFA                            // precision -6
	binary.BigEndian.PutUint32(buf[4:], 655) // ~0.01s NTP short
	binary.BigEndian.PutUint32(buf[8:], 131) // ~0.002s NTP short
	binary.BigEndian.PutUint64(buf[32:], ntpTimestamp(t2))
	binary.BigEndian.PutUint64(buf[40:], ntpTimestamp(t3))
	s, err := parseNTPReply(buf, t1, t4)
	if err != nil || s.stratum != 3 {
		t.Fatalf("%+v %v", s, err)
	}
	if s.offsetMs < -1 || s.offsetMs > 1 {
		t.Fatalf("offset %v", s.offsetMs)
	}
}

func TestNTPCollectorLoopback(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 64)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil || n < 48 {
			return
		}
		now := time.Now()
		resp := make([]byte, 48)
		copy(resp, buf[:48])
		resp[1] = 2
		resp[3] = 0xFA
		binary.BigEndian.PutUint64(resp[32:], ntpTimestamp(now))
		binary.BigEndian.PutUint64(resp[40:], ntpTimestamp(now))
		_, _ = pc.WriteTo(resp, addr)
	}()
	n := &ntpdCollector{cfg: ntpdConfig{Address: pc.LocalAddr().String(), Timeout: time.Second}}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
}

func TestSmartctlParse(t *testing.T) {
	devs := parseSmartScan("/dev/sda -d sat # /dev/sda\n/dev/nvme0 -d nvme\n")
	if len(devs) != 2 || devs[0] != "/dev/sda" {
		t.Fatalf("%v", devs)
	}
	js, _ := json.Marshal(map[string]any{
		"device":            map[string]any{"name": "/dev/sda"},
		"smart_status":      map[string]any{"passed": true},
		"temperature":       map[string]any{"current": 31},
		"power_on_time":     map[string]any{"hours": 2},
		"power_cycle_count": 9,
	})
	d, err := parseSmartJSON(js)
	if err != nil || d.Name != "sda" || !d.Passed || d.Temp != 31 || d.PowerOn != 7200 || d.Cycles != 9 {
		t.Fatalf("%+v %v", d, err)
	}
	calls := 0
	s := &smartctlCollector{
		cfg: smartctlConfig{Command: "smartctl", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			calls++
			if len(args) > 0 && args[0] == "--scan" {
				return []byte("/dev/sda -d sat\n"), nil
			}
			return js, nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := s.Init(reg); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	if err := s.Collect(context.Background(), reg, now); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("smartctl.device_smart_status.sda"); !ok {
		t.Fatal("missing chart")
	}
	after := calls
	if err := s.Collect(context.Background(), reg, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if calls != after {
		t.Fatalf("smartctl ran again after 1s: calls %d -> %d", after, calls)
	}
	if err := s.Collect(context.Background(), reg, now.Add(smartctlEvery)); err != nil {
		t.Fatal(err)
	}
	if calls == after {
		t.Fatal("smartctl did not run again after the sample interval")
	}
}

func TestNVMeParse(t *testing.T) {
	devs := parseNVMeList("Node          SN\n/dev/nvme0n1  ABC  Samsung\n")
	if len(devs) != 1 || devs[0] != "/dev/nvme0n1" {
		t.Fatalf("%v", devs)
	}
	st, err := parseNVMeSmart("temperature                     : 36 C\navailable_spare                 : 100%\npercentage_used                 : 2%\ndata_units_read                 : 10\ndata_units_written              : 20\nmedia_errors                    : 0\n")
	if err != nil || st.temp != 36 || st.spare != 100 || st.used != 2 || st.read != 10 {
		t.Fatalf("%+v %v", st, err)
	}
	n := &nvmeCollector{
		cfg: nvmeConfig{Command: "nvme", Timeout: time.Second},
		run: func(_ context.Context, _ string, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "list" {
				return []byte("/dev/nvme0n1  x\n"), nil
			}
			return []byte("temperature : 36 C\navailable_spare : 100%\npercentage_used : 2%\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := n.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := n.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAPCStatus(t *testing.T) {
	raw := "STATUS   : ONLINE\nBCHARGE  : 100.0 Percent\nBATTV    : 27.3 Volts\nLOADPCT  : 12.0 Percent\nLINEV    : 230.0 Volts\nTIMELEFT : 45.0 Minutes\nITEMP    : 30.0 C\n"
	st := parseAPCStatus(raw)
	if st["STATUS"] != "ONLINE" || apcNum(st["BCHARGE"]) != 100 || apcMinutes(st["TIMELEFT"]) != 45*60 {
		t.Fatalf("%v", st)
	}
	serve := func(server net.Conn) {
		defer server.Close()
		var hdr [2]byte
		if _, err := io.ReadFull(server, hdr[:]); err != nil {
			return
		}
		n := int(binary.BigEndian.Uint16(hdr[:]))
		buf := make([]byte, n)
		_, _ = io.ReadFull(server, buf)
		for _, line := range strings.Split(raw, "\n") {
			if line == "" {
				continue
			}
			_ = apcWrite(server, line)
		}
		var z [2]byte
		_, _ = server.Write(z[:])
	}
	a := &apcupsdCollector{
		cfg: apcupsdConfig{Timeout: 2 * time.Second},
		dial: func(context.Context) (net.Conn, error) {
			c, s := net.Pipe()
			go serve(s)
			return c, nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := a.Init(reg); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("apcupsd.ups_status"); !ok {
		t.Fatal("missing apcupsd chart")
	}
}

func TestParseLVS(t *testing.T) {
	rows := parseLVS("  root vg0 1073741824 12.50  \n  data vg0 2147483648 80.00 3.00\n")
	if len(rows) != 2 || rows[0].Name != "root" || rows[1].Data != 80 || rows[1].Meta != 3 {
		t.Fatalf("%+v", rows)
	}
	l := &lvmCollector{
		cfg: lvmConfig{Command: "lvs", Timeout: time.Second},
		run: func(context.Context, string, ...string) ([]byte, error) {
			return []byte("  data vg0 2147483648 80.00 3.00\n"), nil
		},
	}
	reg := registry.New(&registry.Host{Hostname: "h", UpdateEvery: 1}, nil)
	if err := l.Init(reg); err != nil {
		t.Fatal(err)
	}
	if err := l.Collect(context.Background(), reg, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Chart("lvm.lv_data_percent.vg0_data"); !ok {
		t.Fatal("missing lvm chart")
	}
}

func TestParsePgBouncerMaps(t *testing.T) {
	// header-aware parser is covered via pgParseColNames
	payload := make([]byte, 2)
	binary.BigEndian.PutUint16(payload, 2)
	payload = append(payload, []byte("database\x00")...)
	payload = append(payload, make([]byte, 4+2+4+2+4+2)...)
	payload = append(payload, []byte("cl_active\x00")...)
	payload = append(payload, make([]byte, 4+2+4+2+4+2)...)
	cols := pgParseColNames(payload)
	if len(cols) != 2 || cols[0] != "database" || cols[1] != "cl_active" {
		t.Fatalf("%v", cols)
	}
}
