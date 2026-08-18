package tmux

import (
	"sync"
	"time"
)

// pipeEventSampleWindow is how long one (event, session) pair stays suppressed
// after it has been logged once.
const pipeEventSampleWindow = 60 * time.Second

// pipeEventSamplerCapacity bounds the sampler's key space. Keys are
// (event, session) pairs, so the natural size is a few per live session; the
// cap only exists so a long-running TUI that churns through thousands of
// session names cannot grow the map without bound.
const pipeEventSamplerCapacity = 2048

// pipeEventSampler rate-limits the per-reconnect-cycle pipe DEBUG events.
//
// A single session whose instance record outlived its tmux session used to
// write pipe_closed / pipe_reader_exited / pipe_connect_retry / pipe_scanner_error
// at roughly 63 lines/sec — about 90% of a 32MB, 45-minute debug log, enough
// filesystem churn to show up as fseventsd burning CPU. Fixing the reconnect
// storm removes most of that volume; this caps what any future pathology can
// write.
//
// It samples rather than drops: the FIRST occurrence of every (event, session)
// pair is always logged, and the first occurrence after the window closes is
// logged too, carrying the count of what was suppressed in between. The signal
// that made the storm diagnosable — which event, which session, at what rate —
// survives; only the repetition is gone.
type pipeEventSampler struct {
	mu     sync.Mutex
	now    func() time.Time
	states map[string]*pipeEventState
}

type pipeEventState struct {
	windowStart time.Time
	suppressed  int
}

func newPipeEventSampler(now func() time.Time) *pipeEventSampler {
	if now == nil {
		now = time.Now
	}
	return &pipeEventSampler{now: now, states: map[string]*pipeEventState{}}
}

// pipeEvents is the process-wide sampler used by the ControlPipe log sites.
var pipeEvents = newPipeEventSampler(nil)

// sample reports whether this occurrence of event for session should be logged,
// and how many occurrences were suppressed since the last logged one. A true
// return with suppressed > 0 is the "N suppressed in the last window" summary.
func (s *pipeEventSampler) sample(event, session string) (emit bool, suppressed int) {
	if s == nil {
		return true, 0
	}
	key := event + "\x00" + session

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	st, ok := s.states[key]
	if !ok {
		s.pruneLocked(now)
		s.states[key] = &pipeEventState{windowStart: now}
		return true, 0
	}
	if now.Sub(st.windowStart) < pipeEventSampleWindow {
		st.suppressed++
		return false, 0
	}
	dropped := st.suppressed
	st.windowStart = now
	st.suppressed = 0
	return true, dropped
}

// pruneLocked evicts keys whose window has long closed, but only when the map
// has actually grown past its cap — the common case adds a key and returns.
func (s *pipeEventSampler) pruneLocked(now time.Time) {
	if len(s.states) < pipeEventSamplerCapacity {
		return
	}
	for key, st := range s.states {
		if now.Sub(st.windowStart) >= pipeEventSampleWindow {
			delete(s.states, key)
		}
	}
	// Still full of live windows: drop everything rather than grow unbounded.
	// Worst case one extra line per event/session pair, which is the sampler's
	// whole budget anyway.
	if len(s.states) >= pipeEventSamplerCapacity {
		s.states = map[string]*pipeEventState{}
	}
}

// ResetPipeEventSamplerForTest clears the process-wide sampler so a test's log
// assertions are not suppressed by an earlier test's events.
func ResetPipeEventSamplerForTest() {
	pipeEvents = newPipeEventSampler(nil)
}
