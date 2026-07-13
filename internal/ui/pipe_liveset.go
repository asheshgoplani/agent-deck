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
// focused LRU plus the currently-attached sessions (pinned). Attached is a set,
// not a single name, because a session can be attached on any socket — the main
// TUI's session on the default socket and an attached session on an isolated
// socket must both stay pinned. Read by the PipeManager's wantPipe gate from
// multiple goroutines; written by the UI's reconciler — hence the mutex.
//
// All methods are nil-receiver safe so a Home built by an alternate/test
// constructor (which never initializes liveSet) can exercise Update without
// panicking.
type pipeLiveSet struct {
	mu       sync.Mutex
	capacity int
	lru      []string // most-recent first
	attached []string // pinned attached sessions (across all sockets); deduped, no ""
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
	if s == nil || name == "" {
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

// setAttached replaces the pinned attached set. Empty and duplicate names are
// dropped; calling with no names (or only "") clears the pin.
func (s *pipeLiveSet) setAttached(names ...string) {
	if s == nil {
		return
	}
	pinned := make([]string, 0, len(names))
	seen := make(map[string]bool, len(names))
	for _, n := range names {
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		pinned = append(pinned, n)
	}
	s.mu.Lock()
	s.attached = pinned
	s.mu.Unlock()
}

// want reports whether name should hold a live pipe.
func (s *pipeLiveSet) want(name string) bool {
	if s == nil || name == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, n := range s.attached {
		if n == name {
			return true
		}
	}
	for _, n := range s.lru {
		if n == name {
			return true
		}
	}
	return false
}

// members returns the deduped live set: attached sessions first, then the LRU.
// A nil receiver returns nil; a non-nil empty set returns a non-nil empty slice.
func (s *pipeLiveSet) members() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.lru)+len(s.attached))
	seen := make(map[string]bool, len(s.lru)+len(s.attached))
	for _, n := range s.attached {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	for _, n := range s.lru {
		if !seen[n] {
			out = append(out, n)
			seen[n] = true
		}
	}
	return out
}

// desiredLivePipes is the pure core of (*Home).reconcileLivePipes: it pins the
// focused session and the attached set into ls, then returns the subset of the
// live set that still has a live instance (per socketByName). Names without an
// instance — deleted or restarted sessions — are dropped so they are never
// retried; the caller resolves each survivor's socket via socketByName.
//
// Extracted from reconcileLivePipes so the focus/attach/eviction logic is
// unit-testable without a real tmux server or attached client.
func desiredLivePipes(ls *pipeLiveSet, focused string, attached []string, socketByName map[string]string) []string {
	ls.touch(focused)
	ls.setAttached(attached...)
	members := ls.members()
	desired := make([]string, 0, len(members))
	for _, name := range members {
		if _, ok := socketByName[name]; ok {
			desired = append(desired, name)
		}
	}
	return desired
}

// filterPipeCandidates drops sessions this instance does not own from the
// pipe-candidate map, keeping the focused session unconditionally (the live
// preview of a browsed session must work regardless of ownership). With
// claimPolling off — or with a nil owned set, which means "no successful
// reconcile yet / claim DB unavailable" and must fail open like isOwned does —
// it returns the map unchanged.
func filterPipeCandidates(socketByName map[string]string, nameToID map[string]string, owned map[string]bool, focused string, claimPolling bool) map[string]string {
	if !claimPolling || owned == nil {
		return socketByName
	}
	out := make(map[string]string, len(socketByName))
	for name, socket := range socketByName {
		if name == focused || owned[nameToID[name]] {
			out[name] = socket
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
