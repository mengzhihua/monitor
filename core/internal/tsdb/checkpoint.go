package tsdb

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// A checkpoint is one atomic, checksummed image of the unfinished blocks in
// every tier. Full blocks retain the existing on-disk format. This avoids a
// separate fsync for every series on every checkpoint (including open buckets).
const checkpointFile = "checkpoint.v1"

// Cap compressed checkpoint size and the decoded in-memory image. Loading
// streams through gzip/gob; after apply we drop the image so the peak stays
// near this bound instead of retaining a second full copy until GC.
const maxCheckpointBytes = 300 << 20

type rawCheckpoint struct {
	ID     string
	Last   int64
	TS     []int64
	Values []float64
}
type rollupCheckpoint struct {
	ID   string
	Done []Bucket
	Open *Bucket
}
type tierCheckpoint struct {
	Every  int64
	Series []rollupCheckpoint
}
type checkpointImage struct {
	Version int
	Raw     []rawCheckpoint
	Tiers   []tierCheckpoint
}

// checkpointImage builds a full in-memory image (legacy / tests). Production
// Flush uses writeCheckpoint, which streams MONCP002 without this allocation.
func (s *Store) checkpointImage() checkpointImage {
	image := checkpointImage{Version: 1}
	s.mu.RLock()
	for _, sr := range s.series {
		sr.mu.Lock()
		image.Raw = append(image.Raw, rawCheckpoint{sr.id, sr.last, append([]int64(nil), sr.ts...), append([]float64(nil), sr.vals...)})
		sr.mu.Unlock()
	}
	s.mu.RUnlock()
	for _, t := range s.tiers {
		tier := tierCheckpoint{Every: t.spec.Every}
		for _, sr := range t.all() {
			sr.mu.Lock()
			row := rollupCheckpoint{ID: sr.id, Done: append([]Bucket(nil), sr.done...)}
			if sr.open != nil {
				b := *sr.open
				row.Open = &b
			}
			tier.Series = append(tier.Series, row)
			sr.mu.Unlock()
		}
		image.Tiers = append(image.Tiers, tier)
	}
	return image
}

func (s *Store) writeCheckpoint() error {
	path := filepath.Join(s.opt.Dir, checkpointFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(tmp) }()
	hash := sha256.New()
	writer := io.MultiWriter(f, hash)
	if _, err = writer.Write([]byte(checkpointMagicV3)); err != nil {
		return err
	}
	z := gzip.NewWriter(writer)
	limited := &checkpointLimitWriter{w: z, left: maxCheckpointBytes}
	if err = s.writeCheckpointStream(limited); err != nil {
		return err
	}
	if err = z.Close(); err != nil {
		return err
	}
	if _, err = f.Write(hash.Sum(nil)); err != nil {
		return err
	}
	if stat, statErr := f.Stat(); statErr != nil {
		return statErr
	} else if stat.Size() > maxCheckpointBytes {
		return fmt.Errorf("compressed checkpoint exceeds size limit")
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return syncBlockDir(s.opt.Dir)
}

func (s *Store) loadCheckpoint() error {
	path := filepath.Join(s.opt.Dir, checkpointFile)
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return err
	}
	if stat.Size() < 40 || stat.Size() > maxCheckpointBytes {
		return fmt.Errorf("invalid checkpoint size: %d", stat.Size())
	}
	var checksum [32]byte
	if _, err = f.ReadAt(checksum[:], stat.Size()-32); err != nil {
		return err
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Hash the compressed image while decoding it. The trailing checksum is
	// read once up front, so a restart does not scan the file a second time.
	hash := sha256.New()
	payload := io.TeeReader(io.LimitReader(f, stat.Size()-32), hash)
	decodeErr := s.decodeCheckpoint(payload)
	if _, err = io.Copy(io.Discard, payload); err != nil && decodeErr == nil {
		decodeErr = err
	}
	if !bytes.Equal(checksum[:], hash.Sum(nil)) {
		return fmt.Errorf("checkpoint checksum mismatch: %s", path)
	}
	return decodeErr
}

func (s *Store) decodeCheckpoint(r io.Reader) error {
	var magic [8]byte
	if _, err := io.ReadFull(r, magic[:]); err != nil {
		return err
	}
	z, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer z.Close()
	limited := io.LimitReader(z, maxCheckpointBytes)
	switch string(magic[:]) {
	case checkpointMagicV3:
		return s.loadCheckpointStreamV3(limited)
	case checkpointMagicV2:
		return s.loadCheckpointStream(limited)
	case "MONCP001":
		var image checkpointImage
		if err = gob.NewDecoder(limited).Decode(&image); err != nil {
			return fmt.Errorf("decode checkpoint: %w", err)
		}
		return s.applyCheckpointImage(image)
	default:
		return fmt.Errorf("unsupported checkpoint format")
	}
}

func (s *Store) applyCheckpointImage(image checkpointImage) error {
	if image.Version != 1 {
		return fmt.Errorf("unsupported checkpoint version: %d", image.Version)
	}
	// Match by resolution: adding/removing a tier must not restore buckets into
	// another resolution. Existing block configuration changes remain explicit.
	for _, saved := range image.Tiers {
		for _, t := range s.tiers {
			if t.spec.Every != saved.Every {
				continue
			}
			for _, row := range saved.Series {
				sr := t.getOrCreate(row.ID)
				last, has := sr.lastPersistedLocked()
				start := 0
				if has {
					start = sort.Search(len(row.Done), func(i int) bool { return row.Done[i].TS > last })
				}
				sr.done = appendCheckpointBatch(sr.done, row.Done[start:], t.spec.BlockSize)
				last, has = sr.lastPersistedLocked()
				sr.open = nil
				if row.Open != nil && (!has || row.Open.TS > last) {
					b := *row.Open
					sr.open = &b
				}
			}
		}
	}
	seen := make(map[string]bool, len(image.Raw))
	for _, row := range image.Raw {
		seen[row.ID] = true
		if len(row.TS) != len(row.Values) {
			return fmt.Errorf("checkpoint series %q length mismatch", row.ID)
		}
		sr := s.getOrCreate(row.ID)
		persistedLast := sr.last
		start := sort.Search(len(row.TS), func(i int) bool { return row.TS[i] > persistedLast })
		if start > len(row.Values) {
			start = len(row.Values)
		}
		sr.ts = appendCheckpointBatch(sr.ts, row.TS[start:], s.opt.BlockSize)
		sr.vals = appendCheckpointBatch(sr.vals, row.Values[start:], s.opt.BlockSize)
		visible := persistedLast
		if start < len(row.TS) && row.TS[len(row.TS)-1] > visible {
			visible = row.TS[len(row.TS)-1]
		}
		// A Last past every retained sample becomes the accept watermark.
		// Collections after restart are then dropped until wall clock catches
		// up, and last_entry stays on the sample written at process start.
		sr.last = visible
		sr.dirty = len(sr.ts) > 0
		// A full raw block may have reached disk after the last checkpoint. Replay
		// only that newer suffix into the restored rollups; persisted closed buckets
		// reject duplicates, while a saved partial bucket receives its missing tail.
		if persistedLast > row.Last {
			points, err := s.Query(row.ID, row.Last+1, persistedLast)
			if err != nil {
				return err
			}
			for _, p := range points {
				for _, t := range s.tiers {
					t.append(t.getOrCreate(row.ID), p.TS, p.Value)
				}
			}
		}
	}
	// A series created after the checkpoint may already have full raw blocks.
	for id, sr := range s.series {
		if seen[id] {
			continue
		}
		points, err := s.Query(id, 0, sr.last)
		if err != nil {
			return err
		}
		for _, p := range points {
			for _, t := range s.tiers {
				t.append(t.getOrCreate(id), p.TS, p.Value)
			}
		}
	}
	n := len(image.Raw)
	// Drop decoded buffers promptly; series data now lives in Store maps.
	image.Raw = nil
	image.Tiers = nil
	s.log.Info("tsdb: checkpoint loaded", "series", n)
	return nil
}

// appendCheckpointBatch copies a restored suffix in one allocation. Capacity
// follows appendCapped: at most the configured block while the result fits,
// and exactly len+added when an older checkpoint is larger than today's block.
func appendCheckpointBatch[T any](dst, src []T, limit int) []T {
	if len(src) == 0 {
		return dst
	}
	need := len(dst) + len(src)
	if need <= cap(dst) {
		return append(dst, src...)
	}
	capn := need
	if limit <= 0 || need <= limit {
		capn = growCapped(cap(dst), len(dst), len(src), limit)
	}
	out := make([]T, len(dst), capn)
	copy(out, dst)
	return append(out, src...)
}

func growCapped(capn, length, add, limit int) int {
	for i := 0; i < add; i++ {
		if length < capn {
			length++
			continue
		}
		next := capn * 2
		if next < 1 {
			next = 1
		}
		need := length + 1
		if next < need {
			next = need
		}
		if limit > 0 && next > limit {
			if need <= limit {
				next = limit
			} else {
				next = need
			}
		}
		capn = next
		length++
	}
	return capn
}

// Bound the uncompressed image too: never write a checkpoint this reader would
// refuse to decode. A failed checkpoint leaves its predecessor intact.
type checkpointLimitWriter struct {
	w    io.Writer
	left int64
}

func (w *checkpointLimitWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.left {
		return 0, fmt.Errorf("checkpoint exceeds %d bytes", maxCheckpointBytes)
	}
	n, err := w.w.Write(p)
	w.left -= int64(n)
	return n, err
}
