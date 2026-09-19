package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Status-light audit 2026-09-17: one row per substate the fix set introduced
// or extended (hook-lag; usage-limit on codex; interactive-menu on claude and
// codex), pinned so the light legend stays healthy — each row must keep the
// glyph of its coarse status, never borrow an error glyph or lose its light.
func TestStatusLightsSubstateRowsGolden(t *testing.T) {
	forceTrueColorProfile()
	h := &Home{width: 100}
	rows := []struct {
		tool     string
		status   session.Status
		substate session.Substate
	}{
		{"claude", session.StatusWaiting, session.SubstateHookLag},
		{"codex", session.StatusError, session.SubstateUsageLimit},
		{"claude", session.StatusWaiting, session.SubstateInteractiveMenu},
		{"codex", session.StatusWaiting, session.SubstateInteractiveMenu},
	}
	var frame strings.Builder
	for _, r := range rows {
		id := r.tool + "-" + string(r.substate)
		inst := &session.Instance{ID: id, Title: id, Tool: r.tool, Status: r.status}
		snapshot := map[string]sessionRenderState{id: {status: r.status, substate: r.substate, tool: r.tool}}
		h.renderSessionItem(&frame, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true}, false, snapshot, h.width)
	}
	got := stripAnsi(frame.String())
	const path = "testdata/status_lights_substates.txt"
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
	if string(want) != got {
		t.Fatalf("substate rows changed:\nwant:\n%s\ngot:\n%s", want, got)
	}
}
