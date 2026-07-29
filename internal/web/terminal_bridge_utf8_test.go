//go:build !windows

package web

import "testing"

// TestTmuxAttachCommandForcesUTF8 guards the fix for tmux downgrading non-ASCII
// glyphs to '_' when the daemon runs without a UTF-8 locale (e.g. under launchd):
// the attach command must pass the global `-u` flag before the subcommand.
func TestTmuxAttachCommandForcesUTF8(t *testing.T) {
	cmd := tmuxAttachCommand("my-session", "my-socket")
	args := cmd.Args

	uIdx, attachIdx := -1, -1
	for i, a := range args {
		switch a {
		case "-u":
			uIdx = i
		case "attach-session":
			attachIdx = i
		}
	}

	if uIdx == -1 {
		t.Fatalf("expected global -u flag in attach command, got %v", args)
	}
	if attachIdx == -1 {
		t.Fatalf("expected attach-session subcommand, got %v", args)
	}
	// -u is a server/client flag and must precede the subcommand.
	if uIdx > attachIdx {
		t.Fatalf("-u must come before attach-session, got %v", args)
	}
}
