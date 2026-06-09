// rename_title_lock_test.go — CLI contract tests for title locking on
// explicit renames and explicitly titled forks (PR #1355 review follow-up).
//
// An explicit rename or fork title is user intent: it must set TitleLocked so
// the #572 Claude-name sync (e.g. an auto-assigned plan title) can't revert
// it on the next hook event. Direct `inst.Title = ...` assignments bypass the
// SetField mutator that applies the lock.
//
// Why structural assertions instead of end-to-end handler invocation:
// handleRename and handleSessionFork call os.Exit on every error path, and
// there is no runMain/TestHelperProcess subprocess harness in this package.
// We follow the extractFuncBody precedent from session_remove_kill_test.go.

package main

import (
	"os"
	"strings"
	"testing"
)

func mustExtractFuncBody(t *testing.T, file, funcName string) string {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	body := extractFuncBody(string(src), funcName)
	if body == "" {
		t.Fatalf("could not extract %s body from %s — file layout changed?", funcName, file)
	}
	return body
}

// TestHandleRename_RoutesThroughSetField: `agent-deck rename` must apply the
// title via session.SetField (which sets TitleLocked), not by assigning
// inst.Title directly.
func TestHandleRename_RoutesThroughSetField(t *testing.T) {
	body := foldSpaces(mustExtractFuncBody(t, "main.go", "handleRename"))

	if !strings.Contains(body, "session.SetField(inst, session.FieldTitle, newTitle, nil)") {
		t.Error("handleRename must route the rename through session.SetField(FieldTitle) so TitleLocked is set")
	}
	if strings.Contains(body, "inst.Title = newTitle") {
		t.Error("handleRename must not assign inst.Title directly — that bypasses the TitleLocked mutator")
	}
}

// TestHandleSessionFork_LocksExplicitTitle: `agent-deck session fork -t X`
// must lock the fork's title, while the auto-generated "<title>-fork" default
// keeps the #572 name sync enabled (mirrors the TUI dialog-vs-quick-fork
// split).
func TestHandleSessionFork_LocksExplicitTitle(t *testing.T) {
	body := foldSpaces(mustExtractFuncBody(t, "session_cmd.go", "handleSessionFork"))

	if !strings.Contains(body, `explicitTitle := forkTitle != ""`) {
		t.Error("handleSessionFork must record whether -t/--title was explicitly passed before applying the default")
	}
	if !strings.Contains(body, "if explicitTitle { forkedInst.TitleLocked = true }") {
		t.Error("handleSessionFork must set forkedInst.TitleLocked when -t/--title was explicitly passed")
	}
}
