package session

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// SetTmuxSessionForTest assigns the unexported tmuxSession field. Tests in
// other packages (notably internal/ui) need this to construct an Instance with
// a *tmux.Session attached without going through storage hydration or a real
// tmux server.
//
// Do not call from non-test code; production paths populate tmuxSession via
// Start (instance.go) or storage.LoadWithGroups (storage.go).
func (i *Instance) SetTmuxSessionForTest(s *tmux.Session) {
	i.tmuxSession = s
}

// MarkComposerStalledForTest seeds the in-memory dwell tracker so external
// render tests can exercise DisplaySubstate without waiting ten minutes or
// capturing a real pane.
func (i *Instance) MarkComposerStalledForTest(t testing.TB) {
	t.Helper()
	tracker := i.stallTracker()
	tracker.mu.Lock()
	origDraft, origSince := tracker.draft, tracker.since
	tracker.draft = "operator draft"
	tracker.since = stallClock().Add(-StallDwell - time.Second)
	tracker.mu.Unlock()
	t.Cleanup(func() {
		tracker.mu.Lock()
		tracker.draft, tracker.since = origDraft, origSince
		tracker.mu.Unlock()
	})
}
