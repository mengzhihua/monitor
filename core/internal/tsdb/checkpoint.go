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
)

// A checkpoint is one atomic, checksummed image of the unfinished blocks in
// every tier. Full blocks retain the existing on-disk format. This avoids a
// separate fsync for every series on every checkpoint (including open buckets).
const checkpointFile = "checkpoint.v1"
const maxCheckpointBytes = 512 << 20

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

func (s *Store) writeCheckpoint(image checkpointImage) error {
	path := filepath.Join(s.opt.Dir, checkpointFile)
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { f.Close(); os.Remove(tmp) }()
	hash := sha256.New()
	writer := io.MultiWriter(f, hash)
	if _, err = writer.Write([]byte("MONCP001")); err != nil {
		return err
	}
	z := gzip.NewWriter(writer)
	if err = gob.NewEncoder(&checkpointLimitWriter{w: z, left: maxCheckpointBytes}).Encode(image); err != nil {
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
	hash := sha256.New()
	if _, err = io.CopyN(hash, f, stat.Size()-32); err != nil {
		return err
	}
	var checksum [32]byte
	if _, err = io.ReadFull(f, checksum[:]); err != nil {
		return err
	}
	if !bytes.Equal(checksum[:], hash.Sum(nil)) {
		return fmt.Errorf("checkpoint checksum mismatch: %s", path)
	}
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	var magic [8]byte
	if _, err = io.ReadFull(f, magic[:]); err != nil {
		return err
	}
	if string(magic[:]) != "MONCP001" {
		return fmt.Errorf("unsupported checkpoint format")
	}
	z, err := gzip.NewReader(io.LimitReader(f, stat.Size()-40))
	if err != nil {
		return err
	}
	defer z.Close()
	var image checkpointImage
	if err = gob.NewDecoder(io.LimitReader(z, maxCheckpointBytes)).Decode(&image); err != nil {
		return fmt.Errorf("decode checkpoint: %w", err)
	}
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
				for _, b := range row.Done {
					if !has || b.TS > last {
						sr.done = append(sr.done, b)
					}
				}
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
		visible := persistedLast
		for i, ts := range row.TS {
			if ts > persistedLast {
				sr.ts = append(sr.ts, ts)
				sr.vals = append(sr.vals, row.Values[i])
				if ts > visible {
					visible = ts
				}
			}
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
	s.log.Info("tsdb: checkpoint loaded", "series", len(image.Raw))
	return nil
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
