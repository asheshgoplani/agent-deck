package ui

// Remote parity walk on g14 (2026-09-18), finding 1: the New Session dialog
// bound to a remote reported success and then dropped the session when the
// spawn died at once (the header flashed "1 idle" and reverted to 0). The
// CLI path leaves the remote's error record with the spawn_failure
// explainer; the dialog path must persist the same record and draw the row
// with the error light and the explainer in the preview.
//
// Golden frames (row + preview): after a failed create, after a successful
// create. Regenerate with:
//
//	UPDATE_GOLDEN=1 go test ./internal/ui/ -run TestRemoteCreateOutcome_Golden

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func remoteCreateOutcomeHome(t *testing.T) *Home {
	t.Helper()
	forceTrueColorProfile()
	withTempAgentDeckHome(t, `
[remotes.lab]
host = "alice@lab.example"
agent_deck_path = "/home/alice/.local/bin/agent-deck"
`)
	h := newTestHomeWithItems(120, 40, nil)
	h.remoteSessions = map[string][]session.RemoteSessionInfo{"lab": {}}
	return h
}

// renderRemoteCreateFrame draws the remote's group header, its session rows
// and the preview for the first session, the way the parity walk captured
// them.
func renderRemoteCreateFrame(t *testing.T, h *Home) string {
	t.Helper()
	var frame strings.Builder
	h.renderRemoteGroupItem(&frame, session.Item{Type: session.ItemTypeRemoteGroup, Path: "remotes/lab", RemoteName: "lab"}, false)
	h.remoteSessionsMu.RLock()
	rows := append([]session.RemoteSessionInfo(nil), h.remoteSessions["lab"]...)
	h.remoteSessionsMu.RUnlock()
	for i := range rows {
		item := session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "lab", RemoteSession: &rows[i], Level: 1, IsLastInGroup: i == len(rows)-1}
		h.renderRemoteSessionItem(&frame, item, i == 0)
	}
	frame.WriteString("--- preview ---\n")
	if len(rows) > 0 {
		item := session.Item{Type: session.ItemTypeRemoteSession, RemoteName: "lab", RemoteSession: &rows[0], Level: 1}
		frame.WriteString(h.renderRemotePreview(item, 70, 30))
	} else {
		frame.WriteString("(no rows)")
	}
	return frame.String()
}

func assertRemoteCreateGolden(t *testing.T, name, got string) {
	t.Helper()
	got = strings.TrimRight(stripAnsi(got), "\n") + "\n"
	path := filepath.Join("testdata", "remote_create", name+".txt")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (UPDATE_GOLDEN=1 to create)", path, err)
	}
	if string(want) != got {
		t.Fatalf("golden %s differs.\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

var parityOpts = session.RemoteAddOptions{Tool: "claude", Title: "parity-1789685743", Path: "/tmp", Group: "tmp"}

func parityCreateSpawnFailure() *session.RemoteSessionSpawnFailedError {
	return &session.RemoteSessionSpawnFailedError{
		ID: "parity-1", Title: "parity-1789685743",
		Reason:  "tool not found on PATH: claude (searched: /usr/bin:/bin)",
		Message: `failed to start session: tmux session "agentdeck_parity_1" is gone: tool not found on PATH: claude (searched: /usr/bin:/bin) (exited after 261ms)`,
		Record: &session.SpawnFailureRecord{
			InstanceID: "parity-1", Tool: "claude",
			Command:   "export AGENTDECK_INSTANCE_ID=parity-1; exec claude --session-id 1",
			Reason:    "tool not found on PATH: claude (searched: /usr/bin:/bin)",
			ElapsedMs: 261, Timestamp: 1789685743,
		},
	}
}

func TestRemoteCreateOutcome_FailedCreateKeepsErrorRow(t *testing.T) {
	msg, ok := remoteCreateOutcomeMsg("lab", parityOpts, "", parityCreateSpawnFailure()).(remoteSessionCreatedMsg)
	if !ok {
		t.Fatalf("outcome must be a remoteSessionCreatedMsg")
	}
	if msg.created == nil {
		t.Fatal("a spawn failure the remote recorded must still produce a row")
	}
	if msg.created.ID != "parity-1" || msg.created.Status != "error" || msg.created.RemoteName != "lab" || msg.created.Tool != "claude" {
		t.Fatalf("row = %+v, want the remote's id with status error", *msg.created)
	}
	if !strings.Contains(msg.preview, "session failed to start") || !strings.Contains(msg.preview, "not found on PATH: claude") {
		t.Fatalf("preview must carry the explainer, got %q", msg.preview)
	}
	if msg.err == nil || !strings.Contains(msg.err.Error(), "on lab:") {
		t.Fatalf("the footer must still report the failure, got %v", msg.err)
	}

	h := remoteCreateOutcomeHome(t)
	h.Update(msg)
	h.remoteSessionsMu.RLock()
	rows := h.remoteSessions["lab"]
	h.remoteSessionsMu.RUnlock()
	if len(rows) != 1 || rows[0].Status != "error" {
		t.Fatalf("rows = %+v, want one error row", rows)
	}
	frame := stripAnsi(renderRemoteCreateFrame(t, h))
	if !strings.Contains(frame, "✕ parity-1789685743") {
		t.Fatalf("row must carry the error light:\n%s", frame)
	}
	if !strings.Contains(frame, "not found on PATH: claude") {
		t.Fatalf("preview must show the explainer:\n%s", frame)
	}
	if strings.Contains(frame, "○") {
		t.Fatalf("no idle light for a session that never ran:\n%s", frame)
	}
}

// An older remote fails the start without the reason/spawn_failure shape.
// The create path rolled the session back, so nothing is drawn: the error
// is reported and the remote's list stays the truth (no phantom row).
func TestRemoteCreateOutcome_OldShapeDrawsNoRow(t *testing.T) {
	msg, ok := remoteCreateOutcomeMsg("lab", parityOpts, "", fmt.Errorf("failed to start remote session: %w", errors.New("ssh command failed: exit status 1"))).(remoteSessionCreatedMsg)
	if !ok {
		t.Fatalf("outcome must be a remoteSessionCreatedMsg")
	}
	if msg.created != nil || msg.preview != "" {
		t.Fatalf("old shape must not draw a row: %+v", msg)
	}
	if msg.err == nil {
		t.Fatal("the failure must be reported")
	}
	h := remoteCreateOutcomeHome(t)
	h.Update(msg)
	if got := len(h.remoteSessions["lab"]); got != 0 {
		t.Fatalf("phantom row drawn: %d", got)
	}
}

func TestRemoteCreateOutcome_Golden(t *testing.T) {
	t.Run("01-failed-create", func(t *testing.T) {
		h := remoteCreateOutcomeHome(t)
		h.Update(remoteCreateOutcomeMsg("lab", parityOpts, "", parityCreateSpawnFailure()))
		assertRemoteCreateGolden(t, "01-failed-create", renderRemoteCreateFrame(t, h))
	})
	t.Run("02-successful-create", func(t *testing.T) {
		h := remoteCreateOutcomeHome(t)
		h.Update(remoteCreateOutcomeMsg("lab", parityOpts, "parity-2", nil))
		assertRemoteCreateGolden(t, "02-successful-create", renderRemoteCreateFrame(t, h))
	})
}
