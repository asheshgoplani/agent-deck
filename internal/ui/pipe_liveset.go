package ui

import (
	"sync"
	"time"
)

const (
	// livePipeLRUCapacity is how many recently-focused sessions keep a live
	// control pipe (beyond the attached session). Small by design: the point
	// is to bound pipes-per-instance so N instances stay cheap.
	livePipeLRUCapacity = 3

	// livePipeReconcileInterval is how often the reconciler samples the
	// focused/attached session and syncs pipes. This interval doubles as the
	// focus debounce: scrolling faster than this never connects intermediate
	// sessions.
	livePipeReconcileInterval = 500 * time.Millisecond
)

// pipeLiveSet is the thread-safe set of tmux session names that should hold a
// live control pipe for this agent-deck instance: a bounded most-recently-
// focused LRU plus the currently-attached session (pinned). Read by the
// PipeManager's wantPipe gate from multiple goroutines; written by the UI's
// reconciler — hence the mutex.
type pipeLiveSet struct {
	mu       sync.Mutex
	capacity int
	lru      []string // most-recent first
	attached string   // pinned; "" when nothing attached
}

func newPipeLiveSet(capacity int) *pipeLiveSet {
	if capacity < 1 {
		capacity = 1
	}
	return &pipeLiveSet{capacity: capacity}
}

// touch promotes name to the front of the LRU, deduping, and trims to capacity.
// Empty name is a no-op (cursor on a group header, nothing attached, etc).
func (s *pipeLiveSet) touch(name string) {
	if name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Filter in-place: out shares lru's backing array. Safe because we skip at
	// most one element (the existing copy of name), so the write index never
	// overtakes the read index.
	out := s.lru[:0]
	for _, n := range s.lru {
		if n != name {
			out = append(out, n)
		}
	}
	s.lru = append([]string{name}, out...)
	if len(s.lru) > s.capacity {
		s.lru = s.lru[:s.capacity]
	}
}

// setAttached pins the attached session ("" clears the pin).
func (s *pipeLiveSet) setAttached(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attached = name
}

// want reports whether name should hold a live pipe.
func (s *pipeLiveSet) want(name string) bool {
	if name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == s.attached {
		return true
	}
	for _, n := range s.lru {
		if n == name {
			return true
		}
	}
	return false
}

// members returns the deduped live set: attached (if any) followed by the LRU.
func (s *pipeLiveSet) members() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.lru)+1)
	seen := map[string]bool{}
	if s.attached != "" {
		out = append(out, s.attached)
		seen[s.attached] = true
	}
	for _, n := range s.lru {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	return out
}

// pipeConnector is the slice of PipeManager that reconcilePipes needs. Defined
// here so the diff logic is unit-testable with a fake. *tmux.PipeManager
// satisfies it.
type pipeConnector interface {
	IsConnected(name string) bool
	Connect(name, socket string) error
	Disconnect(name string)
	ConnectedSessions() []string
}

// reconcilePipes makes pm's connected pipes match desired: it connects each
// desired session not already connected, and disconnects each connected session
// no longer desired. socketOf maps a session name to its tmux -L socket.
func reconcilePipes(pm pipeConnector, desired []string, socketOf func(string) string) {
	desiredSet := make(map[string]bool, len(desired))
	for _, n := range desired {
		if n != "" {
			desiredSet[n] = true
		}
	}
	for n := range desiredSet {
		if !pm.IsConnected(n) {
			_ = pm.Connect(n, socketOf(n))
		}
	}
	for _, n := range pm.ConnectedSessions() {
		if !desiredSet[n] {
			pm.Disconnect(n)
		}
	}
}
