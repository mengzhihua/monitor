package collect

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"time"
)

// readIPP asks a CUPS scheduler for printers and pending jobs. Chart IDs stay
// the same as the lpstat path.
func readIPP(addr string, timeout time.Duration) ([]cupsDest, int, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	printers, err := ippCall(addr, timeout, 0x4002) // CUPS-Get-Printers
	if err != nil {
		return nil, 0, err
	}
	dests := parseIPPPrinters(printers)
	if len(dests) == 0 {
		return nil, 0, fmt.Errorf("cups: ipp returned no printers")
	}
	jobs := 0
	if body, err := ippCall(addr, timeout, 0x000a); err == nil { // Get-Jobs
		jobs = parseIPPJobCount(body)
	}
	return dests, jobs, nil
}

func ippCall(addr string, timeout time.Duration, op uint16) ([]byte, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))
	body := ippRequest(op)
	hdr := fmt.Sprintf("POST / HTTP/1.1\r\nHost: %s\r\nContent-Type: application/ipp\r\nContent-Length: %d\r\nConnection: close\r\n\r\n", addr, len(body))
	if _, err := conn.Write(append([]byte(hdr), body...)); err != nil {
		return nil, err
	}
	raw, err := io.ReadAll(io.LimitReader(conn, 1<<20))
	if err != nil {
		return nil, err
	}
	if i := bytes.Index(raw, []byte("\r\n\r\n")); i >= 0 {
		raw = raw[i+4:]
	}
	if len(raw) < 8 {
		return nil, fmt.Errorf("cups: short ipp response")
	}
	if status := binary.BigEndian.Uint16(raw[2:4]); status >= 0x0400 {
		return nil, fmt.Errorf("cups: ipp status %d", status)
	}
	return raw, nil
}

func ippRequest(op uint16) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x02, 0x00})
	_ = binary.Write(&b, binary.BigEndian, op)
	_ = binary.Write(&b, binary.BigEndian, uint32(1))
	b.WriteByte(0x01)
	writeIPPString(&b, 0x47, "attributes-charset", "utf-8")
	writeIPPString(&b, 0x48, "attributes-natural-language", "en")
	b.WriteByte(0x03)
	return b.Bytes()
}

func writeIPPString(b *bytes.Buffer, tag byte, name, value string) {
	b.WriteByte(tag)
	_ = binary.Write(b, binary.BigEndian, uint16(len(name)))
	b.WriteString(name)
	_ = binary.Write(b, binary.BigEndian, uint16(len(value)))
	b.WriteString(value)
}

func writeIPPEnum(b *bytes.Buffer, name string, v uint32) {
	b.WriteByte(0x23)
	_ = binary.Write(b, binary.BigEndian, uint16(len(name)))
	b.WriteString(name)
	_ = binary.Write(b, binary.BigEndian, uint16(4))
	_ = binary.Write(b, binary.BigEndian, v)
}

func parseIPPPrinters(b []byte) []cupsDest {
	var out []cupsDest
	walkIPP(b, func(g, tag byte, name string, val []byte) {
		if tag < 0x10 {
			if tag == 0x04 {
				out = append(out, cupsDest{State: "idle"})
			}
			return
		}
		if g != 0x04 || len(out) == 0 || name == "" {
			return
		}
		cur := &out[len(out)-1]
		switch name {
		case "printer-name":
			cur.Name = trimCString(val)
		case "printer-state":
			if tag == 0x23 && len(val) >= 4 {
				switch binary.BigEndian.Uint32(val[:4]) {
				case 4:
					cur.State = "printing"
				case 5:
					cur.State = "stopped"
				default:
					cur.State = "idle"
				}
			}
		}
	})
	kept := out[:0]
	for _, d := range out {
		if d.Name != "" {
			kept = append(kept, d)
		}
	}
	return kept
}

func parseIPPJobCount(b []byte) int {
	n := 0
	walkIPP(b, func(_, tag byte, _ string, _ []byte) {
		if tag == 0x02 {
			n++
		}
	})
	return n
}

func walkIPP(b []byte, fn func(group, tag byte, name string, val []byte)) {
	if len(b) < 8 {
		return
	}
	i := 8
	var group byte
	name := ""
	for i < len(b) {
		tag := b[i]
		i++
		if tag == 0x03 {
			return
		}
		if tag < 0x10 {
			group = tag
			name = ""
			fn(group, tag, "", nil)
			continue
		}
		if i+2 > len(b) {
			return
		}
		nl := int(binary.BigEndian.Uint16(b[i : i+2]))
		i += 2
		if nl < 0 || i+nl > len(b) {
			return
		}
		if nl > 0 {
			name = string(b[i : i+nl])
		}
		i += nl
		if i+2 > len(b) {
			return
		}
		vl := int(binary.BigEndian.Uint16(b[i : i+2]))
		i += 2
		if vl < 0 || i+vl > len(b) {
			return
		}
		fn(group, tag, name, b[i:i+vl])
		i += vl
	}
}

func trimCString(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return string(b)
}
