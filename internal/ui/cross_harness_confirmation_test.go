package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The modal is a time boundary: changes made after its disclosure was shown
// must require a new preview/confirmation rather than being accepted by the
// later asynchronous execution-time snapshot.
func TestConfirmDialogCrossHarnessRejectsChangedSource(t *testing.T) {
	started := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	inst := &session.Instance{
		ID: "source", Tool: "pi", Account: "", ProjectPath: "/workspace/repo", Title: "source", GroupPath: "group",
		Command: "pi", Status: session.StatusRunning, ClaudeSessionID: "claude-id", CodexSessionID: "codex-id", LastStartedAt: started,
	}
	dialog := NewConfirmDialog()
	dialog.ShowCrossHarnessTransfer(inst, "codex", "work", []string{"context is transferred"})
	if !dialog.CrossHarnessSourceMatches(inst) {
		t.Fatal("unchanged source no longer matches confirmation snapshot")
	}
	inst.CodexSessionID = "new-codex-id"
	if dialog.CrossHarnessSourceMatches(inst) {
		t.Fatal("modal accepted source whose native identity changed")
	}
}
