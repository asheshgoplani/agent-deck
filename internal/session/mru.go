package session

import "sort"

// DefaultMRUCapacity bounds the in-memory MRU ring (#2058). A deck with
// dozens of sessions only ever bounces between a handful at a time; capping
// the ring keeps Walk/Alternate cheap and keeps a single runaway burst of
// visits from growing memory unboundedly.
const DefaultMRUCapacity = 50

// MRUHistory is the in-memory most-recently-used ring backing the
// alternate-session toggle and the MRU walk (#2058). It tracks, in visit
// order, which sessions were selected: entries[pos] is "where you are now",
// entries[:pos] is the back-stack, entries[pos+1:] is the redo/forward stack.
//
// Visit records a genuine new selection (e.g. Enter/attach): it truncates any
// forward (redo) stack, dedups the target so it appears once, and remembers
// the session you were on immediately before as the "alternate" — the same
// slot vim's Ctrl+^ swaps with. WalkBack/WalkForward move the position
// without mutating entries, so a burst of walk key-presses holds a stable
// order instead of reshuffling after every hop (mirrors the existing
// Ctrl+S/Ctrl+A session switcher's frozen-list behavior).
//
// Not safe for concurrent use without external synchronization; callers own a
// single instance per TUI process (see internal/ui Home.mru).
type MRUHistory struct {
	entries []string
	pos     int // index into entries of the current session, or -1 when empty
	altID   string
	cap     int
}

// NewMRUHistory creates an empty MRU ring bounded to capacity entries. A
// non-positive capacity falls back to DefaultMRUCapacity.
func NewMRUHistory(capacity int) *MRUHistory {
	if capacity <= 0 {
		capacity = DefaultMRUCapacity
	}
	return &MRUHistory{pos: -1, cap: capacity}
}

// NewMRUHistoryFromInstances seeds the ring from each instance's persisted
// LastAccessedAt, oldest first, so a freshly started TUI's alternate toggle
// and MRU walk pick up where the last session left off (#2058) instead of
// starting empty. Instances with a zero LastAccessedAt (never attached) are
// skipped. The current position lands on the most-recently-accessed
// instance.
func NewMRUHistoryFromInstances(instances []*Instance, capacity int) *MRUHistory {
	m := NewMRUHistory(capacity)

	seeded := make([]*Instance, 0, len(instances))
	for _, inst := range instances {
		if inst == nil || inst.LastAccessedAt.IsZero() {
			continue
		}
		seeded = append(seeded, inst)
	}
	sort.SliceStable(seeded, func(i, j int) bool {
		return seeded[i].LastAccessedAt.Before(seeded[j].LastAccessedAt)
	})

	for _, inst := range seeded {
		m.entries = append(m.entries, inst.ID)
	}
	m.pos = len(m.entries) - 1
	m.trimToCapacity()
	return m
}

// trimToCapacity drops the oldest entries once the ring exceeds its bound,
// shifting pos to match. Both callers leave pos on the newest entry, so pos
// stays in range: it can only shift down to cap-1.
func (m *MRUHistory) trimToCapacity() {
	if len(m.entries) <= m.cap {
		return
	}
	drop := len(m.entries) - m.cap
	m.entries = m.entries[drop:]
	m.pos -= drop
}

// currentID returns the session at pos, or "" when the ring is empty.
func (m *MRUHistory) currentID() string {
	if m == nil || m.pos < 0 || m.pos >= len(m.entries) {
		return ""
	}
	return m.entries[m.pos]
}

// removeID drops id's occurrence from entries (there is at most one, by the
// dedup invariant Visit maintains) and shifts pos so it keeps pointing at the
// same logical entry. Removing the entry at pos itself lands pos on the
// previous entry, or on the new first entry when there is no previous one.
func (m *MRUHistory) removeID(id string) {
	idx := -1
	for i, e := range m.entries {
		if e == id {
			idx = i
			break
		}
	}
	if idx == -1 {
		return
	}
	m.entries = append(m.entries[:idx], m.entries[idx+1:]...)
	if idx <= m.pos {
		m.pos--
	}
	if m.pos < 0 && len(m.entries) > 0 {
		m.pos = 0
	}
}

// Visit records id as the newly current session. Re-visiting the session
// already at pos (in particular, confirming a WalkBack/WalkForward step) is a
// no-op: it neither disturbs the alternate pointer nor reshuffles the ring,
// which is what keeps a walk burst's ordering stable. A genuinely new visit
// remembers the prior current session as the alternate (vim Ctrl+^ semantics),
// discards any forward/redo stack past pos, dedups id to a single occurrence,
// appends it as the new current entry, and trims to capacity.
func (m *MRUHistory) Visit(id string) {
	if m == nil || id == "" {
		return
	}
	if m.currentID() == id {
		return
	}
	m.altID = m.currentID()
	m.removeID(id)
	if m.pos+1 < len(m.entries) {
		m.entries = m.entries[:m.pos+1]
	}
	m.entries = append(m.entries, id)
	m.pos = len(m.entries) - 1
	m.trimToCapacity()
}

// Remove drops id from the ring entirely (e.g. the session was closed), and
// clears the alternate pointer if it referenced id. Safe to call whether or
// not id is present.
func (m *MRUHistory) Remove(id string) {
	if m == nil || id == "" {
		return
	}
	m.removeID(id)
	if m.altID == id {
		m.altID = ""
	}
}

// WalkBack steps to the previous (older) session in the ring without
// recording a new visit. It returns ok=false when already at the oldest
// entry (or the ring is empty).
func (m *MRUHistory) WalkBack() (id string, ok bool) {
	if m == nil || m.pos <= 0 {
		return "", false
	}
	m.pos--
	return m.entries[m.pos], true
}

// WalkForward steps to the next (newer) session in the ring without
// recording a new visit. It returns ok=false when already at the newest
// entry (or the ring is empty).
func (m *MRUHistory) WalkForward() (id string, ok bool) {
	if m == nil || m.pos < 0 || m.pos+1 >= len(m.entries) {
		return "", false
	}
	m.pos++
	return m.entries[m.pos], true
}

// Alternate returns the session that was current immediately before the
// current one — the same slot vim's Ctrl+^ swaps with. ok is false when no
// alternate has been recorded yet.
func (m *MRUHistory) Alternate() (id string, ok bool) {
	if m == nil || m.altID == "" {
		return "", false
	}
	return m.altID, true
}

// Snapshot returns a copy of the ring's entries, oldest-first, for tests and
// inspection.
func (m *MRUHistory) Snapshot() []string {
	if m == nil {
		return nil
	}
	return append([]string(nil), m.entries...)
}
