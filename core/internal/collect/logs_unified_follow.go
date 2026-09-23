package collect

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"sync"
	"time"
)

const unifiedRows = 2000
const unifiedBytes = 2 << 20

type unifiedRunner func(context.Context, func(), func(LogRow)) error

// The stream owns a bounded recent-history ring. Collection drains counters;
// reading the log table never drains or launches another system command.
type unifiedFollower struct {
	mu                  sync.Mutex
	rows                [unifiedRows]LogRow
	start, count, bytes int
	pending             float64
	severity            map[string]float64
	err                 error
	intervalErr         error
	cancel              context.CancelFunc
	done                chan struct{}
}

func newUnifiedFollower(run unifiedRunner, retry time.Duration) *unifiedFollower {
	ctx, cancel := context.WithCancel(context.Background())
	f := &unifiedFollower{cancel: cancel, done: make(chan struct{}), err: fmt.Errorf("unified log stream starting")}
	go func() {
		defer close(f.done)
		delay := retry
		for ctx.Err() == nil {
			started := time.Now()
			err := run(ctx, func() { f.setError(nil) }, f.append)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = io.EOF
			}
			f.setError(fmt.Errorf("unified log stream interrupted; retrying: %w", err))
			if time.Since(started) >= 30*time.Second {
				delay = retry
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			delay = min(30*time.Second, delay*2)
		}
	}()
	return f
}

func (f *unifiedFollower) stop() {
	f.cancel()
	<-f.done
}

func (f *unifiedFollower) setError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
	if err != nil {
		f.intervalErr = err
		// A failed interval is a gap, not zero activity or a later rate spike.
		f.pending = 0
		clear(f.severity)
	}
}

func logRowBytes(row LogRow) int {
	return len(row.Message) + len(row.Unit) + len(row.PID) + len(row.Priority) + len(row.Cursor) + len(row.Boot)
}

func (f *unifiedFollower) append(row LogRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pending++
	if f.severity == nil {
		f.severity = make(map[string]float64)
	}
	f.severity[normalizePri(row.Priority)]++
	size := logRowBytes(row)
	if size > unifiedBytes {
		return
	}
	for f.count > 0 && (f.count == unifiedRows || f.bytes+size > unifiedBytes) {
		f.bytes -= logRowBytes(f.rows[f.start])
		f.rows[f.start] = LogRow{}
		f.start = (f.start + 1) % unifiedRows
		f.count--
	}
	f.rows[(f.start+f.count)%unifiedRows] = row
	f.count++
	f.bytes += size
}

func (f *unifiedFollower) counters() (float64, map[string]float64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	err := f.err
	if err == nil {
		err = f.intervalErr
	}
	f.intervalErr = nil
	if err != nil {
		// A stream can fail and recover between collections. Keep that interval
		// missing even though on-demand reads can already use the recovered stream.
		f.pending = 0
		clear(f.severity)
		return 0, nil, err
	}
	n, severity := f.pending, f.severity
	f.pending, f.severity = 0, nil
	return n, severity, nil
}

func (f *unifiedFollower) recent(q LogQuery) ([]LogRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	limit := q.Limit
	if limit <= 0 {
		limit = 200
	}
	limit = min(limit, unifiedRows)
	rows := make([]LogRow, 0, min(limit, f.count))
	for i := f.count - 1; i >= 0 && len(rows) < limit; i-- {
		row := f.rows[(f.start+i)%unifiedRows]
		if acceptLog(q, row) {
			rows = append(rows, row)
		}
	}
	slices.Reverse(rows)
	return rows, nil
}

func (l *logsCollector) useUnifiedBuffer(q LogQuery) bool {
	return l.unified != nil && (q.Source == "" || q.Source == "macos" || q.Source == "unified") &&
		q.After == 0 && q.Before == 0 && q.Boot == "" && q.Cursor == ""
}

func unifiedStream(ctx context.Context, ready func(), emit func(LogRow)) error {
	return runUnifiedStream(ctx, exec.CommandContext(ctx, "/usr/bin/log", "stream", "--style", "ndjson", "--level", "default", "--type", "log"), ready, emit)
}

func runUnifiedStream(ctx context.Context, cmd *exec.Cmd, ready func(), emit func(LogRow)) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		_ = stdout.Close()
		return err
	}
	ready()
	readErr := readUnifiedStream(stdout, emit)
	if readErr != nil || ctx.Err() != nil {
		_ = cmd.Process.Kill()
	}
	// Finish consuming stdout before Wait can close its read end.
	waitErr := cmd.Wait()
	if readErr != nil {
		return readErr
	}
	return waitErr
}

func readUnifiedStream(reader io.Reader, emit func(LogRow)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		// log stream may print a human-readable filter header before NDJSON.
		if len(line) == 0 || bytes.HasPrefix(line, []byte("Filtering the log data")) {
			continue
		}
		var record unifiedLogRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return fmt.Errorf("invalid unified log record: %w", err)
		}
		row := record.row()
		if row.Message != "" || row.Unit != "" {
			emit(row)
		}
	}
	return scanner.Err()
}
