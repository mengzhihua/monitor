package tsdb

import (
	"bytes"
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
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
		if err := encodeGobRecord(w, row); err != nil {
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
			if err := encodeGobRecord(w, rollup); err != nil {
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
