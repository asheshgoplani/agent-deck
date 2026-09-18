package ui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// mru_nav.go implements the alternate-session toggle and MRU walk (#2058):
// one key that swaps between the current session and the previous one (vim
// Ctrl+^ style), plus a key pair that steps back/forward through recently
// visited sessions. Both are backed by Home.mru (internal/session/mru.go),
// seeded at startup from the persisted last_accessed column and updated on
// every real attach (see markSessionVisited below).

// markSessionVisited records that inst is now the session the user is on: it
// stamps the persisted last-accessed time and records the visit in the MRU
// ring (#2058). Every path that moves focus onto a session — attachSession and
// the embedded-focus path in insert_mode.go — goes through here so the two
// never drift apart. Detaching deliberately does not: it re-stamps
// last_accessed with the more accurate detach time but is not a new visit.
//
// A walk-triggered attach already moved the ring's position onto inst.ID
// before this runs, so Visit is then a no-op that keeps the burst's ordering
// frozen; a fresh Enter/click attach (or the alternate toggle) advances the
// ring normally.
func (h *Home) markSessionVisited(inst *session.Instance) {
	inst.MarkAccessed()
	h.mru.Visit(inst.ID)
}

// attachSessionForMRU moves the cursor to id and attaches it, mirroring the
// "enter" key's dispatch for a plain session row (internal/ui/home.go). It
// deliberately skips enter's dead-pane/restart handling: an MRU target is a
// session the user already attached to successfully, and restarting it out
// from under a toggle/walk key would surprise more than help. found is false
// when id no longer refers to a live, attachable session, so callers can drop
// it from the ring and try the next entry.
func (h *Home) attachSessionForMRU(id string) (cmd tea.Cmd, found bool) {
	inst := h.getInstanceByID(id)
	if inst == nil || !h.sessionExistsForUI(inst) {
		return nil, false
	}
	h.moveCursorToSession(id)
	if h.embeddedLayout {
		if h.enterEmbeddedMode() {
			return h.startEmbeddedModeCmd(), true
		}
		return nil, true
	}
	return h.attachSession(inst), true
}

// handleAltSessionToggle swaps to the alternate session (the one current
// immediately before this one), the way vim's Ctrl+^ swaps buffers. Pressing
// it again swaps back. A stale alternate (the session was closed) is dropped
// and the toggle no-ops rather than falling further back through history —
// that is what the MRU walk keys are for.
func (h *Home) handleAltSessionToggle() (tea.Model, tea.Cmd) {
	id, ok := h.mru.Alternate()
	if !ok {
		return h, nil
	}
	if cmd, found := h.attachSessionForMRU(id); found {
		return h, cmd
	}
	h.mru.Remove(id)
	return h, nil
}

// handleMRUWalk steps back (forward=false) or forward (forward=true) through
// recently visited sessions. A stale entry (session closed since it was
// visited) is skipped and the walk continues in the same direction, so one
// closed session in the middle of a burst doesn't just stop the walk dead.
//
// Stale entries are dropped only once the walk has landed: removing one
// mid-walk shifts the ring position back over the entry the next step would
// have visited, which would silently skip it.
func (h *Home) handleMRUWalk(forward bool) (tea.Model, tea.Cmd) {
	var stale []string
	var cmd tea.Cmd
	for {
		var id string
		var ok bool
		if forward {
			id, ok = h.mru.WalkForward()
		} else {
			id, ok = h.mru.WalkBack()
		}
		if !ok {
			break
		}
		attachCmd, found := h.attachSessionForMRU(id)
		if found {
			cmd = attachCmd
			break
		}
		stale = append(stale, id)
	}
	for _, id := range stale {
		h.mru.Remove(id)
	}
	return h, cmd
}
