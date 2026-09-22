package collect

import (
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// procNetInodes maps "local|remote" (same formatting as network-connections)
// to the socket inode from /proc/net/{tcp,tcp6,udp,udp6}. Missing files yield
// an empty map; a bad line is skipped.
func procNetInodes(read func(name string) ([]byte, error)) map[string]string {
	if read == nil {
		read = func(name string) ([]byte, error) {
			return os.ReadFile("/proc/net/" + name)
		}
	}
	out := map[string]string{}
	for _, name := range []string{"tcp", "tcp6", "udp", "udp6"} {
		b, err := read(name)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			local, remote, inode, ok := parseProcNetLine(line)
			if !ok || inode == "" || inode == "0" {
				continue
			}
			key := local + "|" + remote
			if _, exists := out[key]; !exists {
				out[key] = inode
			}
		}
	}
	return out
}

func parseProcNetLine(line string) (local, remote, inode string, ok bool) {
	f := strings.Fields(line)
	if len(f) < 10 || !strings.HasSuffix(f[0], ":") {
		return "", "", "", false
	}
	lip, lp, ok1 := parseProcHexAddr(f[1])
	rip, rp, ok2 := parseProcHexAddr(f[2])
	if !ok1 || !ok2 {
		return "", "", "", false
	}
	return fmtAddrParts(lip, lp), fmtAddrParts(rip, rp), f[9], true
}

func parseProcHexAddr(s string) (string, uint32, bool) {
	ipHex, portHex, ok := strings.Cut(s, ":")
	if !ok || (len(ipHex) != 8 && len(ipHex) != 32) || len(portHex) != 4 {
		return "", 0, false
	}
	raw, err := hex.DecodeString(ipHex)
	if err != nil {
		return "", 0, false
	}
	// /proc/net stores IPv4 little-endian and IPv6 as four little-endian words.
	if len(raw) == 4 {
		raw[0], raw[1], raw[2], raw[3] = raw[3], raw[2], raw[1], raw[0]
	} else {
		for i := 0; i < 16; i += 4 {
			raw[i], raw[i+3] = raw[i+3], raw[i]
			raw[i+1], raw[i+2] = raw[i+2], raw[i+1]
		}
	}
	port, err := strconv.ParseUint(portHex, 16, 32)
	if err != nil {
		return "", 0, false
	}
	return net.IP(raw).String(), uint32(port), true
}

func fmtAddrParts(ip string, port uint32) string {
	if ip == "" && port == 0 {
		return ""
	}
	if strings.Contains(ip, ":") {
		return fmt.Sprintf("[%s]:%d", ip, port)
	}
	return fmt.Sprintf("%s:%d", ip, port)
}
