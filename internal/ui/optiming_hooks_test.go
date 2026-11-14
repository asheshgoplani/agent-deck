package ui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// timedOps turns timing on into a temp dir for the test and returns a reader
// of the operation names recorded so far (ASK-106 hooks).
func timedOps(t *testing.T) func() []string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, true))
	return func() []string {
		entries, err := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{})
		if err != nil {
			return nil
		}
		ops := make([]string, 0, len(entries))
		for _, e := range entries {
			ops = append(ops, e.Op)
		}
		return ops
	}
}

func countOp(ops []string, op string) int {
	n := 0
	for _, o := range ops {
		if o == op {
			n++
		}
	}
	return n
}

func TestOptiming_PreviewFetchRecordsTUIPreview(t *testing.T) {
	ops := timedOps(t)
	home := NewHome()
	inst := &session.Instance{ID: "optiming-preview", Title: "p", Tool: "shell"}
	msg := home.fetchPreview(inst, inst.ID, -1)()
	if pm, ok := msg.(previewFetchedMsg); !ok || pm.started.IsZero() {
		t.Fatalf("fetchPreview msg = %#v, want a previewFetchedMsg stamped with its start", msg)
	}
	home.Update(msg)
	if got := countOp(ops(), "tui.preview"); got != 1 {
		t.Fatalf("tui.preview recorded %d times, want 1 (ops %v)", got, ops())
	}
}

func TestOptiming_PreviewOffRecordsNothing(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(optiming.SetForTest(dir, false))
	home := NewHome()
	inst := &session.Instance{ID: "optiming-preview-off", Title: "p", Tool: "shell"}
	msg := home.fetchPreview(inst, inst.ID, -1)()
	if pm := msg.(previewFetchedMsg); !pm.started.IsZero() {
		t.Fatal("fetchPreview stamped a start with timings off")
	}
	home.Update(msg)
	if entries, _ := optiming.ReadFile(filepath.Join(dir, optiming.FileName), time.Time{}); len(entries) != 0 {
		t.Fatalf("timings off but recorded %+v", entries)
	}
}

func TestOptiming_StatusPassRecordsTUIStatusPass(t *testing.T) {
	ops := timedOps(t)
	priorSocket := tmux.DefaultSocketName()
	// Unreachable socket: the sweep returns early, the deferred records still run.
	tmux.SetDefaultSocketName("optiming-status-pass-fixture")
	t.Cleanup(func() { tmux.SetDefaultSocketName(priorSocket) })

	h := newHomeForSnapshotTest()
	h.backgroundStatusUpdate()
	if got := countOp(ops(), "tui.status_pass"); got != 1 {
		t.Fatalf("tui.status_pass recorded %d times, want 1 (ops %v)", got, ops())
	}
}

func TestOptiming_FirstFrameRecordedOnceAfterFirstLoad(t *testing.T) {
	ops := timedOps(t)
	prev := processStart
	SetProcessStart(time.Now().Add(-50 * time.Millisecond))
	t.Cleanup(func() { processStart = prev })

	home := NewHome()
	home.width, home.height = 120, 40
	home.initialLoading = true
	_ = home.View() // splash frame: not the first list frame
	if got := countOp(ops(), "tui.first_frame"); got != 0 {
		t.Fatalf("tui.first_frame recorded before the first load (%v)", ops())
	}
	model, _ := home.Update(loadSessionsMsg{instances: []*session.Instance{}})
	h := model.(*Home)
	_ = h.View()
	_ = h.View()
	model, _ = h.Update(loadSessionsMsg{instances: []*session.Instance{}}) // a reload is not a first frame
	_ = model.(*Home).View()
	if got := countOp(ops(), "tui.first_frame"); got != 1 {
		t.Fatalf("tui.first_frame recorded %d times, want 1 (ops %v)", got, ops())
	}
}

func TestOptiming_DetachRecordedAtNextFrame(t *testing.T) {
	ops := timedOps(t)
	home := NewHome()
	home.width, home.height = 120, 40
	home.initialLoading = false
	model, _ := home.Update(statusUpdateMsg{detachedAt: time.Now().Add(-10 * time.Millisecond)})
	h := model.(*Home)
	_ = h.View()
	_ = h.View()
	if got := countOp(ops(), "tui.detach"); got != 1 {
		t.Fatalf("tui.detach recorded %d times, want 1 (ops %v)", got, ops())
	}
}
