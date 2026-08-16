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
// out of scope. This file pinned the end-to-end contract.
//
// CONTRACT REVERSED BY #1815 — read this before "fixing" the test back.
// The recovery above is indistinguishable, at the moment of resume, from the
// incident #1815 reports: a session with no recorded conversation id was
// restarted, disk discovery offered the newest transcript filed under the
// working directory, and the restart resumed a conversation belonging to a
// DIFFERENT session — bringing that session's context and authority up in
// this pane. Discovery cannot tell "my lost transcript" from "the neighbour's
// transcript": both are simply the newest jsonl in a shared directory.
//
// #1815 rules that the ambiguity must fail closed. A discovered id is a hint,
// never proof of ownership, so it may not authorize --resume. The cost is
// exactly the #956 recovery: a custom-wrapper session whose id was never
// captured now starts fresh instead of re-attaching its own history. Losing
// one conversation's history is recoverable; adopting another session's
// conversation is not.
//
// What survives from #956: the restart still routes through the claude spawn
// path with an explicit session id rather than blindly re-running the wrapper.
// What this test now pins is the refusal.
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

// TestConductor_Restart_DoesNotAdoptDiscoveredTranscript_1815 is the #956
// scenario under the #1815 ruling: a custom-command Claude session with an
// empty ClaudeSessionID at Restart() time, and a JSONL transcript sitting in
// its working directory that nothing can attribute to it.
//
// Post-#1815 contract: the restart must NOT emit `--resume <discovered
// uuid>`, and must not claim the discovered uuid via --session-id either (a
// shared id would also make the duplicate sweeper kill one of the pair). It
// starts fresh, on the claude spawn path, with a newly minted id.
func TestConductor_Restart_DoesNotAdoptDiscoveredTranscript_1815(t *testing.T) {
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
	projectDir := claudeProjectDirForTest(t, filepath.Join(home, ".claude"), inst.ProjectPath)
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

	// Restart: discovery offers the transcript; the identity guard refuses it.
	require.NoError(t, inst.Restart(), "Restart: must succeed")

	argv := readCapturedClaudeArgv(t, argvLog, 3*time.Second)
	joined := strings.Join(argv, " ")

	require.NotContains(t, joined, "--resume",
		"#1815: a transcript this session cannot be shown to own must not be "+
			"resumed. Discovery cannot distinguish this session's lost "+
			"transcript from a neighbour's in a shared directory, so the "+
			"ambiguity fails closed. Got argv: %v", argv)
	require.NotContains(t, joined, jsonlUUID,
		"#1815: the refused id may belong to another session and must not be "+
			"reused via --session-id either (a shared CLAUDE_SESSION_ID also "+
			"trips the duplicate sweeper). Got argv: %v", argv)
	require.Contains(t, joined, "--session-id",
		"#1815: the refusal must still start the session on the claude spawn "+
			"path with an explicit fresh id (that part of #956 survives). "+
			"Got argv: %v", argv)

	// Write-through: the Instance carries the freshly minted id, not the
	// discovered one, so nothing unverified reaches the save cycle.
	require.NotEmpty(t, inst.ClaudeSessionID,
		"#1815: the instance must carry the freshly minted conversation id")
	require.NotEqual(t, jsonlUUID, inst.ClaudeSessionID,
		"#1815: the instance must NOT keep the discovered (unowned) uuid")
}
