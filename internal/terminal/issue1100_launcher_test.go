package terminal

import (
	"strings"
	"testing"
)

// Issue #1100 (by @ddorman-dn, follow-up to #1098):
//   (a) Shift+Enter on a REMOTE session did nothing — the dispatch path
//       only constructed local-tmux AttachRequests, so the launcher had
//       no SSH information to act on.
//   (b) For local sessions Shift+Enter opened a fresh iTerm WINDOW —
//       users expect a TAB (iTerm's natural UX). Tabbed iTerm users
//       in particular found a detached window jarring.
//
// These tests pin both fixes at the BuildAttachCommand layer so the
// shell command shape is locked in regardless of which platform
// implementation runs it.

// TestIssue1100_BuildAttachCommand_RemoteSession verifies that an
// AttachRequest with a populated Remote field renders an ssh invocation
// that runs `agent-deck session attach <id>` on the remote host. The
// shell command must be safe to paste into a freshly-spawned terminal
// — no unquoted spaces, no shell-metachar surprises in the host or
// session id.
func TestIssue1100_BuildAttachCommand_RemoteSession(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{
		Name: "abc-123",
		Remote: &RemoteAttach{
			Host:          "user@server.example",
			AgentDeckPath: "/usr/local/bin/agent-deck",
			Profile:       "work",
		},
	})

	for _, want := range []string{
		"ssh -tt",
		" -o ControlMaster=auto",
		" -o ControlPath='/tmp/agent-deck-ssh/%r@%h:%p'",
		" -o ControlPersist=600",
		"'user@server.example'",
		"'/usr/local/bin/agent-deck'",
		"-p 'work'",
		"session attach 'abc-123'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("remote BuildAttachCommand missing %q\nfull:\n%s", want, got)
		}
	}
}

// TestIssue1100_BuildAttachCommand_RemoteDefaultProfileOmitsFlag pins
// the contract that the default profile is implicit on the remote side
// — adding `-p default` would force agent-deck to look for a profile
// named "default" instead of falling through to its own defaults.
func TestIssue1100_BuildAttachCommand_RemoteDefaultProfileOmitsFlag(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{
		Name: "abc-123",
		Remote: &RemoteAttach{
			Host:    "u@h",
			Profile: "default",
		},
	})
	if strings.Contains(got, "-p 'default'") {
		t.Errorf("remote command should not pass -p 'default' explicitly:\n%s", got)
	}
	if !strings.Contains(got, "session attach 'abc-123'") {
		t.Errorf("remote command must still attach to the session id:\n%s", got)
	}
}

// TestIssue1100_BuildAttachCommand_RemoteEmptyAgentDeckPathFallsBack
// pins the default path lookup — leaving AgentDeckPath blank must
// still produce a runnable command (relies on PATH on the remote).
func TestIssue1100_BuildAttachCommand_RemoteEmptyAgentDeckPathFallsBack(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{
		Name:   "id",
		Remote: &RemoteAttach{Host: "u@h"},
	})
	if !strings.Contains(got, "'agent-deck'") {
		t.Errorf("expected bare 'agent-deck' fallback path:\n%s", got)
	}
}

// TestIssue1100_BuildAttachCommand_RemoteEmptyHostReturnsEmpty pins
// the safety guard — a Remote with no Host is unrunnable, and an
// empty string is the contract for "do not invoke the launcher".
func TestIssue1100_BuildAttachCommand_RemoteEmptyHostReturnsEmpty(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{
		Name:   "id",
		Remote: &RemoteAttach{Host: "   "},
	})
	if got != "" {
		t.Errorf("empty host should produce empty command, got %q", got)
	}
}

// TestIssue1100_BuildAttachCommand_LocalUnchanged pins the
// non-regression guarantee: a request without Remote still produces
// the local tmux invocation shipped in #1077. The follow-up fixes for
// #1098/#1100 must not alter the local command shape.
func TestIssue1100_BuildAttachCommand_LocalUnchanged(t *testing.T) {
	got := BuildAttachCommand(AttachRequest{Name: "myproj", SocketName: "agentdeck"})
	want := "tmux -L 'agentdeck' attach -t 'myproj'"
	if got != want {
		t.Errorf("local BuildAttachCommand changed:\n got=%q\nwant=%q", got, want)
	}
}
