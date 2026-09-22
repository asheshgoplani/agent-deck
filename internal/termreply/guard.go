package termreply

import (
	"sync/atomic"
	"time"
)

var quarantineUntilUnixNano atomic.Int64
var quarantineWindow atomic.Uint64

// QuarantineFor drops terminal reply traffic until the later of the existing
// deadline or now+duration.
func QuarantineFor(duration time.Duration) {
	if duration <= 0 {
		return
	}
	now := time.Now()
	target := now.Add(duration).UnixNano()
	for {
		current := quarantineUntilUnixNano.Load()
		if current >= target {
			return
		}
		if quarantineUntilUnixNano.CompareAndSwap(current, target) {
			if current <= now.UnixNano() {
				quarantineWindow.Add(1)
			}
			return
		}
	}
}

// Window identifies the current quarantine period. It changes when a new
// period starts, even if no input was read between periods.
func Window() uint64 {
	return quarantineWindow.Load()
}

// Active reports whether terminal replies should currently be discarded.
func Active() bool {
	return time.Now().UnixNano() < quarantineUntilUnixNano.Load()
}

// Clear removes any active quarantine window. Intended for tests.
func Clear() {
	quarantineUntilUnixNano.Store(0)
}
