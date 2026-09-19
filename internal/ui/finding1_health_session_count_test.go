package ui

// Review round 2, finding 1, 2026-09-18: backgroundStatusUpdate's deferred
// health.RecordStatusPass/queueHealthWarning call still fed session_count the
// raw h.instances snapshot length, so health --json / doctor --json disagreed
// with status --json / list --json (which both derive from
// session.VisibleInstances) whenever an archived cross-harness source was
// still superseded by a "Restart with new session ID" target. Fix: the
// deferred closure now filters through session.VisibleInstances before
// recording, matching countByStatus/countSessionStatuses/renderGroupPreview.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/health"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestBackgroundStatusUpdate_HealthSessionCountExcludesSupersededSource(t *testing.T) {
	dir := t.TempDir()
	priorSocket := tmux.DefaultSocketName()
	// A dead/unreachable socket keeps IsServerAlive() false so the sweep
	// returns immediately after reconcileClaims, before touching any real
	// tmux state — the deferred health recording still fires either way.
	tmux.SetDefaultSocketName("finding1-health-session-count-fixture")
	t.Cleanup(func() { tmux.SetDefaultSocketName(priorSocket) })

	source, target := supersededSourceAndTarget()
	other := &session.Instance{ID: "plain", Title: "plain-session", Tool: "claude", Status: session.StatusIdle}
	h := newHomeForSnapshotTest()
	h.instances = []*session.Instance{source, target, other}

	healthDir := filepath.Join(dir, "health")
	stop := health.Start(healthDir, "test", filepath.Join(dir, "hooks"), "test")
	h.backgroundStatusUpdate()
	stop()

	report, err := health.Report(healthDir, time.Hour)
	if err != nil || len(report.Processes) != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	sample := report.Processes[0].Latest
	if sample.Sessions == nil {
		t.Fatalf("missing session_count sample: %+v", sample)
	}
	if *sample.Sessions != 2 {
		t.Fatalf("session_count = %d, want 2 (target + plain; source excluded as a superseded archive)", *sample.Sessions)
	}
}
