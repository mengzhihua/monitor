package collect

import (
	"encoding/binary"
	"strings"
)

// xenDomainsFrom turns a xenstore directory listing and path reads into the
// same rows `xl list` produces. memory/target is KiB; charts use MiB.
func xenDomainsFrom(ids []string, reads map[string]string) []xlDomain {
	var out []xlDomain
	for _, id := range ids {
		if id == "" {
			continue
		}
		base := "/local/domain/" + id
		name := reads[base+"/name"]
		if name == "" {
			name = "domain-" + id
		}
		out = append(out, xlDomain{
			Name:  name,
			State: "r",
			ID:    firstFloat(id),
			Mem:   firstFloat(reads[base+"/memory/target"]) / 1024,
		})
	}
	return out
}

func xsPack(op uint32, payload string) []byte {
	if !strings.HasSuffix(payload, "\x00") {
		payload += "\x00"
	}
	buf := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], op)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(payload)))
	copy(buf[16:], payload)
	return buf
}

func xsSplit(payload string) []string {
	var out []string
	for _, p := range strings.Split(payload, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
