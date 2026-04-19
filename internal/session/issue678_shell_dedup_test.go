package session

// Issue #678 — shell / placeholder sessions must also be deduplicated
// across tmux. The #596/#599 guard keyed on CLAUDE_SESSION_ID (and
// later GEMINI_/OPENCODE_/CODEX_SESSION_ID) is a no-op for sessions
// that have no tool-level session id: shell sessions, sessions that
// have not yet captured a tool session id, and any custom tool whose
// resume config is missing. @bautrey reported 10 duplicate tmux
// sessions on a v0.28.3 fork after a 2-week run on a Linux+systemd
// host with 30 shell-tool projects.
//
// Root cause: sweepDuplicateToolSessions() runs a switch over the
// known tool-session-id env vars and exits silently when none match,
// so the respawn-pane path has no dedup for shell sessions. The
// fallback Restart() branch is similarly gated on i.ClaudeSessionID.
//
// Fix: add an instance-id sweep (AGENTDECK_INSTANCE_ID is set on
// every agent-deck tmux session at instance.go:2129/2282/4415) as a
// second, unconditional guard. Two sweeps are safe — the second is
// a no-op if the first already killed the stale session.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// Shell session with no tool-level session id still sweeps by
// AGENTDECK_INSTANCE_ID so duplicate tmux sessions collapse to one.
func TestIssue678_SweepDuplicateToolSessions_ShellUsesInstanceID(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("shell-sess", "/tmp/x", "shell")
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_shell-sess_beefcafe"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("shell session should sweep once by instance id, got %d calls: %+v", len(*calls), *calls)
	}
	got := (*calls)[0]
	if got.envKey != "AGENTDECK_INSTANCE_ID" {
		t.Errorf("env key = %q, want AGENTDECK_INSTANCE_ID", got.envKey)
	}
	if got.envValue != inst.ID {
		t.Errorf("env value = %q, want instance id %q", got.envValue, inst.ID)
	}
	if got.excludeName != "agentdeck_shell-sess_beefcafe" {
		t.Errorf("exclude = %q, want own tmux session name", got.excludeName)
	}
}

// Claude session with a known ClaudeSessionID keeps the legacy #596
// sweep AND gets the new instance-id guard — belt-and-suspenders for
// cases where the two sweeps catch different stale sessions
// (fork-then-edit scenario from #666).
func TestIssue678_SweepDuplicateToolSessions_ClaudeAlsoInstanceID(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("claude-sess", "/tmp/x", "claude")
	inst.ClaudeSessionID = "abc-123"
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_claude-sess_deadbeef"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 2 {
		t.Fatalf("claude session should sweep twice (tool id + instance id), got %d calls: %+v", len(*calls), *calls)
	}
	var sawClaude, sawInstance bool
	for _, c := range *calls {
		switch c.envKey {
		case "CLAUDE_SESSION_ID":
			sawClaude = true
			if c.envValue != "abc-123" {
				t.Errorf("claude sweep value = %q, want abc-123", c.envValue)
			}
		case "AGENTDECK_INSTANCE_ID":
			sawInstance = true
			if c.envValue != inst.ID {
				t.Errorf("instance sweep value = %q, want %q", c.envValue, inst.ID)
			}
		default:
			t.Errorf("unexpected sweep env key = %q", c.envKey)
		}
	}
	if !sawClaude {
		t.Error("missing CLAUDE_SESSION_ID sweep")
	}
	if !sawInstance {
		t.Error("missing AGENTDECK_INSTANCE_ID sweep")
	}
}

// A Claude session that has not yet captured its ClaudeSessionID
// (placeholder / just-launched) still sweeps by instance id, so the
// race that spawned @bautrey's duplicates is closed for Claude too.
func TestIssue678_SweepDuplicateToolSessions_ClaudePlaceholderUsesInstanceID(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("placeholder", "/tmp/x", "claude")
	inst.ClaudeSessionID = "" // not yet captured
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_placeholder_0001"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("placeholder should sweep once by instance id, got %d calls", len(*calls))
	}
	if (*calls)[0].envKey != "AGENTDECK_INSTANCE_ID" {
		t.Errorf("env key = %q, want AGENTDECK_INSTANCE_ID", (*calls)[0].envKey)
	}
}

// No tmux session → nothing to exclude, skip sweep entirely. Keeps
// the existing guard from #666 tests valid for the instance-id path.
func TestIssue678_SweepDuplicateToolSessions_ShellSkipsWhenNoTmux(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("no-tmux", "/tmp/x", "shell")
	inst.tmuxSession = nil

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 0 {
		t.Fatalf("no tmux session means no sweep; got %d calls", len(*calls))
	}
}
