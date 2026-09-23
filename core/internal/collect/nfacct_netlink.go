package collect

import "encoding/binary"

// parseNfacctNetlink decodes an NFNETLINK_ACCT dump. Names are NFACCT_NAME,
// counters are big-endian 64-bit NFACCT_PKTS and NFACCT_BYTES.
func parseNfacctNetlink(buf []byte) []nfacctObj {
	var out []nfacctObj
	for len(buf) >= 16 {
		ln := int(binary.LittleEndian.Uint32(buf[0:4]))
		if ln < 16 || ln > len(buf) {
			break
		}
		payload := buf[16:ln]
		if len(payload) >= 4 {
			payload = payload[4:] // nfgenmsg
			var obj nfacctObj
			for len(payload) >= 4 {
				al := int(binary.BigEndian.Uint16(payload[0:2]))
				if al < 4 || al > len(payload) {
					break
				}
				typ := binary.BigEndian.Uint16(payload[2:4])
				val := payload[4:al]
				switch typ {
				case 1: // NFACCT_NAME
					if i := indexByte(val, 0); i >= 0 {
						val = val[:i]
					}
					obj.Name = string(val)
				case 2: // NFACCT_PKTS
					if len(val) >= 8 {
						obj.Packets = float64(binary.BigEndian.Uint64(val[:8]))
					}
				case 3: // NFACCT_BYTES
					if len(val) >= 8 {
						obj.Bytes = float64(binary.BigEndian.Uint64(val[:8]))
					}
				}
				pad := (4 - al%4) % 4
				next := al + pad
				if next > len(payload) {
					break
				}
				payload = payload[next:]
			}
			if obj.Name != "" {
				out = append(out, obj)
			}
		}
		next := ln
		if ln%4 != 0 {
			next += 4 - ln%4
		}
		if next > len(buf) {
			break
		}
		buf = buf[next:]
	}
	return out
}

func indexByte(b []byte, c byte) int {
	for i, x := range b {
		if x == c {
			return i
		}
	}
	return -1
}
