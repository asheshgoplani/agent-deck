package session

import (
	"os"
	"strings"
	"testing"
)

// TestBuildClaudeCommand_EmptyBaseCommand_StillEmitsClaudeBinary pins the
// regression behind the user-reported "R/Enter loop forever" symptom on
// feature/sessions-dispear-on-restart. Repro state, captured live from
// the user's state.db on 2026-04-27:
//
//	row: tool=claude, command="", tool_data="{}" (no ClaudeSessionID)
//
// Restart() at instance.go fallback path picks
//
//	command = i.buildClaudeCommand(i.Command)   // i.Command == ""
//
// because IsClaudeCompatible(i.Tool) holds but ClaudeSessionID is empty.
// The current implementation of buildClaudeCommandWithMessage gates its
// claude-build branch on `baseCommand == "claude"` and falls through to
// the custom-command branch when baseCommand is empty, returning JUST
// the env exports (`export AGENTDECK_INSTANCE_ID=...; ` with no claude
// invocation). The pane runs that, exits, status flips back to error,
// and R/Enter loop indefinitely.
//
// Captured restart command from the user's debug.log:
//
//	export COLORFGBG='15;0' && unset TELEGRAM_STATE_DIR && export
//	COLORFGBG='15;0' && unset TELEGRAM_STATE_DIR && export
//	AGENTDECK_INSTANCE_ID=9e618f9f-1773801500;
//
// Note: zero `claude` invocations.
//
// Contract being pinned: when Tool is Claude-compatible, the produced
// command MUST contain a `claude` binary invocation regardless of
// whether i.Command was "" or "claude". An empty command on a Claude
// tool must default to "claude".
func TestBuildClaudeCommand_EmptyBaseCommand_StillEmitsClaudeBinary(t *testing.T) {
	origHome := os.Getenv("HOME")
	t.Setenv("HOME", t.TempDir())
	ClearUserConfigCache()
	t.Cleanup(func() {
		os.Setenv("HOME", origHome)
		ClearUserConfigCache()
	})

	inst := NewInstanceWithTool("Smithy", t.TempDir(), "claude")
	// Smithy's stored state: empty Command, no ClaudeSessionID, no extra
	// per-instance ClaudeOptions overrides. Default-shaped Claude row
	// from a session that lost its tool-data fingerprint.
	inst.Command = ""
	inst.ClaudeSessionID = ""

	cmd := inst.buildClaudeCommand(inst.Command)

	// The regression: cmd is just env exports terminated by `; `. The
	// strict assertion: a `claude` binary invocation must appear after
	// the env prefix. We accept any of:
	//   - `claude `         (bare binary + flag)
	//   - `claude\n`        (end of command)
	//   - `claude --`       (flag follows)
	// To stay robust to the env-prefix-with-claude-substring case,
	// require both a claude token AND a --session-id flag (the marker
	// of a real new-session start path inside buildClaudeCommandWithMessage).
	if !strings.Contains(cmd, "claude") {
		t.Fatalf("buildClaudeCommand(\"\") for Tool=claude must invoke the `claude` binary;\n"+
			"got: %q", cmd)
	}
	if !strings.Contains(cmd, "--session-id") {
		t.Errorf("expected --session-id flag (new-session start path) when ClaudeSessionID is empty;\n"+
			"got: %q", cmd)
	}

	// Negative assertion: the produced command must not be just env
	// exports with no actual binary to run. The simplest signature of
	// the bug is the trailing semicolon with nothing executable after
	// the AGENTDECK_INSTANCE_ID export.
	if strings.HasSuffix(strings.TrimSpace(cmd), "AGENTDECK_INSTANCE_ID="+inst.ID+";") {
		t.Errorf("produced command ends at the AGENTDECK_INSTANCE_ID export with no executable;\n"+
			"this is the live-bug signature. got: %q", cmd)
	}
}
