package ui

// Group-scoped keyboard navigation (v1.7.60).
//
// Adds an Alt-prefixed layer (Alt+j/k, Alt+1-9, Alt+g/G, Alt+/) that navigates
// within the cursor's current group only, as a non-breaking complement to the
// existing global j/k, 1-9, g/G, and / bindings. See handleMainKey for wiring.

import (
	"os"
	"path/filepath"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// navHintSentinelName is the file whose presence records that the v1.7.60
// group-navigation discoverability hint has already been shown to this user.
// Lives under the effective data directory so it persists across binary upgrades.
const navHintSentinelName = ".nav-hint-v1760-shown"

// navHintText is the one-shot discoverability message rendered in the
// maintenance banner slot on first TUI launch after upgrading to v1.7.60.
const navHintText = "Tip: Alt+j/k and Alt+1-9 navigate within the current group. Press ? for all keybindings."

// navHintSentinelPath returns the absolute path of the sentinel file, or "" if
// it cannot be resolved (no HOME, etc. — treat as "do not show").
func navHintSentinelPath() string {
	path, err := agentpaths.EffectiveDataPath(navHintSentinelName, navHintSentinelName)
	if err != nil {
		return ""
	}
	return path
}

// navHintAlreadyShown reports whether the sentinel exists. Also returns true
// when running under the test profile so tests do not write a sentinel file
// into the developer's real data directory.
func navHintAlreadyShown() bool {
	if os.Getenv("AGENTDECK_PROFILE") == "_test" {
		return true
	}
	path := navHintSentinelPath()
	if path == "" {
		return true // fail closed — no spurious prompts
	}
	_, err := os.Stat(path)
	return err == nil
}

// markNavHintShown creates the sentinel file so the hint is never shown again
// on this installation. Errors are swallowed: the worst case is the hint
// re-appears on next launch, not a broken TUI.
func markNavHintShown() {
	path := navHintSentinelPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	_ = os.WriteFile(path, []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644)
}

// currentGroupPath returns the group path that the cursor is "inside of":
//   - on a session item: the session's GroupPath
//   - on a group header: the group's Path
//   - on a window item: the parent session's GroupPath (by walking flatItems)
//   - anywhere else: ""
func (h *Home) currentGroupPath() string {
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		return ""
	}
	it := h.flatItems[h.cursor]
	switch it.Type {
	case session.ItemTypeGroup:
		return it.Path
	case session.ItemTypeSession:
		if it.Session != nil {
			return it.Session.GroupPath
		}
	case session.ItemTypeWindow:
		// Walk backwards to find the parent session.
		for i := h.cursor - 1; i >= 0; i-- {
			p := h.flatItems[i]
			if p.Type == session.ItemTypeSession && p.Session != nil && p.Session.ID == it.WindowSessionID {
				return p.Session.GroupPath
			}
		}
	}
	return ""
}

// nextSessionInCurrentGroup returns the flatItems index of the next session
// (after cursor) whose GroupPath matches the current group, or -1 if none.
// Does NOT wrap.
func (h *Home) nextSessionInCurrentGroup() int {
	groupPath := h.currentGroupPath()
	if groupPath == "" {
		return -1
	}
	for i := h.cursor + 1; i < len(h.flatItems); i++ {
		it := h.flatItems[i]
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.GroupPath == groupPath {
			return i
		}
	}
	return -1
}

// prevSessionInCurrentGroup returns the flatItems index of the previous
// session (before cursor) whose GroupPath matches the current group, or -1.
func (h *Home) prevSessionInCurrentGroup() int {
	groupPath := h.currentGroupPath()
	if groupPath == "" {
		return -1
	}
	for i := h.cursor - 1; i >= 0; i-- {
		it := h.flatItems[i]
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.GroupPath == groupPath {
			return i
		}
	}
	return -1
}

// sessionsInCurrentGroup returns flatItems indices of every session in the
// current group, in order.
func (h *Home) sessionsInCurrentGroup() []int {
	groupPath := h.currentGroupPath()
	if groupPath == "" {
		return nil
	}
	var out []int
	for i, it := range h.flatItems {
		if it.Type == session.ItemTypeSession && it.Session != nil && it.Session.GroupPath == groupPath {
			out = append(out, i)
		}
	}
	return out
}

// nthSessionInCurrentGroup returns the flatItems index of the 1-indexed Nth
// session in the current group, or -1 if n is out of range.
func (h *Home) nthSessionInCurrentGroup(n int) int {
	if n < 1 {
		return -1
	}
	sessions := h.sessionsInCurrentGroup()
	if n > len(sessions) {
		return -1
	}
	return sessions[n-1]
}

// firstSessionInCurrentGroup returns index of the first session or -1.
func (h *Home) firstSessionInCurrentGroup() int {
	return h.nthSessionInCurrentGroup(1)
}

// lastSessionInCurrentGroup returns index of the last session or -1.
func (h *Home) lastSessionInCurrentGroup() int {
	sessions := h.sessionsInCurrentGroup()
	if len(sessions) == 0 {
		return -1
	}
	return sessions[len(sessions)-1]
}

// jumpToIndex moves the cursor to the given flatItems index and mirrors the
// side effects used by the existing j/k handlers (viewport sync, preview
// debounce, activity mark). Returns the command that the handler should pass
// back from handleMainKey.
func (h *Home) jumpToIndex(idx int) {
	if idx < 0 || idx >= len(h.flatItems) {
		return
	}
	h.cursor = idx
	h.previewScrollOffset = 0
	h.syncViewport()
	h.markNavigationActivity()
}

// openInGroupSearch shows the local Search overlay scoped to the current
// group. If no current group can be determined, falls back to the normal
// unscoped search flow so the user never gets a silent no-op.
func (h *Home) openInGroupSearch() {
	groupPath := h.currentGroupPath()
	if groupPath == "" {
		h.search.Show()
		return
	}
	h.search.SetScopedGroup(groupPath)
	// Re-populate items through the scope filter.
	h.instancesMu.RLock()
	items := make([]*session.Instance, len(h.instances))
	copy(items, h.instances)
	h.instancesMu.RUnlock()
	h.search.SetItems(items)
	h.search.Show()
}
