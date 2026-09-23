package collect

import "encoding/binary"

// parseAuditNetlink reads NETLINK_AUDIT replies. AUDIT_GET (1000) carries
// struct audit_status; the first nine u32 fields are enabled, failure, pid,
// rate, backlog limit, lost, backlog, version, and backlog wait time.
func parseAuditNetlink(buf []byte) (auditStatus, bool) {
	var st auditStatus
	ok := false
	for len(buf) >= 16 {
		ln := int(binary.LittleEndian.Uint32(buf[0:4]))
		if ln < 16 || ln > len(buf) {
			break
		}
		typ := binary.LittleEndian.Uint16(buf[4:6])
		payload := buf[16:ln]
		if typ == 1000 && len(payload) >= 32 {
			u := func(i int) float64 {
				return float64(binary.LittleEndian.Uint32(payload[i : i+4]))
			}
			st.Enabled = u(4)
			st.Failure = u(8)
			st.BacklogLimit = u(20)
			st.Lost = u(24)
			st.Backlog = u(28)
			ok = true
		}
		buf = buf[ln:]
		if ln%4 != 0 {
			pad := 4 - ln%4
			if pad > len(buf) {
				break
			}
			buf = buf[pad:]
		}
	}
	return st, ok
}
