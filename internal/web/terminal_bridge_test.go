package web

import (
	"reflect"
	"strings"
	"testing"
)

// TestTmuxAttachCommand_NoIgnoreSize: the web's tmux attach must NOT pass
// `-f ignore-size`. Earlier the bridge combined ignore-size with a manual
// `tmux resize-window` call (since reverted) which had the side effect of
// flipping the session option to `window-size=manual`, dragging the window
// for ALL attached clients (Ghostty, iTerm) — the dots-in-window bug.
// With ignore-size removed and resize-window dropped, the web client
// participates in tmux's `window-size=largest` arbitration set at
// Session.Start (internal/tmux/tmux.go), so every client sees content sized
// to the biggest viewer.
func TestTmuxAttachCommand_NoIgnoreSize(t *testing.T) {
	t.Setenv("TMUX", "")

	cmd := tmuxAttachCommand("sess-1", "")

	wantArgs := []string{"tmux", "attach-session", "-t", "sess-1"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected args: got %v want %v", cmd.Args, wantArgs)
	}
}

func TestTmuxAttachCommandUsesSocketFromTMUXEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test.sock,12345,0")

	cmd := tmuxAttachCommand("sess-2", "")

	wantArgs := []string{"tmux", "-S", "/tmp/tmux-test.sock", "attach-session", "-t", "sess-2"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("unexpected args with TMUX env: got %v want %v", cmd.Args, wantArgs)
	}

	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "TMUX=") {
			t.Fatalf("TMUX variable should be removed from command env, got %q", env)
		}
	}
}

// TestTmuxAttachCommand_SocketNameOverridesEnv: when the per-session socket
// name is explicit (MenuSession.TmuxSocketName, threaded through from
// Instance at v1.7.50), the legacy $TMUX env path is ignored and the web
// bridge targets the isolated agent-deck socket instead. This is the
// phase-1 guarantee for issue #687 users running `agent-deck web` inside
// their own tmux pane.
func TestTmuxAttachCommand_SocketNameOverridesEnv(t *testing.T) {
	// $TMUX is set to the user's default tmux — must be ignored because the
	// caller supplied an explicit socket name.
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")

	cmd := tmuxAttachCommand("agentdeck-foo", "agent-deck")

	wantArgs := []string{"tmux", "-L", "agent-deck", "attach-session", "-t", "agentdeck-foo"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("socket name must take precedence over $TMUX env\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}

	// TMUX must be stripped so tmux-in-tmux refuse-to-nest guards don't trip.
	for _, env := range cmd.Env {
		if strings.HasPrefix(env, "TMUX=") {
			t.Fatalf("TMUX variable should be removed when socket name is set, got %q", env)
		}
	}
}

// TestTmuxAttachCommand_WhitespaceSocketNameFallsBackToEnv: the same
// defensive trim we use elsewhere. A typo like `socket_name = "   "` in
// config must not send the web bridge to a phantom server named "   " —
// treat as empty and use the legacy env path.
func TestTmuxAttachCommand_WhitespaceSocketNameFallsBackToEnv(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-test.sock,12345,0")

	cmd := tmuxAttachCommand("sess-3", "   \t")

	wantArgs := []string{"tmux", "-S", "/tmp/tmux-test.sock", "attach-session", "-t", "sess-3"}
	if !reflect.DeepEqual(cmd.Args, wantArgs) {
		t.Fatalf("whitespace-only socket name must fall through to legacy TMUX env\n got:  %v\n want: %v", cmd.Args, wantArgs)
	}
}
