package session

// Issue #666 — restart path must sweep duplicate tool sessions across
// tmux. The fallback restart path already calls
// tmux.KillSessionsWithEnvValue at instance.go:4396 to kill other
// agentdeck tmux sessions that share the same CLAUDE_SESSION_ID
// (issue #596 fix). The primary respawn-pane path was missing the same
// sweep, so if two agentdeck tmux sessions ended up with the same tool
// session id (fork-then-edit scenario, or a user `session set
// claude-session-id` collision), restarting one would leave the other's
// claude process running — compounding the telegram 409 conflict the
// user reported in #666.
//
// We cover this with a unit test on a small helper Instance method,
// sweepDuplicateToolSessions(), which is wired into every respawn-pane
// branch of Restart(). Driving the full Restart() path requires real
// tmux + claude; the helper test is fast and deterministic, and an
// integration test in instance_test.go already covers the live path.

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// spyCall captures one invocation of killDuplicateSessionsFn.
type spyCall struct {
	envKey      string
	envValue    string
	excludeName string
}

// withSpyKiller substitutes killDuplicateSessionsFn with a spy for the
// duration of the test and returns a pointer to the recorded calls plus
// a cleanup func.
func withSpyKiller(t *testing.T) (*[]spyCall, func()) {
	t.Helper()
	prev := killDuplicateSessionsFn
	var calls []spyCall
	killDuplicateSessionsFn = func(envKey, envValue, excludeName string) {
		calls = append(calls, spyCall{envKey, envValue, excludeName})
	}
	return &calls, func() { killDuplicateSessionsFn = prev }
}

func TestIssue666_SweepDuplicateToolSessions_Claude(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("claude-sess", "/tmp/x", "claude")
	inst.ClaudeSessionID = "abc123"
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_claude-sess_deadbeef"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("expected exactly one sweep call, got %d", len(*calls))
	}
	got := (*calls)[0]
	if got.envKey != "CLAUDE_SESSION_ID" {
		t.Errorf("env key = %q, want CLAUDE_SESSION_ID", got.envKey)
	}
	if got.envValue != "abc123" {
		t.Errorf("env value = %q, want abc123", got.envValue)
	}
	if got.excludeName != "agentdeck_claude-sess_deadbeef" {
		t.Errorf("exclude = %q, want agentdeck_claude-sess_deadbeef (self)", got.excludeName)
	}
}

func TestIssue666_SweepDuplicateToolSessions_Gemini(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("gem-sess", "/tmp/x", "gemini")
	inst.GeminiSessionID = "gem-xyz"
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_gem-sess_cafef00d"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("expected one sweep call, got %d", len(*calls))
	}
	if (*calls)[0].envKey != "GEMINI_SESSION_ID" {
		t.Errorf("env key = %q, want GEMINI_SESSION_ID", (*calls)[0].envKey)
	}
}

func TestIssue666_SweepDuplicateToolSessions_OpenCode(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("oc-sess", "/tmp/x", "opencode")
	inst.OpenCodeSessionID = "oc-xyz"
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_oc-sess_f00d"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("expected one sweep call, got %d", len(*calls))
	}
	if (*calls)[0].envKey != "OPENCODE_SESSION_ID" {
		t.Errorf("env key = %q, want OPENCODE_SESSION_ID", (*calls)[0].envKey)
	}
}

func TestIssue666_SweepDuplicateToolSessions_Codex(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("cx-sess", "/tmp/x", "codex")
	inst.CodexSessionID = "cx-xyz"
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_cx-sess_1234"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 1 {
		t.Fatalf("expected one sweep call, got %d", len(*calls))
	}
	if (*calls)[0].envKey != "CODEX_SESSION_ID" {
		t.Errorf("env key = %q, want CODEX_SESSION_ID", (*calls)[0].envKey)
	}
}

// No session id → nothing to sweep (the sweep key is the session id).
func TestIssue666_SweepDuplicateToolSessions_SkipsWhenNoSessionID(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("noid", "/tmp/x", "claude")
	inst.ClaudeSessionID = ""
	inst.tmuxSession = &tmux.Session{Name: "agentdeck_noid_dead"}

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 0 {
		t.Fatalf("no session id means no sweep; got %d calls", len(*calls))
	}
}

// No tmux session → cannot exclude ourselves, so we must not sweep.
func TestIssue666_SweepDuplicateToolSessions_SkipsWhenNoTmux(t *testing.T) {
	calls, restore := withSpyKiller(t)
	defer restore()

	inst := NewInstanceWithTool("notmux", "/tmp/x", "claude")
	inst.ClaudeSessionID = "abc"
	inst.tmuxSession = nil

	inst.sweepDuplicateToolSessions()

	if len(*calls) != 0 {
		t.Fatalf("no tmux session means no sweep; got %d calls", len(*calls))
	}
}
