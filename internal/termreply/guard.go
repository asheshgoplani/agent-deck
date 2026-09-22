package termreply

import (
	"sync/atomic"
	"time"
)

type quarantineState struct {
	until  int64
	window uint64
}

var quarantine atomic.Pointer[quarantineState]

// QuarantineFor drops terminal reply traffic until the later of the existing
// deadline or now+duration.
func QuarantineFor(duration time.Duration) {
	if duration <= 0 {
		return
	}
	for {
		now := time.Now()
		target := now.Add(duration).UnixNano()
		old := quarantine.Load()
		current := old
		if current == nil {
			current = &quarantineState{}
		}
		if current.until >= target {
			return
		}
		next := &quarantineState{until: target, window: current.window}
		if current.until <= now.UnixNano() {
			next.window++
		}
		if quarantine.CompareAndSwap(old, next) {
			return
		}
	}
}

// State reports whether a quarantine is active and identifies its period.
// Both values come from one immutable snapshot.
func State() (bool, uint64) {
	current := quarantine.Load()
	if current == nil {
		return false, 0
	}
	return time.Now().UnixNano() < current.until, current.window
}

// Active reports whether terminal replies should currently be discarded.
func Active() bool {
	active, _ := State()
	return active
}

// Clear removes any active quarantine window. Intended for tests.
func Clear() {
	for {
		current := quarantine.Load()
		if current == nil {
			return
		}
		if quarantine.CompareAndSwap(current, &quarantineState{window: current.window}) {
			return
		}
	}
}
