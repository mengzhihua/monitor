package tsdb

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"math"
	"sync"
)

// Streaming checkpoint frame (MONCP002): length-prefixed gob records so the
// writer never materializes a full checkpointImage. Layout:
//
//	magic MONCP002
//	uvarint rawCount
//	rawCount × (uvarint len + gob rawCheckpoint)
//	uvarint tierCount
//	tierCount × (
//	  uvarint every
//	  uvarint seriesCount
//	  seriesCount × (uvarint len + gob rollupCheckpoint)
//	)
//	sha256 of everything above
const checkpointMagicV2 = "MONCP002"
const checkpointMagicV3 = "MONCP003"

var (
	rawCheckpointPool    = sync.Pool{New: func() any { return &rawCheckpoint{} }}
	rollupCheckpointPool = sync.Pool{New: func() any { return &rollupCheckpoint{} }}
	gobBufPool           = sync.Pool{New: func() any { return &bytes.Buffer{} }}
)

func writeUvarint(w io.Writer, v uint64) error {
	var buf [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(buf[:], v)
	_, err := w.Write(buf[:n])
	return err
}

func readUvarint(r io.ByteReader) (uint64, error) {
	return binary.ReadUvarint(r)
}

func encodeGobRecord(w io.Writer, v any) error {
	buf := gobBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer gobBufPool.Put(buf)
	if err := gob.NewEncoder(buf).Encode(v); err != nil {
		return err
	}
	if err := writeUvarint(w, uint64(buf.Len())); err != nil {
		return err
	}
	_, err := w.Write(buf.Bytes())
	return err
}

func decodeGobRecord(r io.Reader, br io.ByteReader, v any) error {
	n, err := readUvarint(br)
	if err != nil {
		return err
	}
	if n > uint64(maxCheckpointBytes) {
		return fmt.Errorf("checkpoint record exceeds size limit")
	}
	return gob.NewDecoder(io.LimitReader(r, int64(n))).Decode(v)
}

func (s *Store) writeCheckpointStream(w io.Writer) error {
	// Snapshot series pointers under the store lock, then copy each row with a
	// pooled scratch so peak RSS stays near one series (not the whole image).
	s.mu.RLock()
	rawIDs := make([]*series, 0, len(s.series))
	for _, sr := range s.series {
		rawIDs = append(rawIDs, sr)
	}
	s.mu.RUnlock()
	if err := writeUvarint(w, uint64(len(rawIDs))); err != nil {
		return err
	}
	row := rawCheckpointPool.Get().(*rawCheckpoint)
	defer func() {
		row.ID, row.Last = "", 0
		row.TS = row.TS[:0]
		row.Values = row.Values[:0]
		rawCheckpointPool.Put(row)
	}()
	for _, sr := range rawIDs {
		sr.mu.Lock()
		row.ID = sr.id
		row.Last = sr.last
		row.TS = append(row.TS[:0], sr.ts...)
		row.Values = append(row.Values[:0], sr.vals...)
		sr.mu.Unlock()
		if err := writeRawCompact(w, row); err != nil {
			return err
		}
	}

	if err := writeUvarint(w, uint64(len(s.tiers))); err != nil {
		return err
	}
	rollup := rollupCheckpointPool.Get().(*rollupCheckpoint)
	defer func() {
		rollup.ID = ""
		rollup.Done = rollup.Done[:0]
		rollup.Open = nil
		rollupCheckpointPool.Put(rollup)
	}()
	for _, t := range s.tiers {
		if err := writeUvarint(w, uint64(t.spec.Every)); err != nil {
			return err
		}
		tierSeries := t.all()
		if err := writeUvarint(w, uint64(len(tierSeries))); err != nil {
			return err
		}
		for _, sr := range tierSeries {
			sr.mu.Lock()
			rollup.ID = sr.id
			rollup.Done = append(rollup.Done[:0], sr.done...)
			if sr.open != nil {
				b := *sr.open
				rollup.Open = &b
			} else {
				rollup.Open = nil
			}
			sr.mu.Unlock()
			if err := writeRollupCompact(w, rollup); err != nil {
				return err
			}
		}
	}
	return nil
}

type byteReader struct {
	r io.Reader
	b [1]byte
}

func (b *byteReader) ReadByte() (byte, error) {
	_, err := io.ReadFull(b.r, b.b[:])
	return b.b[0], err
}

func zigzag(n int64) uint64   { return uint64((n << 1) ^ (n >> 63)) }
func unzigzag(n uint64) int64 { return int64((n >> 1) ^ -(n & 1)) }

func writeString(w io.Writer, s string) error {
	if err := writeUvarint(w, uint64(len(s))); err != nil {
		return err
	}
	_, err := io.WriteString(w, s)
	return err
}

func readString(br *byteReader) (string, error) {
	n, err := readUvarint(br)
	if err != nil {
		return "", err
	}
	if n > 1<<20 {
		return "", fmt.Errorf("checkpoint id too long")
	}
	buf := make([]byte, n)
	if _, err = io.ReadFull(br.r, buf); err != nil {
		return "", err
	}
	return string(buf), nil
}

func writeF64(w io.Writer, v float64) error {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
	_, err := w.Write(buf[:])
	return err
}

func readF64(br *byteReader) (float64, error) {
	var buf [8]byte
	if _, err := io.ReadFull(br.r, buf[:]); err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.LittleEndian.Uint64(buf[:])), nil
}

func writeRawCompact(w io.Writer, row *rawCheckpoint) error {
	if len(row.TS) != len(row.Values) {
		return fmt.Errorf("checkpoint raw length mismatch")
	}
	if err := writeString(w, row.ID); err != nil {
		return err
	}
	if err := writeUvarint(w, zigzag(row.Last)); err != nil {
		return err
	}
	if err := writeUvarint(w, uint64(len(row.TS))); err != nil {
		return err
	}
	var prev int64
	for i, ts := range row.TS {
		delta := ts
		if i > 0 {
			delta = ts - prev
		}
		prev = ts
		if err := writeUvarint(w, zigzag(delta)); err != nil {
			return err
		}
		if err := writeF64(w, row.Values[i]); err != nil {
			return err
		}
	}
	return nil
}

func writeBucket(w io.Writer, b Bucket) error {
	if err := writeUvarint(w, zigzag(b.TS)); err != nil {
		return err
	}
	for _, v := range []float64{b.Min, b.Max, b.Sum, b.Last} {
		if err := writeF64(w, v); err != nil {
			return err
		}
	}
	return writeUvarint(w, zigzag(b.Count))
}

func readBucket(br *byteReader) (Bucket, error) {
	var b Bucket
	ts, err := readUvarint(br)
	if err != nil {
		return b, err
	}
	b.TS = unzigzag(ts)
	for _, dst := range []*float64{&b.Min, &b.Max, &b.Sum, &b.Last} {
		v, err := readF64(br)
		if err != nil {
			return b, err
		}
		*dst = v
	}
	n, err := readUvarint(br)
	if err != nil {
		return b, err
	}
	b.Count = unzigzag(n)
	return b, nil
}

func writeRollupCompact(w io.Writer, row *rollupCheckpoint) error {
	if err := writeString(w, row.ID); err != nil {
		return err
	}
	if err := writeUvarint(w, uint64(len(row.Done))); err != nil {
		return err
	}
	for _, b := range row.Done {
		if err := writeBucket(w, b); err != nil {
			return err
		}
	}
	if row.Open == nil {
		return writeUvarint(w, 0)
	}
	if err := writeUvarint(w, 1); err != nil {
		return err
	}
	return writeBucket(w, *row.Open)
}

func (s *Store) loadCheckpointStreamV3(r io.Reader) error {
	br := &byteReader{r: r}
	rawCount, err := readUvarint(br)
	if err != nil {
		return err
	}
	if rawCount > uint64(maxCheckpointBytes) {
		return fmt.Errorf("invalid checkpoint raw count: %d", rawCount)
	}
	image := checkpointImage{Version: 1, Raw: make([]rawCheckpoint, 0, rawCount)}
	for i := uint64(0); i < rawCount; i++ {
		id, err := readString(br)
		if err != nil {
			return err
		}
		last, err := readUvarint(br)
		if err != nil {
			return err
		}
		n, err := readUvarint(br)
		if err != nil {
			return err
		}
		if n > uint64(maxCheckpointBytes/8) {
			return fmt.Errorf("checkpoint series too long")
		}
		row := rawCheckpoint{ID: id, Last: unzigzag(last), TS: make([]int64, n), Values: make([]float64, n)}
		var prev int64
		for j := uint64(0); j < n; j++ {
			delta, err := readUvarint(br)
			if err != nil {
				return err
			}
			ts := unzigzag(delta)
			if j > 0 {
				ts += prev
			}
			prev = ts
			row.TS[j] = ts
			v, err := readF64(br)
			if err != nil {
				return err
			}
			row.Values[j] = v
		}
		image.Raw = append(image.Raw, row)
	}
	tierCount, err := readUvarint(br)
	if err != nil {
		return err
	}
	image.Tiers = make([]tierCheckpoint, 0, tierCount)
	for i := uint64(0); i < tierCount; i++ {
		every, err := readUvarint(br)
		if err != nil {
			return err
		}
		nSeries, err := readUvarint(br)
		if err != nil {
			return err
		}
		tier := tierCheckpoint{Every: int64(every), Series: make([]rollupCheckpoint, 0, nSeries)}
		for j := uint64(0); j < nSeries; j++ {
			id, err := readString(br)
			if err != nil {
				return err
			}
			nDone, err := readUvarint(br)
			if err != nil {
				return err
			}
			row := rollupCheckpoint{ID: id, Done: make([]Bucket, 0, nDone)}
			for k := uint64(0); k < nDone; k++ {
				b, err := readBucket(br)
				if err != nil {
					return err
				}
				row.Done = append(row.Done, b)
			}
			hasOpen, err := readUvarint(br)
			if err != nil {
				return err
			}
			if hasOpen == 1 {
				b, err := readBucket(br)
				if err != nil {
					return err
				}
				row.Open = &b
			}
			tier.Series = append(tier.Series, row)
		}
		image.Tiers = append(image.Tiers, tier)
	}
	return s.applyCheckpointImage(image)
}

func (s *Store) loadCheckpointStream(r io.Reader) error {
	br := &byteReader{r: r}
	rawCount, err := readUvarint(br)
	if err != nil {
		return err
	}
	if rawCount > uint64(maxCheckpointBytes) {
		return fmt.Errorf("invalid checkpoint raw count: %d", rawCount)
	}
	image := checkpointImage{Version: 1, Raw: make([]rawCheckpoint, 0, rawCount)}
	for i := uint64(0); i < rawCount; i++ {
		var row rawCheckpoint
		if err := decodeGobRecord(r, br, &row); err != nil {
			return fmt.Errorf("decode raw checkpoint: %w", err)
		}
		image.Raw = append(image.Raw, row)
	}
	tierCount, err := readUvarint(br)
	if err != nil {
		return err
	}
	image.Tiers = make([]tierCheckpoint, 0, tierCount)
	for i := uint64(0); i < tierCount; i++ {
		every, err := readUvarint(br)
		if err != nil {
			return err
		}
		nSeries, err := readUvarint(br)
		if err != nil {
			return err
		}
		tier := tierCheckpoint{Every: int64(every), Series: make([]rollupCheckpoint, 0, nSeries)}
		for j := uint64(0); j < nSeries; j++ {
			var row rollupCheckpoint
			if err := decodeGobRecord(r, br, &row); err != nil {
				return fmt.Errorf("decode tier checkpoint: %w", err)
			}
			tier.Series = append(tier.Series, row)
		}
		image.Tiers = append(image.Tiers, tier)
	}
	return s.applyCheckpointImage(image)
}
