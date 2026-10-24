package ui

// The header pill must leave out every archived session, not only archived
// cross-harness sources that were superseded. `agent-deck status` and the web
// menu already drop archived sessions; the TUI header has to agree, or an
// archived worker left in "waiting" shows as waiting forever (archived rows
// are skipped by the background status sweep, so the status never clears).

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestHeaderCounter_ExcludesArchivedWaitingSession(t *testing.T) {
	home := NewHome()
	home.width, home.height = 100, 30

	live := &session.Instance{ID: "live", Title: "live-worker", Tool: "claude", Status: session.StatusRunning}
	archived := &session.Instance{
		ID: "archived", Title: "done-worker", Tool: "claude",
		Status: session.StatusWaiting, ArchivedAt: time.Now(),
	}
	home.instances = []*session.Instance{live, archived}
	home.refreshSessionRenderSnapshot(nil)
	home.cachedStatusCounts.valid.Store(false)

	running, waiting, idle, stopped, errored := home.countSessionStatuses()
	if running != 1 || waiting != 0 || idle != 0 || stopped != 0 || errored != 0 {
		t.Fatalf("counts = running=%d waiting=%d idle=%d stopped=%d errored=%d, want running=1 waiting=0 (archived excluded)",
			running, waiting, idle, stopped, errored)
	}
}
