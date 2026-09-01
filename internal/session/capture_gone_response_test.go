package session

import (
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// goneTmuxSession returns a *tmux.Session pointed at an isolated socket with no
// running server, so any capture returns tmux.ErrCaptureGone — the reproducible
// stand-in for the post-completion/resume vanish from plan 001.
func goneTmuxSession() *tmux.Session {
	return &tmux.Session{
		Name:       "agentdeck_gone_response_probe",
		SocketName: fmt.Sprintf("adeck-gone-resp-%d", os.Getpid()),
	}
}

// TestGetTerminalLastResponse_GonePropagatesSentinel verifies the read path no
// longer wraps a vanished pane as the opaque "failed to capture terminal
// output: ... exit status 1"; it propagates tmux.ErrCaptureGone so callers can
// degrade cleanly.
func TestGetTerminalLastResponse_GonePropagatesSentinel(t *testing.T) {
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("shell unavailable")
	}
	i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
	_, err := i.getTerminalLastResponse()
	if !errors.Is(err, tmux.ErrCaptureGone) {
		t.Fatalf("getTerminalLastResponse = %v, want tmux.ErrCaptureGone", err)
	}
}

// TestGetLastResponseBestEffort_GoneReturnsEmpty verifies the conductor-facing
// best-effort read degrades a vanished session to an empty response (no error)
// for a non-Claude/Gemini tool — the exact case that was surfacing the scary
// error string on codex children.
func TestGetLastResponseBestEffort_GoneReturnsEmpty(t *testing.T) {
	i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
	resp, err := i.GetLastResponseBestEffort()
	if err != nil {
		t.Fatalf("GetLastResponseBestEffort returned error for vanished session: %v", err)
	}
	if resp == nil {
		t.Fatal("expected a non-nil empty response, got nil")
	}
	if resp.Content != "" {
		t.Fatalf("expected empty content for vanished session, got %q", resp.Content)
	}
}
