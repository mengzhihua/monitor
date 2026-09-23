//go:build linux

package collect

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

func readXenDomains() ([]xlDomain, bool) {
	var conn net.Conn
	var err error
	for _, path := range []string{"/var/run/xenstored/socket_ro", "/run/xenstored/socket_ro", "/var/run/xenstored/socket"} {
		conn, err = net.DialTimeout("unix", path, time.Second)
		if err == nil {
			break
		}
	}
	if conn == nil {
		return nil, false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	dir, err := xsRound(conn, 1, "/local/domain") // XS_DIRECTORY
	if err != nil {
		return nil, false
	}
	ids := xsSplit(dir)
	reads := map[string]string{}
	for _, id := range ids {
		base := "/local/domain/" + id
		if name, err := xsRound(conn, 2, base+"/name"); err == nil { // XS_READ
			reads[base+"/name"] = name
		}
		if mem, err := xsRound(conn, 2, base+"/memory/target"); err == nil {
			reads[base+"/memory/target"] = mem
		}
	}
	doms := xenDomainsFrom(ids, reads)
	if len(doms) == 0 {
		return nil, false
	}
	return doms, true
}

func xsRound(conn net.Conn, op uint32, path string) (string, error) {
	if _, err := conn.Write(xsPack(op, path)); err != nil {
		return "", err
	}
	hdr := make([]byte, 16)
	if _, err := io.ReadFull(conn, hdr); err != nil {
		return "", err
	}
	n := int(binary.LittleEndian.Uint32(hdr[12:16]))
	if n < 0 || n > 1<<20 {
		return "", fmt.Errorf("xenstore: bad length")
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(conn, body); err != nil {
			return "", err
		}
	}
	if binary.LittleEndian.Uint32(hdr[0:4]) == 16 { // XS_ERROR
		return "", fmt.Errorf("xenstore: %s", strings.TrimRight(string(body), "\x00"))
	}
	return strings.TrimRight(string(body), "\x00"), nil
}
