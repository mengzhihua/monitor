package plugins

import (
	"context"
	"io"
)

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
