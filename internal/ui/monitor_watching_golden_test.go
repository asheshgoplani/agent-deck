package ui

import (
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
	"os"
	"strings"
	"testing"
)

func TestMonitorWatchingGolden(t *testing.T) {
	forceTrueColorProfile()
	h := &Home{width: 100}
	var frame strings.Builder
	for _, status := range []session.Status{session.StatusWaiting, session.StatusIdle} {
		inst := &session.Instance{ID: string(status), Title: "Observer", Tool: "claude", Status: status}
		snapshot := map[string]sessionRenderState{inst.ID: {status: status, substate: session.SubstateWatching, tool: "claude", title: "Observer"}}
		h.renderSessionItem(&frame, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true}, false, snapshot, h.width)
		work := tmux.BackgroundWork{Kind: tmux.BackgroundKindMonitor, Count: 1, Task: "Observe a fixture event", Source: "pane+transcript"}
		frame.WriteString(backgroundWorkLine(work) + "\n")
		icon, style := rowStatusGlyph(status, session.SubstateWatching, false)
		_, runningStyle := rowStatusGlyph(session.StatusRunning, session.SubstateRunning, false)
		if icon == "●" || style.GetForeground() == runningStyle.GetForeground() {
			t.Fatal("watcher has running light")
		}
	}
	got := stripAnsi(frame.String())
	path := "testdata/monitor_watching.txt"
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(path, []byte(got), 0644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("watching frame differs:\n%s", got)
	}
}
