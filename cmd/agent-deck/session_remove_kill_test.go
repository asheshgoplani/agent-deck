// session_remove_kill_test.go — regression tests for issue #59 (v1.7.68).
//
// Context: on 2026-04-22 a claude process (PID 321456,
// AGENTDECK_INSTANCE_ID `bcb1d1cc-1776748185`) was found running for
// 33+ hours with no corresponding agent-deck session record. The
// `session remove` code path in v1.7.61 only called `inst.Kill()`
// when the caller also passed `--prune-worktree`. `remove --force`
// alone deleted the registry row but left the tmux scope (and any
// SIGHUP-immune claude process inside it) alive forever.
//
// These tests lock in two properties of the fix:
//
//  1. handleSessionRemove unconditionally invokes the Instance kill path
//     (inst.Kill or inst.KillAndWait) — NOT gated on --prune-worktree.
//  2. The bulk --all-errored path does the same.
//
// Both assertions are structural (source-level) because a real-tmux
// integration test would need a running claude/shell binary and a
// clean tmux server per test, and the failure mode to guard against
// is specifically "the code forgot to call Kill" — a readable
// invariant that a source-level test encodes cheaply.

package main

import (
	"os"
	"regexp"
	"testing"
)

// extractFuncBody returns the body of a named Go function from source.
// Finds `func <name>` then walks to the next top-level `{` and does
// brace-counting to the matching `}`. Handles multi-line signatures.
func extractFuncBody(src, fnName string) string {
	re := regexp.MustCompile(`(?m)^func[^\n]*\b` + regexp.QuoteMeta(fnName) + `\s*\(`)
	loc := re.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	i := loc[1]
	for i < len(src) && src[i] != '{' {
		i++
	}
	if i == len(src) {
		return ""
	}
	start := i + 1
	depth := 1
	for j := start; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start:j]
			}
		}
	}
	return ""
}

// The single-session remove handler MUST call inst.Kill (or KillAndWait)
// in its mainline — before issue #59 this was only reachable via the
// --prune-worktree side branch, so `session remove --force` silently
// leaked the tmux scope and every child process in it.
func TestSessionRemove_HandlerCallsKillUnconditionally(t *testing.T) {
	src, err := os.ReadFile("session_remove_cmd.go")
	if err != nil {
		t.Fatalf("read session_remove_cmd.go: %v", err)
	}
	body := extractFuncBody(string(src), "handleSessionRemove")
	if body == "" {
		t.Fatalf("could not extract handleSessionRemove body — file layout changed?")
	}
	// Must call the kill path. `.Kill(` and `.KillAndWait(` both satisfy
	// the fix; either is acceptable.
	killRe := regexp.MustCompile(`inst\.(Kill|KillAndWait)\s*\(`)
	if !killRe.MatchString(body) {
		t.Errorf(
			"handleSessionRemove must unconditionally invoke inst.Kill / inst.KillAndWait "+
				"(issue #59 regression guard); function body:\n%s",
			body,
		)
	}
}

// The bulk-delete path removes many sessions in a loop and must ALSO kill each
// one — same rationale as the single-session handler. Both bulk callers
// (`--all-errored` and `session cleanup`) route through bulkRemoveSessions, so
// the invariant is asserted where it now lives.
func TestSessionRemove_BulkRemoveCallsKillUnconditionally(t *testing.T) {
	src, err := os.ReadFile("session_remove_cmd.go")
	if err != nil {
		t.Fatalf("read session_remove_cmd.go: %v", err)
	}
	body := extractFuncBody(string(src), "bulkRemoveSessions")
	if body == "" {
		t.Fatalf("could not extract bulkRemoveSessions body — file layout changed?")
	}
	killRe := regexp.MustCompile(`\b(Kill|KillAndWait)\s*\(`)
	if !killRe.MatchString(body) {
		t.Errorf(
			"bulkRemoveSessions must kill each session before deleting it "+
				"(issue #59 regression guard); function body:\n%s",
			body,
		)
	}
}

// ...and the bulk callers must actually route through it, or the guard above
// protects an unused function. This is what let the two hand-copied bulk paths
// drift apart (only one killed, only one skipped pinned) before they were
// merged into bulkRemoveSessions.
func TestSessionRemove_BulkCallersDelegateToBulkRemoveSessions(t *testing.T) {
	for file, fn := range map[string]string{
		"session_remove_cmd.go":  "removeAllErrored",
		"session_cleanup_cmd.go": "handleSessionCleanup",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		body := extractFuncBody(string(src), fn)
		if body == "" {
			t.Fatalf("could not extract %s body from %s — file layout changed?", fn, file)
		}
		if !regexp.MustCompile(`\bbulkRemoveSessions\s*\(`).MatchString(body) {
			t.Errorf(
				"%s must delegate its bulk delete to bulkRemoveSessions "+
					"(single-sourced #59/#909/#910 choreography); function body:\n%s",
				fn, body,
			)
		}
	}
}

// `session cleanup` must never force-remove a git worktree implicitly: that
// destroys uncommitted work, and the command advertises itself as registry-only.
// pruneSessionWorktree may only be reached via the opt-in --prune-worktree flag,
// which cleanup passes straight through to bulkRemoveSessions.
func TestSessionCleanup_DoesNotPruneWorktreesImplicitly(t *testing.T) {
	src, err := os.ReadFile("session_cleanup_cmd.go")
	if err != nil {
		t.Fatalf("read session_cleanup_cmd.go: %v", err)
	}
	if regexp.MustCompile(`\bpruneSessionWorktree\s*\(`).MatchString(string(src)) {
		t.Errorf("session_cleanup_cmd.go must not call pruneSessionWorktree directly — " +
			"worktree deletion is opt-in via --prune-worktree, threaded through bulkRemoveSessions")
	}
	body := extractFuncBody(string(src), "handleSessionCleanup")
	if !regexp.MustCompile(`prune-worktree`).MatchString(body) {
		t.Errorf("handleSessionCleanup must expose an explicit --prune-worktree opt-in flag")
	}
}

// The interactive confirm prompt is a TOCTOU window: a candidate can be
// restarted while [y/N] sits open. handleSessionCleanup must re-probe liveness
// after the prompt and before any destructive call.
func TestSessionCleanup_ReprobesLivenessAfterConfirm(t *testing.T) {
	src, err := os.ReadFile("session_cleanup_cmd.go")
	if err != nil {
		t.Fatalf("read session_cleanup_cmd.go: %v", err)
	}
	body := extractFuncBody(string(src), "handleSessionCleanup")
	if body == "" {
		t.Fatalf("could not extract handleSessionCleanup body — file layout changed?")
	}
	confirm := regexp.MustCompile(`isYesConfirmation\s*\(`).FindStringIndex(body)
	reprobe := regexp.MustCompile(`dropRevivedCandidates\s*\(`).FindStringIndex(body)
	destroy := regexp.MustCompile(`bulkRemoveSessions\s*\(`).FindStringIndex(body)
	if confirm == nil || reprobe == nil || destroy == nil {
		t.Fatalf("expected confirm, re-probe and bulk-delete calls in handleSessionCleanup")
	}
	if !(confirm[0] < reprobe[0] && reprobe[0] < destroy[0]) {
		t.Errorf("handleSessionCleanup must re-probe liveness (dropRevivedCandidates) AFTER the " +
			"[y/N] confirm and BEFORE bulkRemoveSessions, or a session revived during the prompt is killed")
	}
}
