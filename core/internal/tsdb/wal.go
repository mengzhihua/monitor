package tsdb

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
)

// walFile is a per-sample log of points accepted after the last successful
// checkpoint. Recovery replays it and skips timestamps the checkpoint or a
// raw block already holds. The log is fsynced about once a second and again
// before a checkpoint truncates the prefix that image covers.
const walFile = "wal.v1"

const (
	walMagic    = "MWAL"
	walVersion  = 1
	walHeader   = 5
	walMaxIDLen = 4096
)

type walRec struct {
	id string
	ts int64
	v  float64
}

type walLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
	bw   *bufio.Writer
}

func openWAL(dir string) (*walLog, error) {
	path := filepath.Join(dir, walFile)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o640)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if st.Size() == 0 {
		if _, err := f.Write([]byte{walMagic[0], walMagic[1], walMagic[2], walMagic[3], walVersion}); err != nil {
			f.Close()
			return nil, err
		}
	} else if end, err := walValidEnd(f, st.Size()); err != nil {
		f.Close()
		return nil, err
	} else if end < st.Size() {
		// A torn tail must not stay in front of records appended on the next
		// run. Recovery stops at the first short record, so anything written
		// after the tear would never be replayed.
		if err := f.Truncate(end); err != nil {
			f.Close()
			return nil, err
		}
		if end == 0 {
			if _, err := f.Seek(0, io.SeekStart); err != nil {
				f.Close()
				return nil, err
			}
			if _, err := f.Write([]byte{walMagic[0], walMagic[1], walMagic[2], walMagic[3], walVersion}); err != nil {
				f.Close()
				return nil, err
			}
		}
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		f.Close()
		return nil, err
	}
	return &walLog{path: path, f: f, bw: bufio.NewWriterSize(f, 64*1024)}, nil
}

func (w *walLog) append(id string, ts int64, v float64) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendLocked(id, ts, v)
}

func (w *walLog) appendLocked(id string, ts int64, v float64) error {
	if len(id) == 0 || len(id) > walMaxIDLen {
		return fmt.Errorf("wal: bad series id")
	}
	n := 2 + len(id) + 16
	var stack [128]byte
	buf := stack[:0]
	if n > len(stack) {
		buf = make([]byte, n)
	} else {
		buf = stack[:n]
	}
	binary.LittleEndian.PutUint16(buf[0:2], uint16(len(id)))
	copy(buf[2:], id)
	off := 2 + len(id)
	binary.LittleEndian.PutUint64(buf[off:off+8], uint64(ts))
	binary.LittleEndian.PutUint64(buf[off+8:off+16], math.Float64bits(v))
	_, err := w.bw.Write(buf)
	return err
}

func (w *walLog) sync() error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.syncLocked()
}

func (w *walLog) syncLocked() error {
	if err := w.bw.Flush(); err != nil {
		return err
	}
	return w.f.Sync()
}

func (w *walLog) size() (int64, error) {
	if w == nil {
		return 0, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.syncLocked(); err != nil {
		return 0, err
	}
	st, err := w.f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// discardPrefix drops bytes before off, which must fall on a record boundary
// (or the header). Bytes written after the checkpoint snapshot stay in the log.
func (w *walLog) discardPrefix(off int64) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.syncLocked(); err != nil {
		return err
	}
	st, err := w.f.Stat()
	if err != nil {
		return err
	}
	if off < walHeader {
		off = walHeader
	}
	if off > st.Size() {
		off = st.Size()
	}
	tail := make([]byte, st.Size()-off)
	if len(tail) > 0 {
		if _, err := w.f.ReadAt(tail, off); err != nil {
			return err
		}
	}
	if err := w.f.Truncate(0); err != nil {
		return err
	}
	if _, err := w.f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if _, err := w.f.Write([]byte{walMagic[0], walMagic[1], walMagic[2], walMagic[3], walVersion}); err != nil {
		return err
	}
	if len(tail) > 0 {
		if _, err := w.f.Write(tail); err != nil {
			return err
		}
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	if _, err := w.f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	w.bw = bufio.NewWriterSize(w.f, 64*1024)
	return nil
}

func (w *walLog) close() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	err := w.syncLocked()
	if cErr := w.f.Close(); err == nil {
		err = cErr
	}
	w.f = nil
	return err
}

// walValidEnd is the offset just past the last complete record. A short or
// illegal record stops the scan; bytes at and after that offset are a torn tail.
func walValidEnd(f *os.File, size int64) (int64, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	r := bufio.NewReaderSize(f, 256*1024)
	hdr := make([]byte, walHeader)
	if _, err := io.ReadFull(r, hdr); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return 0, nil
		}
		return 0, err
	}
	if string(hdr[:4]) != walMagic || hdr[4] != walVersion {
		return 0, fmt.Errorf("wal: bad header")
	}
	off := int64(walHeader)
	var idBuf [256]byte
	var rest [16]byte
	for off+2 <= size {
		var lenBuf [2]byte
		if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
			return off, nil
		}
		n := int(binary.LittleEndian.Uint16(lenBuf[:]))
		if n == 0 || n > walMaxIDLen {
			return off, nil
		}
		rec := int64(2 + n + 16)
		if off+rec > size {
			return off, nil
		}
		copied, err := io.CopyBuffer(io.Discard, io.LimitReader(r, int64(n)), idBuf[:])
		if err != nil {
			return 0, err
		}
		if copied != int64(n) {
			return off, nil
		}
		if _, err := io.ReadFull(r, rest[:]); err != nil {
			return off, nil
		}
		off += rec
	}
	return off, nil
}

func readWAL(path string) ([]walRec, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	hdr := make([]byte, walHeader)
	if _, err := io.ReadFull(f, hdr); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, nil
		}
		return nil, err
	}
	if string(hdr[:4]) != walMagic || hdr[4] != walVersion {
		return nil, fmt.Errorf("wal: bad header")
	}
	var out []walRec
	for {
		var lenBuf [2]byte
		if _, err := io.ReadFull(f, lenBuf[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, err
		}
		n := int(binary.LittleEndian.Uint16(lenBuf[:]))
		if n == 0 || n > walMaxIDLen {
			return out, nil
		}
		id := make([]byte, n)
		if _, err := io.ReadFull(f, id); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, err
		}
		var rest [16]byte
		if _, err := io.ReadFull(f, rest[:]); err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return out, nil
			}
			return nil, err
		}
		out = append(out, walRec{
			id: string(id),
			ts: int64(binary.LittleEndian.Uint64(rest[:8])),
			v:  math.Float64frombits(binary.LittleEndian.Uint64(rest[8:])),
		})
	}
}

func (s *Store) walWrite(id string, ts int64, v float64) {
	if s.wal == nil {
		return
	}
	if err := s.wal.append(id, ts, v); err != nil {
		s.log.Warn("tsdb: wal append failed", "series", id, "err", err)
	}
}

func (s *Store) walSync() error {
	if s.wal == nil {
		return nil
	}
	return s.wal.sync()
}

func (s *Store) walSyncSize() (int64, error) {
	if s.wal == nil {
		return 0, nil
	}
	return s.wal.size()
}

func (s *Store) walDiscard(off int64) error {
	if s.wal == nil {
		return nil
	}
	return s.wal.discardPrefix(off)
}

func (s *Store) replayWAL() error {
	recs, err := readWAL(filepath.Join(s.opt.Dir, walFile))
	if err != nil {
		return err
	}
	for _, r := range recs {
		s.Append(r.id, r.ts, r.v)
	}
	if len(recs) > 0 {
		s.log.Info("tsdb: wal replayed", "records", len(recs))
	}
	return nil
}
