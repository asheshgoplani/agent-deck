// initial_select_retry_test.go — regression tests for #746 (v1.7.68).
//
// @tarekrached reported that `agent-deck --select <id>` right after
// `launch --json` lands the cursor on an adjacent session instead of
// the target. Manual invocation after a 1–2s idle works, which points
// at a race between the storage watcher catching the just-launched
// session and the TUI's one-shot initial-select attempt.
//
// Root cause: Home.Update's loadSessionsMsg handler only calls
// applyInitialSelection in the `restoreState == nil` branch (the
// very first load). Every subsequent loadSessionsMsg — including the
// auto-reload that fires when the storage watcher notices the new
// session — takes the `restoreState != nil` branch and skips the
// retry entirely. If the first load didn't include the target yet,
// the cursor lands on whatever pendingCursorRestore resolves to (an
// adjacent row) and stays there forever.
//
// This test file locks in two contracts:
//
//  1. applyInitialSelection is idempotent when the target appears on
//     a later load (behavioral — calls the helper twice across a
//     flatItems rebuild).
//  2. The loadSessionsMsg handler invokes applyInitialSelection in
//     BOTH branches (structural — grep-asserts the source so a future
//     refactor can't silently regress). Combined with contract (1),
//     this means the --select target wins as soon as the storage
//     watcher surfaces it.
package ui

import (
	"os"
	"regexp"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// TestRegression746_InitialSelectRetriesOnNextLoad is the behavioral
// guard: applyInitialSelection must succeed on the SECOND call when
// the target was absent on the first.
func TestRegression746_InitialSelectRetriesOnNextLoad(t *testing.T) {
	h := &Home{}
	h.windowsCollapsed = make(map[string]bool)

	older := session.NewInstance("older", "/tmp/a")
	older.ID = "older-111"

	// First load: target not yet in storage (launch --json wrote the
	// registry file, but the TUI hasn't observed the mtime bump yet).
	h.instances = []*session.Instance{older}
	h.groupTree = session.NewGroupTree(h.instances)
	h.SetInitialSelection("target-789")
	h.rebuildFlatItems()
	if h.applyInitialSelection() {
		t.Fatal("applyInitialSelection must return false when target is absent")
	}
	if h.initialSelectDone {
		t.Fatal("initialSelectDone must stay false on a failed match so a later load can retry")
	}

	// Second load: storage watcher picked up the new session.
	target := session.NewInstance("newly-launched", "/tmp/n")
	target.ID = "target-789"
	h.instances = []*session.Instance{older, target}
	h.groupTree = session.NewGroupTree(h.instances)
	h.rebuildFlatItems()
	if !h.applyInitialSelection() {
		t.Fatal("applyInitialSelection must succeed on the retry once the target appears in flatItems")
	}
	if h.cursor < 0 || h.cursor >= len(h.flatItems) {
		t.Fatalf("cursor %d out of range after retry", h.cursor)
	}
	if sel := h.flatItems[h.cursor]; sel.Session == nil || sel.Session.ID != "target-789" {
		t.Fatalf("cursor landed on %+v, want target-789", sel)
	}
}

// TestRegression746_LoadSessionsHandlerRetriesInBothBranches is the
// structural guard: the loadSessionsMsg handler must call
// applyInitialSelection in BOTH the restoreState and initial-load
// branches. Without this, the auto-reload path that fires after
// `launch --json` silently skips the retry.
func TestRegression746_LoadSessionsHandlerRetriesInBothBranches(t *testing.T) {
	src, err := os.ReadFile("home.go")
	if err != nil {
		t.Fatalf("read home.go: %v", err)
	}
	body := string(src)

	// Locate the loadSessionsMsg case block. Simple brace walk from the
	// `case loadSessionsMsg:` marker to the start of the next top-level
	// `case ` or the closing switch brace.
	caseRe := regexp.MustCompile(`case\s+loadSessionsMsg\s*:`)
	caseIdx := caseRe.FindStringIndex(body)
	if caseIdx == nil {
		t.Fatal("could not locate `case loadSessionsMsg:` in home.go")
	}
	// Walk forward and grab ~8000 chars of case body (well beyond the
	// actual block, which fits in a few hundred lines).
	end := caseIdx[1] + 12000
	if end > len(body) {
		end = len(body)
	}
	caseBody := body[caseIdx[1]:end]

	// Trim to the next `case ` at top indentation — tea.Msg cases in Update().
	nextCaseRe := regexp.MustCompile(`(?m)^\tcase\s+\w`)
	if next := nextCaseRe.FindStringIndex(caseBody); next != nil {
		caseBody = caseBody[:next[0]]
	}

	// Find the POST-rebuild restoreState dispatch. There are two
	// `if msg.restoreState != nil {` sites in the loadSessionsMsg case:
	// one pre-rebuild (mutates msg.restoreState.cursorSessionID from the
	// current flatItems) and one post-rebuild (calls
	// h.restoreState(*msg.restoreState)). We want the latter — it's the
	// dispatch that must be paired with applyInitialSelection.
	postRe := regexp.MustCompile(`(?s)if\s+msg\.restoreState\s*!=\s*nil\s*\{\s*h\.restoreState\(`)
	restoreIdx := postRe.FindStringIndex(caseBody)
	if restoreIdx == nil {
		t.Fatal("could not locate post-rebuild `if msg.restoreState != nil { h.restoreState(...)` inside the loadSessionsMsg case — handler shape changed")
	}

	// Split into restoreState branch and the rest (else branch +
	// post-dispatch code). Brace-walk from the `{` after the if to find
	// the matching closing `}`. The matched substring ends just after
	// the `h.restoreState(` call, so the opening `{` lives somewhere
	// inside the match — locate the first `{` within the match window.
	braceOffsetRe := regexp.MustCompile(`\{`)
	rel := braceOffsetRe.FindStringIndex(caseBody[restoreIdx[0]:restoreIdx[1]])
	if rel == nil {
		t.Fatal("could not locate opening { for post-rebuild restoreState block")
	}
	braceStart := restoreIdx[0] + rel[0]
	depth := 0
	var restoreEnd int
	for j := braceStart; j < len(caseBody); j++ {
		switch caseBody[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				restoreEnd = j
				goto done
			}
		}
	}
done:
	if restoreEnd == 0 {
		t.Fatal("could not find matching } for restoreState branch")
	}
	restoreBranch := caseBody[braceStart : restoreEnd+1]
	rest := caseBody[restoreEnd+1:]

	applyRe := regexp.MustCompile(`\bapplyInitialSelection\s*\(`)
	if !applyRe.MatchString(restoreBranch) {
		t.Error("#746: loadSessionsMsg restoreState branch must call applyInitialSelection — otherwise auto-reload after `launch --json` never retries the --select target")
	}
	// The else / post-dispatch branch must also call it (current v1.7.68
	// behavior — keeping the grep so the first-load path can't regress
	// either).
	if !applyRe.MatchString(rest) {
		t.Error("#746: loadSessionsMsg initial-load branch must call applyInitialSelection")
	}
}
