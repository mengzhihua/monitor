//go:build linux

package collect

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func readAuditNetlink() (auditStatus, bool) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_AUDIT)
	if err != nil {
		return auditStatus{}, false
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return auditStatus{}, false
	}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	hdr := make([]byte, 16)
	binary.LittleEndian.PutUint32(hdr[0:4], 16)
	binary.LittleEndian.PutUint16(hdr[4:6], 1000) // AUDIT_GET
	binary.LittleEndian.PutUint16(hdr[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	binary.LittleEndian.PutUint32(hdr[8:12], 1)
	if _, err := unix.Write(fd, hdr); err != nil {
		return auditStatus{}, false
	}
	buf := make([]byte, 8192)
	n, err := unix.Read(fd, buf)
	if err != nil || n < 16 {
		return auditStatus{}, false
	}
	return parseAuditNetlink(buf[:n])
}
