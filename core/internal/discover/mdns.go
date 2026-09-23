// Package discover announces the agent on the local network as _monitor._tcp
// and parses the same DNS-SD packets the Flutter client sends.
package discover

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const service = "_monitor._tcp.local"

// Port reads the numeric port from ":19999" or "127.0.0.1:19999".
func Port(listen string) int {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return 0
	}
	if !strings.Contains(listen, ":") {
		n, _ := strconv.Atoi(listen)
		return n
	}
	_, p, err := net.SplitHostPort(listen)
	if err != nil {
		if strings.HasPrefix(listen, ":") {
			n, _ := strconv.Atoi(listen[1:])
			return n
		}
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// Announce answers PTR queries for _monitor._tcp.local until ctx is done.
// Binding UDP 5353 fails closed when another responder owns the port.
func Announce(ctx context.Context, hostname string, port int) {
	addr, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
	if err != nil {
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_ = conn.SetReadBuffer(65536)
	host := sanitize(hostname)
	if host == "" {
		host = "monitor"
	}
	buf := make([]byte, 1500)
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, src, err := conn.ReadFromUDP(buf)
		if ctx.Err() != nil {
			return
		}
		if err != nil || n < 12 {
			continue
		}
		if !wantsService(buf[:n]) {
			continue
		}
		pkt := response(host, port, localIPv4())
		_, _ = conn.WriteToUDP(pkt, src)
	}
}

func localIPv4() net.IP {
	conn, err := net.Dial("udp4", "192.0.2.1:80")
	if err != nil {
		return net.IPv4(127, 0, 0, 1)
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok && ua.IP != nil {
		return ua.IP.To4()
	}
	return net.IPv4(127, 0, 0, 1)
}

func sanitize(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '.' || r == '_' {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func wantsService(pkt []byte) bool {
	if len(pkt) < 12 {
		return false
	}
	qd := int(binary.BigEndian.Uint16(pkt[4:6]))
	off := 12
	for i := 0; i < qd && off < len(pkt); i++ {
		name, next := readName(pkt, off)
		if next < 0 {
			return false
		}
		if next+4 > len(pkt) {
			return false
		}
		typ := binary.BigEndian.Uint16(pkt[next : next+2])
		if typ == 12 && (name == service || strings.HasSuffix(name, service)) {
			return true
		}
		off = next + 4
	}
	return false
}

// Query builds a PTR question for _monitor._tcp.local.
func Query() []byte {
	var b []byte
	b = append(b, 0, 1, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0)
	b = append(b, encodeName(service)...)
	b = append(b, 0, 12, 0, 1) // PTR IN
	return b
}

func response(host string, port int, ip net.IP) []byte {
	instance := host + "." + service
	var b []byte
	b = binary.BigEndian.AppendUint16(b, 0)
	b = binary.BigEndian.AppendUint16(b, 0x8400)
	b = append(b, 0, 0, 0, 3, 0, 0, 0, 0) // 3 answers
	b = append(b, encodeName(service)...)
	b = append(b, 0, 12, 0, 1) // PTR
	b = binary.BigEndian.AppendUint32(b, 120)
	ptr := encodeName(instance)
	b = binary.BigEndian.AppendUint16(b, uint16(len(ptr)))
	b = append(b, ptr...)
	b = append(b, encodeName(instance)...)
	b = append(b, 0, 33, 0, 1) // SRV
	b = binary.BigEndian.AppendUint32(b, 120)
	target := encodeName(host + ".local")
	srv := make([]byte, 6+len(target))
	binary.BigEndian.PutUint16(srv[4:6], uint16(port))
	copy(srv[6:], target)
	b = binary.BigEndian.AppendUint16(b, uint16(len(srv)))
	b = append(b, srv...)
	b = append(b, encodeName(host+".local")...)
	b = append(b, 0, 1, 0, 1) // A
	b = binary.BigEndian.AppendUint32(b, 120)
	b = append(b, 0, 4)
	if ip4 := ip.To4(); ip4 != nil {
		b = append(b, ip4...)
	} else {
		b = append(b, 127, 0, 0, 1)
	}
	return b
}

// ParseResponse returns the first http URL advertised in a monitor DNS-SD answer.
func ParseResponse(pkt []byte) (string, bool) {
	if len(pkt) < 12 {
		return "", false
	}
	an := int(binary.BigEndian.Uint16(pkt[6:8]))
	off := 12
	qd := int(binary.BigEndian.Uint16(pkt[4:6]))
	for i := 0; i < qd; i++ {
		_, next := readName(pkt, off)
		if next < 0 || next+4 > len(pkt) {
			return "", false
		}
		off = next + 4
	}
	var port int
	var ip net.IP
	for i := 0; i < an && off+10 < len(pkt); i++ {
		_, next := readName(pkt, off)
		if next < 0 || next+10 > len(pkt) {
			return "", false
		}
		typ := binary.BigEndian.Uint16(pkt[next : next+2])
		rdlen := int(binary.BigEndian.Uint16(pkt[next+8 : next+10]))
		data := next + 10
		if data+rdlen > len(pkt) {
			return "", false
		}
		switch typ {
		case 33: // SRV
			if rdlen >= 6 {
				port = int(binary.BigEndian.Uint16(pkt[data+4 : data+6]))
			}
		case 1: // A
			if rdlen >= 4 {
				ip = net.IP(pkt[data : data+4])
			}
		}
		off = data + rdlen
	}
	if port == 0 || ip == nil {
		return "", false
	}
	return fmt.Sprintf("http://%s:%d", ip.String(), port), true
}

func encodeName(name string) []byte {
	var b []byte
	for _, label := range strings.Split(name, ".") {
		if label == "" {
			continue
		}
		b = append(b, byte(len(label)))
		b = append(b, label...)
	}
	return append(b, 0)
}

func readName(pkt []byte, off int) (string, int) {
	var labels []string
	jumped := false
	next := off
	for steps := 0; steps < 16 && off < len(pkt); steps++ {
		n := int(pkt[off])
		if n == 0 {
			if !jumped {
				next = off + 1
			}
			return strings.Join(labels, "."), next
		}
		if n&0xc0 == 0xc0 {
			if off+1 >= len(pkt) {
				return "", -1
			}
			ptr := int(binary.BigEndian.Uint16(pkt[off:off+2]) & 0x3fff)
			if !jumped {
				next = off + 2
				jumped = true
			}
			off = ptr
			continue
		}
		if off+1+n > len(pkt) {
			return "", -1
		}
		labels = append(labels, string(pkt[off+1:off+1+n]))
		off += 1 + n
		if !jumped {
			next = off
		}
	}
	return "", -1
}
