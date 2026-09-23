package collect

import "encoding/binary"

// parseNftNetlink decodes an nftables GETOBJ dump. Only named counters
// (NFT_OBJECT_COUNTER) are returned. Packets and bytes are big-endian.
func parseNftNetlink(buf []byte) []nftCounter {
	var out []nftCounter
	for len(buf) >= 16 {
		ln := int(binary.LittleEndian.Uint32(buf[0:4]))
		if ln < 16 || ln > len(buf) {
			break
		}
		typ := binary.LittleEndian.Uint16(buf[4:6])
		if typ != 2 && typ != 3 && ln >= 20 { // skip NLMSG_ERROR and NLMSG_DONE
			out = append(out, parseNftObj(buf[20:ln])...)
		}
		next := ln
		if ln%4 != 0 {
			next += 4 - ln%4
		}
		if next > len(buf) || next == 0 {
			break
		}
		buf = buf[next:]
	}
	return out
}

func parseNftObj(payload []byte) []nftCounter {
	var table, name string
	var kind uint32
	var data []byte
	for len(payload) >= 4 {
		al := int(binary.BigEndian.Uint16(payload[0:2]))
		if al < 4 || al > len(payload) {
			break
		}
		typ := binary.BigEndian.Uint16(payload[2:4])
		val := payload[4:al]
		switch typ {
		case 1: // NFTA_OBJ_TABLE
			table = trimCString(val)
		case 2: // NFTA_OBJ_NAME
			name = trimCString(val)
		case 3: // NFTA_OBJ_TYPE
			if len(val) >= 4 {
				kind = binary.BigEndian.Uint32(val[:4])
			}
		case 4: // NFTA_OBJ_DATA
			data = val
		}
		pad := (4 - al%4) % 4
		next := al + pad
		if next > len(payload) || next == 0 {
			break
		}
		payload = payload[next:]
	}
	if kind != 1 || name == "" { // NFT_OBJECT_COUNTER
		return nil
	}
	if table == "" {
		table = "filter"
	}
	c := nftCounter{Table: table, Name: name}
	for len(data) >= 4 {
		al := int(binary.BigEndian.Uint16(data[0:2]))
		if al < 4 || al > len(data) {
			break
		}
		typ := binary.BigEndian.Uint16(data[2:4])
		val := data[4:al]
		switch typ {
		case 1: // NFTA_COUNTER_BYTES
			if len(val) >= 8 {
				c.Bytes = float64(binary.BigEndian.Uint64(val[:8]))
			}
		case 2: // NFTA_COUNTER_PACKETS
			if len(val) >= 8 {
				c.Packets = float64(binary.BigEndian.Uint64(val[:8]))
			}
		}
		pad := (4 - al%4) % 4
		next := al + pad
		if next > len(data) || next == 0 {
			break
		}
		data = data[next:]
	}
	return []nftCounter{c}
}
