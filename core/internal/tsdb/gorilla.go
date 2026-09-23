package tsdb

import (
	"encoding/binary"
	"errors"
	"math"
	"math/bits"
)

// Gorilla-style block encoding (Facebook Gorilla paper):
// timestamps as delta-of-delta with variable-length prefixes, values as XOR
// against the previous value with leading/trailing-zero reuse.

type bitWriter struct {
	buf   []byte
	nbits uint // bits used in the last byte (0..8)
}

func (w *bitWriter) writeBit(b bool) {
	if w.nbits == 0 || w.nbits == 8 {
		w.buf = append(w.buf, 0)
		w.nbits = 0
	}
	if b {
		w.buf[len(w.buf)-1] |= 1 << (7 - w.nbits)
	}
	w.nbits++
}

func (w *bitWriter) writeBits(v uint64, n uint) {
	for n > 0 {
		if w.nbits == 0 || w.nbits == 8 {
			w.buf = append(w.buf, 0)
			w.nbits = 0
		}
		room := 8 - w.nbits
		take := room
		if take > n {
			take = n
		}
		shift := n - take
		chunk := v >> shift
		if take < 64 {
			chunk &= (1 << take) - 1
		}
		w.buf[len(w.buf)-1] |= byte(chunk) << (room - take)
		w.nbits += take
		n = shift
	}
}

func (w *bitWriter) bytes() []byte { return w.buf }

type bitReader struct {
	buf []byte
	pos uint // bit position
}

var errEOF = errors.New("tsdb: unexpected end of block")

func (r *bitReader) readBit() (bool, error) {
	if r.pos>>3 >= uint(len(r.buf)) {
		return false, errEOF
	}
	b := r.buf[r.pos>>3]&(1<<(7-r.pos&7)) != 0
	r.pos++
	return b, nil
}

func (r *bitReader) readBits(n uint) (uint64, error) {
	if n == 0 {
		return 0, nil
	}
	bytePos, offset := r.pos>>3, r.pos&7
	if bytePos >= uint(len(r.buf)) {
		return 0, errEOF
	}
	if n <= 8-offset {
		v := uint64(r.buf[bytePos]>>(8-offset-n)) & ((1 << n) - 1)
		r.pos += n
		return v, nil
	}
	// Values commonly span most of a machine word. Read that word once rather
	// than assembling it byte by byte; an unaligned 64-bit field can need one
	// extra byte. The short tail keeps the bounded reader below.
	if n <= 64 && uint(len(r.buf))-bytePos >= 8 {
		v := binary.BigEndian.Uint64(r.buf[bytePos:]) << offset
		if n+offset > 64 {
			if bytePos+8 >= uint(len(r.buf)) {
				r.pos = uint(len(r.buf)) * 8
				return 0, errEOF
			}
			v |= uint64(r.buf[bytePos+8]) >> (8 - offset)
		}
		r.pos += n
		return v >> (64 - n), nil
	}
	var v uint64
	for n > 0 {
		if r.pos>>3 >= uint(len(r.buf)) {
			return 0, errEOF
		}
		room := 8 - (r.pos & 7)
		take := room
		if take > n {
			take = n
		}
		shift := room - take
		chunk := uint64(r.buf[r.pos>>3]>>shift) & ((1 << take) - 1)
		v = (v << take) | chunk
		r.pos += take
		n -= take
	}
	return v, nil
}

// encodeBlock compresses parallel timestamp/value slices. ts must be ascending.
func encodeBlock(ts []int64, vals []float64) []byte {
	w := &bitWriter{}
	if len(ts) == 0 {
		return nil
	}
	// header: first timestamp (64 bits), first value (64 bits)
	w.writeBits(uint64(ts[0]), 64)
	w.writeBits(math.Float64bits(vals[0]), 64)
	var prevDelta int64
	prevTS := ts[0]
	prevVal := math.Float64bits(vals[0])
	var prevLead, prevTrail uint8 = 0xff, 0
	for i := 1; i < len(ts); i++ {
		delta := ts[i] - prevTS
		dod := delta - prevDelta
		prevTS, prevDelta = ts[i], delta
		switch {
		case dod == 0:
			w.writeBit(false)
		case dod >= -63 && dod <= 64:
			w.writeBits(0b10, 2)
			w.writeBits(uint64(dod+63), 7)
		case dod >= -255 && dod <= 256:
			w.writeBits(0b110, 3)
			w.writeBits(uint64(dod+255), 9)
		case dod >= -2047 && dod <= 2048:
			w.writeBits(0b1110, 4)
			w.writeBits(uint64(dod+2047), 12)
		default:
			w.writeBits(0b1111, 4)
			w.writeBits(uint64(dod), 64)
		}

		cur := math.Float64bits(vals[i])
		x := cur ^ prevVal
		prevVal = cur
		if x == 0 {
			w.writeBit(false)
			continue
		}
		w.writeBit(true)
		lead := uint8(bits.LeadingZeros64(x))
		trail := uint8(bits.TrailingZeros64(x))
		if lead > 31 {
			lead = 31
		}
		if prevLead != 0xff && lead >= prevLead && trail >= prevTrail {
			w.writeBit(false)
			w.writeBits(x>>prevTrail, uint(64-prevLead-prevTrail))
			continue
		}
		prevLead, prevTrail = lead, trail
		sig := 64 - lead - trail
		w.writeBit(true)
		w.writeBits(uint64(lead), 5)
		w.writeBits(uint64(sig), 6) // sig in 1..64; 64 wraps to 0 and is handled on decode
		w.writeBits(x>>trail, uint(sig))
	}
	return w.bytes()
}

// decodeBlock reverses encodeBlock; n is the number of samples in the block.
func decodeBlock(buf []byte, n int) ([]int64, []float64, error) {
	if n == 0 || len(buf) == 0 {
		return nil, nil, nil
	}
	ts := make([]int64, 0, n)
	vals := make([]float64, 0, n)
	err := decodeBlockEach(buf, n, func(t int64, v float64) {
		ts = append(ts, t)
		vals = append(vals, v)
	})
	if err != nil {
		return nil, nil, err
	}
	return ts, vals, nil
}

// decodeBlockEach visits samples in timestamp order. An empty buffer yields
// no samples and no error, matching decodeBlock. A truncated stream returns
// an error after any samples already visited, so callers that must keep the
// block atomic need to roll back.
func decodeBlockEach(buf []byte, n int, fn func(ts int64, v float64)) error {
	if n == 0 || len(buf) == 0 {
		return nil
	}
	r := &bitReader{buf: buf}
	t0, err := r.readBits(64)
	if err != nil {
		return err
	}
	v0, err := r.readBits(64)
	if err != nil {
		return err
	}
	fn(int64(t0), math.Float64frombits(v0))
	prevTS, prevVal := int64(t0), v0
	var prevDelta int64
	var lead, trail uint8
	for i := 1; i < n; i++ {
		var dod int64
		b, err := r.readBit()
		if err != nil {
			return err
		}
		if b {
			b2, _ := r.readBit()
			if !b2 {
				v, err := r.readBits(7)
				if err != nil {
					return err
				}
				dod = int64(v) - 63
			} else {
				b3, _ := r.readBit()
				if !b3 {
					v, err := r.readBits(9)
					if err != nil {
						return err
					}
					dod = int64(v) - 255
				} else {
					b4, _ := r.readBit()
					if !b4 {
						v, err := r.readBits(12)
						if err != nil {
							return err
						}
						dod = int64(v) - 2047
					} else {
						v, err := r.readBits(64)
						if err != nil {
							return err
						}
						dod = int64(v)
					}
				}
			}
		}
		prevDelta += dod
		prevTS += prevDelta

		b, err = r.readBit()
		if err != nil {
			return err
		}
		if !b {
			fn(prevTS, math.Float64frombits(prevVal))
			continue
		}
		ctrl, err := r.readBit()
		if err != nil {
			return err
		}
		if ctrl {
			l, err := r.readBits(5)
			if err != nil {
				return err
			}
			s, err := r.readBits(6)
			if err != nil {
				return err
			}
			lead = uint8(l)
			sig := uint8(s)
			if sig == 0 {
				sig = 64
			}
			trail = 64 - lead - sig
		}
		x, err := r.readBits(uint(64 - lead - trail))
		if err != nil {
			return err
		}
		prevVal ^= x << trail
		fn(prevTS, math.Float64frombits(prevVal))
	}
	return nil
}
