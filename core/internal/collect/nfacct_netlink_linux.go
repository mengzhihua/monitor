//go:build linux

package collect

import (
	"encoding/binary"

	"golang.org/x/sys/unix"
)

func readNfacctNetlink() ([]nfacctObj, bool) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, false
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, false
	}
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 1})
	// (NFNL_SUBSYS_ACCT << 8) | NFNL_MSG_ACCT_GET
	const msgType = (7 << 8) | 1
	hdr := make([]byte, 20)
	binary.LittleEndian.PutUint32(hdr[0:4], 20)
	binary.LittleEndian.PutUint16(hdr[4:6], msgType)
	binary.LittleEndian.PutUint16(hdr[6:8], unix.NLM_F_REQUEST|unix.NLM_F_DUMP)
	binary.LittleEndian.PutUint32(hdr[8:12], 1)
	hdr[16] = unix.AF_UNSPEC
	hdr[17] = 0 // nfnetlink version
	if _, err := unix.Write(fd, hdr); err != nil {
		return nil, false
	}
	var all []byte
	buf := make([]byte, 65536)
	for {
		n, err := unix.Read(fd, buf)
		if err != nil || n < 16 {
			break
		}
		typ := binary.LittleEndian.Uint16(buf[4:6])
		if typ == unix.NLMSG_DONE {
			break
		}
		if typ == unix.NLMSG_ERROR {
			return nil, false
		}
		all = append(all, buf[:n]...)
		if binary.LittleEndian.Uint16(buf[6:8])&unix.NLM_F_MULTI == 0 {
			break
		}
	}
	objs := parseNfacctNetlink(all)
	if len(objs) == 0 {
		return nil, false
	}
	return objs, true
}
