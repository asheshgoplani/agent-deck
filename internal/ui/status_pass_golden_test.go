package ui

import (
	"os"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The shared status pass changes polling only. Pin the Codex row frame so its
// running, waiting, and unavailable states retain the existing presentation.
func TestStatusPassCodexRowsGolden(t *testing.T) {
	forceTrueColorProfile()
	h := &Home{width: 100}
	var frame strings.Builder
	for _, status := range []session.Status{session.StatusRunning, session.StatusWaiting, session.StatusError} {
		inst := &session.Instance{ID: string(status), Title: "codex-" + string(status), Tool: "codex", Status: status}
		snapshot := map[string]sessionRenderState{inst.ID: {status: status, tool: "codex"}}
		h.renderSessionItem(&frame, session.Item{Type: session.ItemTypeSession, Session: inst, Level: 1, IsLastInGroup: true}, false, snapshot, h.width)
	}
	got := stripAnsi(frame.String())
	const path = "testdata/status_pass_codex.txt"
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
		t.Fatalf("Codex rows changed:\nwant:\n%s\ngot:\n%s", want, got)
	}
}
