package tsdb

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"testing"
)

var decodedValue float64

func referenceBits(data []byte, pos, width uint) (uint64, uint, error) {
	var value uint64
	for i := uint(0); i < width; i++ {
		if pos>>3 >= uint(len(data)) {
			return 0, pos, errEOF
		}
		value = value<<1 | uint64(data[pos>>3]>>(7-(pos&7))&1)
		pos++
	}
	return value, pos, nil
}

func TestBitReaderEveryOffsetWidthAndTail(t *testing.T) {
	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(0x96 + i*71)
	}
	// Exercise unaligned word loads, a ninth byte, every short tail, zero
	// width, legacy widths above 64, and the exact cursor after truncation.
	for size := 0; size <= len(data); size++ {
		for start := uint(0); start <= uint(size*8+1); start++ {
			for width := uint(0); width <= 128; width++ {
				reader := bitReader{buf: data[:size], pos: start}
				got, err := reader.readBits(width)
				want, end, wantErr := referenceBits(data[:size], start, width)
				if got != want || err != wantErr || reader.pos != end {
					t.Fatalf("size=%d start=%d width=%d: got (%x,%d,%v), want (%x,%d,%v)", size, start, width,
						got, reader.pos, err, want, end, wantErr)
				}
			}
		}
	}
}

func TestGorillaWideTimestampAndValueFields(t *testing.T) {
	ts := []int64{0, 1, 65, 400, 4000, 1 << 40, 1<<40 + 1, 1<<40 + 2}
	patterns := []uint64{0, 1 << 63, 0x7ff0000000000000, 0xfff0000000000000,
		0x7fefffffffffffff, 1, 0x7ff8000000000012, 0x123456789abcdef0}
	values := make([]float64, len(patterns))
	for i, value := range patterns {
		values[i] = math.Float64frombits(value)
	}
	gotTS, gotValues, err := decodeBlock(encodeBlock(ts, values), len(ts))
	if err != nil {
		t.Fatal(err)
	}
	for i := range ts {
		if gotTS[i] != ts[i] || math.Float64bits(gotValues[i]) != patterns[i] {
			t.Fatalf("sample %d: got (%d,%x), want (%d,%x)", i, gotTS[i], math.Float64bits(gotValues[i]), ts[i], patterns[i])
		}
	}
}

type shortHeaderReader struct{ io.Reader }

func (r shortHeaderReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func encodedTestHeader(id string) []byte {
	header := append([]byte(blockMagic), blockVersion)
	header = binary.LittleEndian.AppendUint16(header, uint16(len(id)))
	header = append(header, id...)
	header = binary.LittleEndian.AppendUint64(header, 1700000000)
	header = binary.LittleEndian.AppendUint64(header, 1700003599)
	header = binary.LittleEndian.AppendUint32(header, 3600)
	return binary.LittleEndian.AppendUint32(header, 1234)
}

func TestBlockHeaderShortReadsAndTruncation(t *testing.T) {
	const id = "system.cpu|用户"
	header := encodedTestHeader(id)
	for _, short := range []bool{false, true} {
		t.Run(fmt.Sprintf("short=%t", short), func(t *testing.T) {
			for size := 0; size <= len(header); size++ {
				var reader io.Reader = bytes.NewReader(header[:size])
				if short {
					reader = shortHeaderReader{reader}
				}
				gotID, meta, dataLen, err := parseHeader(reader)
				if size < len(header) {
					if err == nil {
						t.Fatalf("accepted header truncated at %d", size)
					}
				} else if err != nil || gotID != id || meta.start != 1700000000 || meta.end != 1700003599 || meta.count != 3600 || dataLen != 1234 {
					t.Fatalf("header: id=%q meta=%+v bytes=%d error=%v", gotID, meta, dataLen, err)
				}
			}
		})
	}
	reader := bytes.NewReader(append(header, 1, 2, 3))
	if _, _, _, err := parseHeader(reader); err != nil || reader.Len() != 3 {
		t.Fatalf("header consumed payload: remaining=%d error=%v", reader.Len(), err)
	}
}

func TestBlockHeaderRejectsInvalidFields(t *testing.T) {
	for name, mutate := range map[string]func([]byte){
		"magic":   func(b []byte) { b[0] = 0 },
		"version": func(b []byte) { b[4]++ },
		"count_zero": func(b []byte) {
			binary.LittleEndian.PutUint32(b[7+1+16:], 0)
		},
		"count_limit": func(b []byte) {
			binary.LittleEndian.PutUint32(b[7+1+16:], maxBlockSamples+1)
		},
		"byte_limit": func(b []byte) {
			binary.LittleEndian.PutUint32(b[7+1+20:], maxBlockBytes+1)
		},
		"reversed_range": func(b []byte) {
			binary.LittleEndian.PutUint64(b[7+1+8:], 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			header := encodedTestHeader("x")
			mutate(header)
			if _, _, _, err := parseHeader(bytes.NewReader(header)); err == nil {
				t.Fatal("invalid header accepted")
			}
		})
	}
}

func codecSeries(n int, kind string) ([]int64, []float64) {
	ts, values := make([]int64, n), make([]float64, n)
	for i := range ts {
		ts[i] = 1700000000 + int64(i)
		switch kind {
		case "counter":
			values[i] = float64(i % 100)
		case "gauge":
			values[i] = 50 + 25*math.Sin(float64(i)/13) + math.Cos(float64(i)/3)
		case "constant":
			values[i] = 42
		}
	}
	return ts, values
}

func BenchmarkDecodeBlock3600(b *testing.B) {
	for _, kind := range []string{"counter", "gauge", "constant"} {
		b.Run(kind, func(b *testing.B) {
			ts, values := codecSeries(3600, kind)
			data := encodeBlock(ts, values)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var sum float64
				if err := decodeBlockEach(data, len(ts), func(_ int64, value float64) { sum += value }); err != nil {
					b.Fatal(err)
				}
				decodedValue = sum
			}
		})
	}
}

func BenchmarkReadBlockData3600(b *testing.B) {
	ts, values := codecSeries(3600, "gauge")
	block, err := writeBlock(b.TempDir(), "system.cpu|user", ts, values)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		meta, data, err := readBlockData(block.path)
		if err != nil || meta.count != len(ts) {
			b.Fatalf("block count=%d, error=%v", meta.count, err)
		}
		putBuf(data)
	}
}
