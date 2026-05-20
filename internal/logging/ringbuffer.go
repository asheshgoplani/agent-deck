package logging

import (
	"os"
	"sync"
)

// RingBuffer is a thread-safe circular byte buffer.
// It implements io.Writer and silently overwrites old data when full.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	size int
	pos  int
	full bool
}

// NewRingBuffer creates a ring buffer with the given capacity in bytes.
func NewRingBuffer(size int) *RingBuffer {
	if size <= 0 {
		size = 10 * 1024 * 1024 // 10MB default
	}
	return &RingBuffer{
		buf:  make([]byte, size),
		size: size,
	}
}

// Write implements io.Writer. Data wraps around when the buffer is full.
func (rb *RingBuffer) Write(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	n := len(p)
	if n >= rb.size {
		// Data larger than buffer: keep only the last rb.size bytes
		copy(rb.buf, p[n-rb.size:])
		rb.pos = 0
		rb.full = true
		return n, nil
	}

	// How much fits before we wrap
	space := rb.size - rb.pos
	if n <= space {
		copy(rb.buf[rb.pos:], p)
		rb.pos += n
		if rb.pos == rb.size {
			rb.pos = 0
			rb.full = true
		}
	} else {
		// Split write: fill to end, then wrap
		copy(rb.buf[rb.pos:], p[:space])
		copy(rb.buf, p[space:])
		rb.pos = n - space
		rb.full = true
	}

	return n, nil
}

// Bytes returns the buffer contents in chronological order.
func (rb *RingBuffer) Bytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if !rb.full {
		out := make([]byte, rb.pos)
		copy(out, rb.buf[:rb.pos])
		return out
	}

	// Buffer has wrapped: [pos..end] + [0..pos]
	out := make([]byte, rb.size)
	copy(out, rb.buf[rb.pos:])
	copy(out[rb.size-rb.pos:], rb.buf[:rb.pos])
	return out
}

// DumpToFile writes the ring buffer contents to a file in chronological order.
func (rb *RingBuffer) DumpToFile(path string) error {
	data := rb.Bytes()
	// #nosec G306 -- diagnostic log dump intended to be readable for incident
	// triage; contains no secrets. 0o644 matches the rest of the log surface.
	return os.WriteFile(path, data, 0o644)
}
