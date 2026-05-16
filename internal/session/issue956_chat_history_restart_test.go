package session

// Issue #956 — Conductor restart loses Claude Code chat history.
//
// Custom-command Claude sessions (Tool=claude with a wrapper Command) bypass
// agent-deck's happy-path session-id capture. When such a session has had a
// real conversation (Claude wrote a JSONL transcript to disk) but the
// session id was never propagated back into the Instance (hooks didn't fire,
// or CLAUDE_CONFIG_DIR override kept hooks from being installed), Restart()
// must still pick up the latest JSONL on disk and emit `claude --resume
// <uuid>` so chat history survives the restart.
//
// Start() and StartWithMessage() already handle this via
// `ensureClaudeSessionIDFromDisk` (v1.5.2 REQ-7). Restart() was missed:
// its dispatch tree at instance.go:Restart() falls through to the fallback
// recreate path when ClaudeSessionID is empty, which calls
// `buildClaudeCommand(i.Command)` — running the wrapper directly with no
// --resume, dropping history.
//
// PR #989 (REQ-7 manifestation 3) addressed CanRestart() for the same
// class of sessions (registry-level check), but the resume dispatch was
// out of scope. This file pins the end-to-end contract.
//
// See ~/.claude/projects/-home-ashesh-goplani--agent-deck/memory/
// conductor_restart_history_loss.md for the structural background.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestConductor_Restart_PreservesChatHistory_RegressionFor956 pins the
// contract: when a custom-command Claude session has empty ClaudeSessionID
// at Restart() time and a JSONL transcript exists on disk (written during
// the live conversation), Restart() MUST discover the latest JSONL and
// resume from it (--resume <uuid>) rather than spawn a fresh wrapper that
// loses history.
//
// RED on main: Restart()'s fallback recreate path (instance.go:Restart)
// dispatches through buildClaudeCommand(i.Command), running the wrapper
// fresh. No --resume is emitted. The argv log shows wrapper invocation
// without any --resume flag.
//
// GREEN with the fix: Restart() invokes the disk-discovery prelude before
// dispatch (same logic as Start()'s ensureClaudeSessionIDFromDisk, but
// bypassing the #608 brand-new-session gate because Restart() implies the
// instance previously ran). ClaudeSessionID is populated from the latest
// JSONL, the respawn-pane fast path engages, and `claude --resume <uuid>`
// is spawned via buildClaudeResumeCommand.
func TestConductor_Restart_PreservesChatHistory_RegressionFor956(t *testing.T) {
	requireTmux(t)
	home := isolatedHomeDir(t)
	argvLog := setupStubClaudeOnPATH(t, home)
	inst := newClaudeInstanceForDispatch(t, home)

	// Custom-command preconditions: wrapper bypasses happy-path capture.
	inst.Command = writeCustomWrapperScript(t, home)
	// Clear the auto-assigned ClaudeSessionID from newClaudeInstanceForDispatch
	// so we model the real bug scenario: a session id was never captured.
	inst.ClaudeSessionID = ""
	// ClaudeDetectedAt stays zero — the session never propagated its id back
	// to the Instance. This is the hostile case for ensureClaudeSessionIDFromDisk:
	// Start()'s prelude early-returns on zero ClaudeDetectedAt (#608 gate),
	// so the Restart() fix MUST handle the case independently.
	inst.ClaudeDetectedAt = time.Time{}

	// First Start: no JSONL exists yet, custom wrapper runs, ClaudeSessionID
	// stays empty. This mirrors the real-world flow where a conductor is
	// launched fresh via a wrapper script.
	require.NoError(t, inst.Start(), "Start: custom-command claude session must boot")
	// Allow the tmux pane to start and the wrapper to log its argv.
	time.Sleep(500 * time.Millisecond)
	require.Empty(t, inst.ClaudeSessionID,
		"precondition: no JSONL on disk at Start time → no discovery → "+
			"ClaudeSessionID stays empty (this is what triggers the bug at Restart)")

	// Now write a JSONL on disk — simulating the live conversation Claude
	// just had. This is the state at the moment the user runs `restart`.
	const jsonlUUID = "9560ab10-9560-9560-9560-956000000956"
	projectDir := filepath.Join(home, ".claude", "projects", ConvertToClaudeDirName(inst.ProjectPath))
	require.NoError(t, os.MkdirAll(projectDir, 0o755))
	jsonlPath := filepath.Join(projectDir, jsonlUUID+".jsonl")
	body := []byte(`{"sessionId":"` + jsonlUUID + `","role":"user","content":"remember my favorite color is blue"}` + "\n" +
		`{"sessionId":"` + jsonlUUID + `","role":"assistant","content":"got it, blue"}` + "\n")
	require.NoError(t, os.WriteFile(jsonlPath, body, 0o644))
	// Backdate slightly so any newer file would clearly beat it on mtime;
	// here there is only one, so this is just hygiene.
	past := time.Now().Add(-10 * time.Second)
	require.NoError(t, os.Chtimes(jsonlPath, past, past))
	t.Cleanup(func() { _ = os.Remove(jsonlPath) })

	// Reset the argv log so the Restart's argv is the only entry we inspect.
	require.NoError(t, os.WriteFile(argvLog, nil, 0o644))

	// Restart: must discover the JSONL and resume rather than spawn fresh.
	require.NoError(t, inst.Restart(), "Restart: must succeed")

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")

	// Contract assertion: post-restart, the spawned claude argv must contain
	// --resume <jsonl-uuid>. On main this fails because Restart()'s
	// fallback path runs the wrapper without --resume.
	require.Contains(t, joined, "--resume",
		"Issue #956 RED: Restart() of custom-command claude session with "+
			"empty ClaudeSessionID and a JSONL transcript on disk must "+
			"discover the JSONL and pass --resume to preserve chat history. "+
			"Got argv: %v. The fix mirrors Start()'s ensureClaudeSessionIDFromDisk "+
			"prelude but bypasses the #608 brand-new gate because Restart() "+
			"implies the session previously ran.", argv)
	require.Contains(t, joined, jsonlUUID,
		"Issue #956 RED: Restart() must resume the newest JSONL uuid "+
			"(%s), not mint a fresh id. Got argv: %v", jsonlUUID, argv)

	// Write-through assertion: after Restart(), the Instance MUST carry the
	// discovered session id so subsequent Restart() / status reads see it.
	require.Equal(t, jsonlUUID, inst.ClaudeSessionID,
		"Issue #956: Restart() must persist the discovered JSONL uuid onto "+
			"the Instance so the next operation sees a populated id. "+
			"Mirrors PERSIST-12 write-through from the Start() path.")
}
