package plugins

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
)

// One root listing is shared by manifest and legacy discovery. A missing
// optional directory is empty; an unreadable one is not evidence of removals.
func readPluginDirectory(ctx context.Context, dir string) ([]os.DirEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read plugin directory: %w", err)
	}
	return entries, nil
}

func directoryDiagnostics(err error) []Diagnostic {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil
	}
	return []Diagnostic{{Path: ".", Error: "cannot read plugin directory"}}
}

// Discovery cooperates with cancellation between reads. It does not launch an
// unowned goroutine or claim to interrupt a filesystem call blocked in the OS.
// Wrapping the reader also prevents io.Copy from bypassing cancellation through
// a file's WriterTo fast path.
type discoveryReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r discoveryReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
