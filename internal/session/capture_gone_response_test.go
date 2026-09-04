package session

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// goneTmuxSession returns a *tmux.Session pointed at an isolated socket with no
// running server, so any capture returns tmux.ErrCaptureGone.
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
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
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
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
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

func TestGetLastResponseBestEffort_UsesFinalCaptureError(t *testing.T) {
	for _, tc := range []struct {
		name, first, last string
		wantGone          bool
	}{
		{"gone then permission", "no server running on /tmp/test", "error connecting to /tmp/test (Permission denied)", false},
		{"permission then gone", "error connecting to /tmp/test (Permission denied)", "no server running on /tmp/test", true},
		{"other failure then gone", "unknown capture failure", "no server running on /tmp/test", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// Two attempts model a teardown/reconnect changing the socket between reads.
			script := "#!/bin/sh\nif [ ! -f \"$CAPTURE_STATE\" ]; then touch \"$CAPTURE_STATE\"; printf '%s\\n' \"$FIRST_ERROR\" >&2; else printf '%s\\n' \"$LAST_ERROR\" >&2; fi\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("CAPTURE_STATE", filepath.Join(dir, "captured"))
			t.Setenv("FIRST_ERROR", tc.first)
			t.Setenv("LAST_ERROR", tc.last)
			i := &Instance{Tool: "codex", tmuxSession: goneTmuxSession()}
			resp, err := i.GetLastResponseBestEffort()
			if tc.wantGone {
				if err != nil || resp == nil || resp.Content != "" {
					t.Fatalf("final gone capture = %+v, %v; want empty response", resp, err)
				}
			} else {
				if err == nil {
					t.Fatal("final permission error was swallowed")
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) || !strings.Contains(string(exitErr.Stderr), "Permission denied") {
					t.Fatalf("wrong final error: %v", err)
				}
			}
		})
	}
}
