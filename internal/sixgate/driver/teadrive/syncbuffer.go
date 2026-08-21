package teadrive

import (
	"bytes"
	"sync"
)

// syncBuffer is the renderer's output sink.
//
// Bubble Tea writes frames from its own goroutine while the driver reads the
// byte count from another; an unsynchronised bytes.Buffer would be a data race,
// and a race detector finding in the gate harness is exactly the kind of noise
// that gets a gate switched off.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// Len reports how many bytes the renderer has written so far.
func (b *syncBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Len()
}
