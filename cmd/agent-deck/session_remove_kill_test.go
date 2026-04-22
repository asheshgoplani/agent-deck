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

// The bulk-errored path removes many sessions in a loop and must ALSO
// kill each one — same rationale as the single-session handler.
func TestSessionRemove_AllErroredCallsKillUnconditionally(t *testing.T) {
	src, err := os.ReadFile("session_remove_cmd.go")
	if err != nil {
		t.Fatalf("read session_remove_cmd.go: %v", err)
	}
	body := extractFuncBody(string(src), "removeAllErrored")
	if body == "" {
		t.Fatalf("could not extract removeAllErrored body — file layout changed?")
	}
	killRe := regexp.MustCompile(`\b(Kill|KillAndWait)\s*\(`)
	if !killRe.MatchString(body) {
		t.Errorf(
			"removeAllErrored must kill each session before deleting it "+
				"(issue #59 regression guard); function body:\n%s",
			body,
		)
	}
}
