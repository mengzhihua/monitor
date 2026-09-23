package collect

import (
	"encoding/binary"
	"testing"
)

func TestParseAuditNetlink(t *testing.T) {
	payload := make([]byte, 36)
	binary.LittleEndian.PutUint32(payload[4:8], 1)    // enabled
	binary.LittleEndian.PutUint32(payload[8:12], 2)   // failure
	binary.LittleEndian.PutUint32(payload[20:24], 64) // backlog limit
	binary.LittleEndian.PutUint32(payload[24:28], 3)  // lost
	binary.LittleEndian.PutUint32(payload[28:32], 5)  // backlog
	msg := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(msg[0:4], uint32(len(msg)))
	binary.LittleEndian.PutUint16(msg[4:6], 1000)
	copy(msg[16:], payload)
	st, ok := parseAuditNetlink(msg)
	if !ok || st.Enabled != 1 || st.Failure != 2 || st.BacklogLimit != 64 || st.Lost != 3 || st.Backlog != 5 {
		t.Fatalf("%+v %v", st, ok)
	}
}

func TestParseNfacctNetlink(t *testing.T) {
	name := []byte("acct\x00")
	attrName := make([]byte, 4+len(name))
	binary.BigEndian.PutUint16(attrName[0:2], uint16(4+len(name)))
	binary.BigEndian.PutUint16(attrName[2:4], 1)
	copy(attrName[4:], name)
	attrName = append(attrName, 0, 0, 0) // 4-byte netlink padding; length stays 9
	pkts := make([]byte, 4+8)
	binary.BigEndian.PutUint16(pkts[0:2], 12)
	binary.BigEndian.PutUint16(pkts[2:4], 2)
	binary.BigEndian.PutUint64(pkts[4:], 9)
	body := append([]byte{0, 0, 1, 0}, attrName...)
	body = append(body, pkts...)
	msg := make([]byte, 16+len(body))
	binary.LittleEndian.PutUint32(msg[0:4], uint32(16+len(body)))
	copy(msg[16:], body)
	got := parseNfacctNetlink(msg)
	if len(got) != 1 || got[0].Name != "acct" || got[0].Packets != 9 {
		t.Fatalf("%+v", got)
	}
}

func TestDecodeTCPStat(t *testing.T) {
	buf := make([]byte, 8*len(freebsdTCPFields))
	binary.LittleEndian.PutUint64(buf[26*8:], 42) // tcps_rcvtotal
	m := decodeTCPStat(buf)
	if m["net.inet.tcp.stats.tcps_rcvtotal"] != "42" {
		t.Fatalf("%v", m["net.inet.tcp.stats.tcps_rcvtotal"])
	}
}

func TestParseIPMISensors(t *testing.T) {
	s := parseIPMISDR("1 | CPU Temp | Temperature | Nominal | 33.5 | C | 'OK'\n")
	if len(s) != 1 || s[0].Name != "CPU Temp" || s[0].Kind != "temperatures" || s[0].State != "ok" || s[0].Value != 33.5 {
		t.Fatalf("%+v", s)
	}
}
