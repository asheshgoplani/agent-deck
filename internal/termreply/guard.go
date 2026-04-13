package termreply

import (
	"sync/atomic"
	"time"
)

var quarantineUntilUnixNano atomic.Int64

// QuarantineFor drops terminal reply traffic until the later of the existing
// deadline or now+duration.
func QuarantineFor(duration time.Duration) {
	if duration <= 0 {
		return
	}
	target := time.Now().Add(duration).UnixNano()
	for {
		current := quarantineUntilUnixNano.Load()
		if current >= target {
			return
		}
		if quarantineUntilUnixNano.CompareAndSwap(current, target) {
			return
		}
	}
}

// Active reports whether terminal replies should currently be discarded.
func Active() bool {
	return time.Now().UnixNano() < quarantineUntilUnixNano.Load()
}

// Clear removes any active quarantine window. Intended for tests.
func Clear() {
	quarantineUntilUnixNano.Store(0)
}
