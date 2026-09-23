package collect

import "encoding/binary"

// freebsdTCPFields is the FreeBSD 13/14 struct tcpstat prefix (uint64, little endian).
// sysctl -e prints dotted keys when the kernel exports them; -b returns this struct.
var freebsdTCPFields = []string{
	"tcps_connattempt", "tcps_accepts", "tcps_connects", "tcps_drops", "tcps_conndrops",
	"tcps_closed", "tcps_segstimed", "tcps_rttupdated", "tcps_delack", "tcps_timeoutdrop",
	"tcps_rexmttimeo", "tcps_persisttimeo", "tcps_keeptimeo", "tcps_keepprobe", "tcps_keepdrops",
	"tcps_sndtotal", "tcps_sndpack", "tcps_sndbyte", "tcps_sndrexmitpack", "tcps_sndrexmitbyte",
	"tcps_sndrexmitbad", "tcps_sndacks", "tcps_sndprobe", "tcps_sndurg", "tcps_sndwinup",
	"tcps_sndctrl", "tcps_rcvtotal", "tcps_rcvpack", "tcps_rcvbyte", "tcps_rcvbadsum",
	"tcps_rcvbadoff", "tcps_rcvmemdrop", "tcps_rcvshort", "tcps_rcvduppack", "tcps_rcvdupbyte",
	"tcps_rcvpartduppack", "tcps_rcvpartdupbyte", "tcps_rcvoopack", "tcps_rcvoobyte",
	"tcps_rcvpackafterwin", "tcps_rcvbyteafterwin", "tcps_rcvafterclose", "tcps_rcvwinprobe",
	"tcps_rcvdupack", "tcps_rcvacktoomuch", "tcps_rcvackpack", "tcps_rcvackbyte", "tcps_rcvwinupd",
	"tcps_pawsdrop", "tcps_predack", "tcps_preddat", "tcps_pcbcachemiss",
	"tcps_cachedrtt", "tcps_cachedrttvar", "tcps_cachedssthresh",
	"tcps_usedrtt", "tcps_usedrttvar", "tcps_usedssthresh",
	"tcps_persistdrop", "tcps_badsyn", "tcps_mturesent", "tcps_listendrop",
}

func decodeTCPStat(b []byte) map[string]string {
	out := map[string]string{}
	for i, name := range freebsdTCPFields {
		off := i * 8
		if off+8 > len(b) {
			break
		}
		out["net.inet.tcp.stats."+name] = u64dec(b[off : off+8])
	}
	return out
}

func u64dec(b []byte) string {
	return strconvFormat(binary.LittleEndian.Uint64(b))
}

func strconvFormat(v uint64) string {
	return binToDec(v)
}

func binToDec(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
