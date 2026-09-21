package export

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
)

// Prometheus remote write (snappy + protobuf WriteRequest). Encoded without
// generated stubs so we keep the Agent dependency-free.

func (e *Engine) flushPromRW(ctx context.Context, d Destination) error {
	url := d.URL
	if url == "" {
		return fmt.Errorf("prometheus: url required")
	}
	var series [][]byte
	for _, p := range e.snapshot() {
		name := d.Prefix + "_" + promIdent(p.chart) + "_" + promIdent(p.dim)
		ts := &promTS{}
		ts.label("__name__", name)
		ts.label("instance", e.host)
		ts.label("chart", p.chart)
		ts.label("dimension", p.dim)
		ts.sample(p.value, p.ts*1000)
		series = append(series, ts.encode())
	}
	var body []byte
	for _, s := range series {
		body = protoBytes(body, 1, s) // WriteRequest.timeseries = 1
	}
	compressed := snappyEncode(body)
	headers := map[string]string{
		"Content-Encoding":                  "snappy",
		"X-Prometheus-Remote-Write-Version": "0.1.0",
	}
	for k, v := range d.Headers {
		headers[k] = v
	}
	return e.post(ctx, url, "application/x-protobuf", compressed, headers)
}

type promTS struct{ buf []byte }

func (t *promTS) label(k, v string) {
	var m []byte
	m = protoString(m, 1, k)
	m = protoString(m, 2, v)
	t.buf = protoBytes(t.buf, 1, m)
}

func (t *promTS) sample(v float64, ts int64) {
	var m []byte
	m = protoFixed64(m, 1, math.Float64bits(v))
	m = protoVarint(m, 2, uint64(ts))
	t.buf = protoBytes(t.buf, 2, m)
}

func (t *promTS) encode() []byte { return t.buf }

func protoString(dst []byte, field int, s string) []byte {
	return protoBytes(dst, field, []byte(s))
}

func protoBytes(dst []byte, field int, v []byte) []byte {
	dst = appendVarint(dst, uint64(field<<3|2))
	dst = appendVarint(dst, uint64(len(v)))
	return append(dst, v...)
}

func protoVarint(dst []byte, field int, v uint64) []byte {
	dst = appendVarint(dst, uint64(field<<3))
	return appendVarint(dst, v)
}

func protoFixed64(dst []byte, field int, v uint64) []byte {
	dst = appendVarint(dst, uint64(field<<3|1))
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], v)
	return append(dst, b[:]...)
}

func appendVarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

func promIdent(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, s)
}

// snappyEncode writes a valid (uncompressed-literal) snappy block.
func snappyEncode(src []byte) []byte {
	out := appendUvarint(nil, uint64(len(src)))
	for len(src) > 0 {
		n := len(src)
		if n > 65536 {
			n = 65536
		}
		out = snappyLiteral(out, src[:n])
		src = src[n:]
	}
	return out
}

func snappyLiteral(dst, lit []byte) []byte {
	n := len(lit)
	switch {
	case n == 0:
		return dst
	case n < 61:
		dst = append(dst, byte((n-1)<<2))
	case n < 256:
		dst = append(dst, 60<<2, byte(n-1))
	default:
		dst = append(dst, 61<<2, byte(n-1), byte((n-1)>>8))
	}
	return append(dst, lit...)
}

func appendUvarint(dst []byte, v uint64) []byte {
	for v >= 0x80 {
		dst = append(dst, byte(v)|0x80)
		v >>= 7
	}
	return append(dst, byte(v))
}

// snappyDecodedLen is used by tests.
func snappyDecodedLen(b []byte) (int, []byte, error) {
	n, sz := binary.Uvarint(b)
	if sz <= 0 {
		return 0, nil, fmt.Errorf("bad snappy")
	}
	return int(n), b[sz:], nil
}
