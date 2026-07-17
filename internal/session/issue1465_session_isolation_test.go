package session

// Issue #1465 — sequential Claude review sessions in the same project dir
// inherit a prior session's conversation ("stale context").
//
// Reporter (@lantzbuilds) scenario: a conductor launches PR-333-claude in
// repo dir X, removes it, then launches PR-335-claude in the SAME dir X — and
// the second session resumes the PR-333 conversation instead of starting
// fresh, ignoring its launch prompt.
//
// Root cause: the Start()-path prelude ensureClaudeSessionIDFromDisk did NOT
// honor an explicit `--session-id <uuid>` baked into i.Command before falling
// into mtime-based disk discovery. The Restart()-path prelude already adopts
// the explicit id first (issue #1147, commit 5333dfb4), so the two preludes
// were asymmetric: a Start-path session that lost its ClaudeSessionID (custom
// wrapper, no hook capture) but carries an explicit --session-id in its
// Command could still hijack a newer sibling transcript from the shared cwd.
//
// The reporter's literal evidence (`... --continue --session-dir ...`) is the
// Pi builder's template (buildPiCommand), not Claude's — Claude's "new" mode
// has used a fresh `--session-id <uuid>` (never `-c`) for many releases, so a
// plain fresh `add` is already isolated by the #608 gate. These tests pin the
// remaining contract on the Start path:
//
//   1. Explicit --session-id wins over a newer sibling JSONL (the fix).
//   2. No explicit id + prior conversation: disk discovery still recovers it.
//   3. A brand-new session (ClaudeDetectedAt zero) never inherits a sibling's
//      conversation — the reporter's exact remove-then-recreate scenario.

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIssue1465_StartPrelude_ExplicitIDWinsOverSiblingDiskDiscovery is the core
// regression. A Start-path session in a shared cwd, with its own explicit
// --session-id in Command, a lost ClaudeSessionID, and a non-zero
// ClaudeDetectedAt (so the #608 gate does NOT early-return), must adopt its
// OWN id — not the newest sibling JSONL.
//
// RED before fix: ensureClaudeSessionIDFromDisk skips the explicit id, passes
// the #608 gate, disk-discovers the newest sibling UUID, and hijacks it.
// GREEN after fix: the explicit-id extractor adopts the Command's UUID and
// skips disk discovery.
func TestIssue1465_StartPrelude_ExplicitIDWinsOverSiblingDiskDiscovery(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	const (
		uuidOwn     = "12345678-1465-1465-1465-121212121212"
		uuidSibling = "99999999-1465-1465-1465-999999999999"
	)

	inst := newClaudeInstanceForExplicitID(t, home,
		"env REVIEW=PR-335 claude --session-id "+uuidOwn+" --dangerously-skip-permissions")

	// A newer sibling transcript (a prior PR-333 review removed and left on
	// disk) sits in the shared cwd, newest by mtime.
	now := time.Now()
	stageJSONL(t, home, inst.ProjectPath, uuidOwn, now.Add(-1*time.Minute))
	stageJSONL(t, home, inst.ProjectPath, uuidSibling, now)

	inst.ensureClaudeSessionIDFromDisk()

	require.Equal(t, uuidOwn, inst.ClaudeSessionID,
		"Issue #1465: a Start-path session with `--session-id %s` in its Command must adopt that UUID, not the newest sibling JSONL %s. Got %q — the session would resume the prior review's conversation.",
		uuidOwn, uuidSibling, inst.ClaudeSessionID)
}

// TestIssue1465_StartPrelude_NoExplicitID_DiskDiscoveryPreserved pins parity:
// the fix must NOT regress legitimate Start-path recovery. A session with NO
// explicit --session-id but a prior conversation (ClaudeDetectedAt non-zero)
// must still bind the on-disk JSONL.
func TestIssue1465_StartPrelude_NoExplicitID_DiskDiscoveryPreserved(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	inst := newClaudeInstanceForExplicitID(t, home, "/usr/local/bin/claude-wrapper.sh")

	const jsonlUUID = "14650000-1465-1465-1465-146500001465"
	stageJSONL(t, home, inst.ProjectPath, jsonlUUID, time.Now())

	inst.ensureClaudeSessionIDFromDisk()

	require.Equal(t, jsonlUUID, inst.ClaudeSessionID,
		"Issue #1465 fix must NOT regress Start-path recovery: a session with no explicit --session-id and a prior conversation must still auto-bind the newest JSONL. Got %q.",
		inst.ClaudeSessionID)
}

// TestIssue1465_FreshSession_DoesNotInheritSiblingHistory pins the reporter's
// exact remove-then-recreate scenario: a brand-new session (ClaudeDetectedAt
// zero, no explicit id) launched in a cwd that previously hosted another
// Claude session must NOT inherit that session's conversation. The #608 gate
// already guarantees this; this test locks it in so the guarantee can't
// silently regress.
func TestIssue1465_FreshSession_DoesNotInheritSiblingHistory(t *testing.T) {
	home := isolatedHomeDir(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()

	// A prior review (PR-333) left a transcript in the shared cwd.
	priorProject := filepath.Join(home, "ui-library")
	require.NoError(t, os.MkdirAll(priorProject, 0o755))
	const priorUUID = "33333333-0333-0333-0333-033303330333"
	stageJSONL(t, home, priorProject, priorUUID, time.Now())

	// PR-335 is a freshly added session in the SAME cwd: no explicit id, and
	// ClaudeDetectedAt zero (never started before).
	fresh := &Instance{
		ID:               "test-1465-fresh",
		Tool:             "claude",
		ProjectPath:      priorProject,
		Command:          "claude",
		ClaudeSessionID:  "",
		ClaudeDetectedAt: time.Time{}, // zero — brand-new spawn
	}

	fresh.ensureClaudeSessionIDFromDisk()

	require.Empty(t, fresh.ClaudeSessionID,
		"Issue #1465: a brand-new session (ClaudeDetectedAt zero) in a cwd that previously hosted another Claude session must NOT adopt the prior session's transcript %s. Got %q — that is the stale-context bug.",
		priorUUID, fresh.ClaudeSessionID)
}
