# Session Remove Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `agent-deck session remove <id>` CLI subcommand (+ `--force`, `--all-errored`, `--prune-worktree` flags) and a status-gated TUI `X`/`Ctrl+X` keybind for removing stopped/errored sessions from the daemon registry while preserving Claude transcripts.

**Architecture:** New CLI file `session_remove_cmd.go` (separate from the 1000+ line `session_cmd.go`) that loads sessions, resolves the identifier, status-gates, and calls the existing `Storage.DeleteInstance(id)`. New TUI confirm types `ConfirmRemoveSession` + `ConfirmBulkRemoveErrored` route to new `h.removeSession(inst)` / `h.bulkRemoveErrored()` handlers. The existing `"d"` → `deleteSession()` path (destructive, worktree-cleaning) is **not touched**.

**Tech Stack:** Go 1.24.0, Bubble Tea v1 (charmbracelet), SQLite (via `internal/statedb`), teatest for Seam B, subprocess `runAgentDeck` for CLI tests.

**Ship version:** v1.7.61 (main is at v1.7.58; leaves room for 1.7.59/1.7.60 parallel sessions).

---

## File Structure

**Create:**
- `cmd/agent-deck/session_remove_cmd.go` — new CLI handler (`handleSessionRemove`, `printSessionRemoveHelp`)
- `cmd/agent-deck/session_remove_cmd_test.go` — subprocess CLI tests (mirror `session_move_test.go`)
- `internal/ui/session_remove_tui_test.go` — Seam A tests for `X` / `Ctrl+X`

**Modify:**
- `cmd/agent-deck/session_cmd.go:29-68` — add `case "remove":` + help text line
- `internal/ui/confirm_dialog.go:15-24` — add two new `ConfirmType` constants
- `internal/ui/confirm_dialog.go:63+` — add `ShowRemoveSession`, `ShowBulkRemoveErrored`
- `internal/ui/confirm_dialog.go:261+` (View switch) — add render cases
- `internal/ui/home.go` — add `case "X":` + `case "ctrl+x":`, add `h.removeSession` + `h.bulkRemoveErrored`, wire `confirmAction` cases
- `cmd/agent-deck/main.go:37` — bump `Version` to `"1.7.61"`
- `CHANGELOG.md` — new v1.7.61 entry
- `README.md:331-508` — document `session remove` in the session subcommand list

**Non-goals (out of scope):**
- Touching the existing `"d"` keybind or `deleteSession()` path
- Remote session removal (`ConfirmDeleteRemoteSession` path stays unchanged)
- Group-level bulk remove

---

## Key Contracts (lock these now)

**CLI exit codes** (mirror `session stop`):
- `0` — success
- `1` — validation/operation error (`ErrCodeInvalidOperation`)
- `2` — session not found (`ErrCodeNotFound`)

**Status gating** (enforce in CLI + TUI):
- Remove allowed without `--force` when `inst.Status` is `StatusStopped` or `StatusError`
- Remove with `--force` allowed in any state (documented as destructive)
- `--all-errored` iterates instances where `inst.Status == StatusError`

**Worktree handling:**
- Default: **registry-only** — do NOT call `inst.Kill()`, do NOT touch `git.RemoveWorktree`
- `--prune-worktree` flag: call `inst.Kill()` (best-effort) + `git.RemoveWorktree` + `git.PruneWorktrees` before `DeleteInstance`. Warn in help text.

**Transcript preservation:**
- Never touch `~/.claude/projects/<slug>/` — this is the invariant the `TestSessionRemove_PreservesTranscripts` test guards.

**TUI status-gating in the UI layer:**
- `X` on stopped/error session → `ShowRemoveSession` dialog
- `X` on any other status → `h.setError("session must be stopped or errored; use 'd' for destructive delete")` (no dialog)
- `Ctrl+X` → count errored sessions; if 0, `setError("no errored sessions")`; else show bulk dialog with count

---

## Task 1: Save plan + create failing CLI test scaffolding

**Files:**
- Create: `cmd/agent-deck/session_remove_cmd_test.go`

- [ ] **Step 1: Write the failing CLI happy-path test**

Create `cmd/agent-deck/session_remove_cmd_test.go`:

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addTestSession is a thin wrapper to add a session for remove-tests.
// Mirrors sessionMoveAddSession but with configurable title + path.
func addTestSession(t *testing.T, home, workPath, title string) string {
	t.Helper()
	if err := os.MkdirAll(workPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"add",
		"-t", title,
		"-c", "claude",
		"--no-parent",
		"--json",
		workPath,
	)
	if code != 0 {
		t.Fatalf("agent-deck add failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned empty id")
	}
	return resp.ID
}

// forceSetStatus opens storage directly (under the isolated HOME) and
// writes a target status onto the named instance. We can't use
// `agent-deck session set` because `set` doesn't accept `status` as a
// field (see handleSessionSet validFields map). Direct storage mutation
// is the standard test pattern for driving the registry into a specific
// state.
func forceSetStatus(t *testing.T, home, id string, status session.Status) {
	t.Helper()
	prev := os.Getenv("HOME")
	t.Setenv("HOME", home)
	defer os.Setenv("HOME", prev)

	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("open storage: %v", err)
	}
	instances, groups, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	var target *session.Instance
	for _, inst := range instances {
		if inst.ID == id {
			target = inst
			break
		}
	}
	if target == nil {
		t.Fatalf("instance %s not found", id)
	}
	target.Status = status
	tree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("save: %v", err)
	}
}

// TestSessionRemove_StoppedSessionSucceeds is the happy path: add a session,
// mark it stopped, then `session remove <id>` removes it from the registry.
func TestSessionRemove_StoppedSessionSucceeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-basic")
	forceSetStatus(t, home, id, session.StatusStopped)

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "remove", id, "--json",
	)
	if code != 0 {
		t.Fatalf("session remove failed (exit %d)\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("session %s still present after remove; list:\n%s", id, listJSON)
	}
}
```

- [ ] **Step 2: Run the test — verify it fails**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run TestSessionRemove_StoppedSessionSucceeds -v -count=1`
Expected: FAIL with `unknown session command: remove`.

- [ ] **Step 3: Commit the failing test (red)**

```bash
git add cmd/agent-deck/session_remove_cmd_test.go docs/superpowers/plans/2026-04-22-session-remove.md
git commit -m "test(session-remove): red — happy path for 'session remove <id>'"
```

---

## Task 2: Add remaining failing CLI tests

**Files:**
- Modify: `cmd/agent-deck/session_remove_cmd_test.go`

- [ ] **Step 1: Add running-session-requires-force test**

Append to the test file:

```go
// TestSessionRemove_RunningWithoutForce_Rejected enforces the safety gate.
func TestSessionRemove_RunningWithoutForce_Rejected(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-running")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code == 0 {
		t.Fatalf("expected non-zero exit for running-without-force; stdout=%s stderr=%s", stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, id) {
		t.Errorf("running session was removed without --force; list:\n%s", listJSON)
	}
}

// TestSessionRemove_RunningWithForce_Succeeds confirms --force bypasses the gate.
func TestSessionRemove_RunningWithForce_Succeeds(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-forced")
	forceSetStatus(t, home, id, session.StatusRunning)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--force", "--json")
	if code != 0 {
		t.Fatalf("--force remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, id) {
		t.Errorf("forced remove did not take effect; list:\n%s", listJSON)
	}
}

// TestSessionRemove_AllErrored_RemovesOnlyErrored confirms the bulk path
// respects status filtering — stopped/idle/running are NOT touched.
func TestSessionRemove_AllErrored_RemovesOnlyErrored(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	errID := addTestSession(t, home, filepath.Join(home, "err-proj"), "err-one")
	forceSetStatus(t, home, errID, session.StatusError)
	stoppedID := addTestSession(t, home, filepath.Join(home, "stop-proj"), "stopped-one")
	forceSetStatus(t, home, stoppedID, session.StatusStopped)
	idleID := addTestSession(t, home, filepath.Join(home, "idle-proj"), "idle-one")
	forceSetStatus(t, home, idleID, session.StatusIdle)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, errID) {
		t.Errorf("errored session was NOT removed; list:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, stoppedID) {
		t.Errorf("stopped session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
	if !strings.Contains(listJSON, idleID) {
		t.Errorf("idle session got removed by --all-errored (over-broad); list:\n%s", listJSON)
	}
}

// TestSessionRemove_PreservesTranscripts is the hard invariant: registry
// removal must NOT touch ~/.claude/projects/<slug>/.
func TestSessionRemove_PreservesTranscripts(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	workPath := filepath.Join(home, "proj")
	id := addTestSession(t, home, workPath, "remove-transcript")
	forceSetStatus(t, home, id, session.StatusStopped)

	// Seed the Claude transcript dir with a sentinel file.
	transcriptDir := seedClaudeProjectDir(t, home, workPath, "sentinel-transcript")
	sentinelPath := filepath.Join(transcriptDir, "abc-123.jsonl")

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", id, "--json")
	if code != 0 {
		t.Fatalf("remove failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Errorf("transcript sentinel missing after remove: %v", err)
	}
}

// TestSessionRemove_NotFound_Exit2 mirrors `session stop`'s convention.
func TestSessionRemove_NotFound_Exit2(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "does-not-exist", "--json")
	if code != 2 {
		t.Fatalf("expected exit 2 for not-found, got %d; stdout=%s stderr=%s", code, stdout, stderr)
	}
}
```

- [ ] **Step 2: Run full remove-test suite — all fail**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run TestSessionRemove_ -v -count=1`
Expected: all 5 tests fail with `unknown session command: remove`.

- [ ] **Step 3: Commit**

```bash
git add cmd/agent-deck/session_remove_cmd_test.go
git commit -m "test(session-remove): red — force, bulk, transcript-preserve, not-found"
```

---

## Task 3: Implement handleSessionRemove (green)

**Files:**
- Create: `cmd/agent-deck/session_remove_cmd.go`
- Modify: `cmd/agent-deck/session_cmd.go:29-68`

- [ ] **Step 1: Create the new handler file**

Create `cmd/agent-deck/session_remove_cmd.go`:

```go
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/asheshgoplani/agent-deck/internal/git"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleSessionRemove deletes a session from the registry.
//
// By default only sessions in stopped/error state may be removed; --force
// bypasses the gate. --all-errored removes every session in error state.
// --prune-worktree additionally kills the tmux process and removes any git
// worktree associated with the session (registry-only by default).
//
// Transcripts under ~/.claude/projects/<slug>/ are NEVER touched.
func handleSessionRemove(profile string, args []string) {
	fs := flag.NewFlagSet("session remove", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	force := fs.Bool("force", false, "Remove even when the session is running/waiting/idle (destructive)")
	allErrored := fs.Bool("all-errored", false, "Remove every session currently in the 'error' state (bulk)")
	pruneWorktree := fs.Bool("prune-worktree", false, "Also kill the process and remove any git worktree (destructive)")

	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session remove <id|title> [options]")
		fmt.Println("       agent-deck session remove --all-errored [options]")
		fmt.Println()
		fmt.Println("Remove a session from the registry. By default only stopped or")
		fmt.Println("errored sessions may be removed; use --force to bypass.")
		fmt.Println()
		fmt.Println("This is registry-only by default: Claude transcripts under")
		fmt.Println("~/.claude/projects/ are preserved. Pass --prune-worktree to also")
		fmt.Println("kill the process and delete the git worktree (destructive).")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}

	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}

	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	storage, instances, groups, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}

	if *allErrored {
		removeAllErrored(out, storage, instances, groups, *pruneWorktree)
		return
	}

	identifier := fs.Arg(0)
	if identifier == "" {
		out.Error("usage: session remove <id|title> OR --all-errored", ErrCodeInvalidOperation)
		os.Exit(1)
	}

	inst, errMsg, errCode := ResolveSession(identifier, instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			os.Exit(2)
		}
		os.Exit(1)
		return
	}

	if !*force && !isRemovableStatus(inst.Status) {
		out.Error(
			fmt.Sprintf("session '%s' is in state '%s'; only stopped/error sessions may be removed without --force", inst.Title, inst.Status),
			ErrCodeInvalidOperation,
		)
		os.Exit(1)
	}

	if *pruneWorktree {
		pruneSessionWorktree(inst)
	}

	if err := storage.DeleteInstance(inst.ID); err != nil {
		out.Error(fmt.Sprintf("failed to remove session: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	// Drop the instance from the in-memory list, then persist groups so
	// saveSessionData doesn't re-add a reference via the group graph.
	instances = dropInstance(instances, inst.ID)
	if err := saveSessionData(storage, instances, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}

	out.Success(fmt.Sprintf("Removed session: %s", inst.Title), map[string]interface{}{
		"success": true,
		"id":      inst.ID,
		"title":   inst.Title,
	})
}

// isRemovableStatus returns true for states where a session can be removed
// from the registry without --force.
func isRemovableStatus(s session.Status) bool {
	return s == session.StatusStopped || s == session.StatusError
}

// removeAllErrored implements the --all-errored bulk path.
func removeAllErrored(
	out *CLIOutput,
	storage *session.Storage,
	instances []*session.Instance,
	groups []*session.GroupData,
	pruneWorktree bool,
) {
	var removed []map[string]interface{}
	remaining := instances[:0]
	for _, inst := range instances {
		if inst.Status == session.StatusError {
			if pruneWorktree {
				pruneSessionWorktree(inst)
			}
			if err := storage.DeleteInstance(inst.ID); err != nil {
				out.Error(fmt.Sprintf("failed to remove session %s: %v", inst.ID, err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			removed = append(removed, map[string]interface{}{"id": inst.ID, "title": inst.Title})
			continue
		}
		remaining = append(remaining, inst)
	}
	if err := saveSessionData(storage, remaining, groups); err != nil {
		out.Error(fmt.Sprintf("failed to save session state: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	out.Success(fmt.Sprintf("Removed %d errored session(s)", len(removed)), map[string]interface{}{
		"success": true,
		"count":   len(removed),
		"removed": removed,
	})
}

// pruneSessionWorktree kills the session and removes its git worktree (if any).
// Errors are logged to stderr but never block the remove — the caller
// still persists the registry delete.
func pruneSessionWorktree(inst *session.Instance) {
	_ = inst.Kill() // best-effort — a dead session returns an error we ignore
	if inst.IsWorktree() {
		if err := git.RemoveWorktree(inst.WorktreeRepoRoot, inst.WorktreePath, true); err != nil {
			fmt.Fprintf(os.Stderr, "warn: worktree remove failed for %s: %v\n", inst.ID, err)
		}
		_ = git.PruneWorktrees(inst.WorktreeRepoRoot)
	}
}

// dropInstance returns a new slice with the given id filtered out.
func dropInstance(instances []*session.Instance, id string) []*session.Instance {
	out := instances[:0]
	for _, i := range instances {
		if i.ID != id {
			out = append(out, i)
		}
	}
	return out
}
```

- [ ] **Step 2: Wire the dispatcher**

Edit `cmd/agent-deck/session_cmd.go`. Insert after the `"stop"` case (line 33):

```go
		case "remove":
			handleSessionRemove(profile, args[1:])
```

And add to `printSessionHelp` (after the `stop` line at ~line 79):

```go
		fmt.Println("  remove <id>             Remove a session from the registry (stopped/error only; --force to bypass)")
```

- [ ] **Step 3: Build and run all remove tests**

Run:
```bash
GOTOOLCHAIN=go1.24.0 go build ./...
GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -run TestSessionRemove_ -v -count=1
```
Expected: all 5 tests PASS.

- [ ] **Step 4: Run the full cmd/agent-deck test suite — no regressions**

Run: `GOTOOLCHAIN=go1.24.0 go test ./cmd/agent-deck/ -race -count=1`
Expected: PASS.

- [ ] **Step 5: Commit (green)**

```bash
git add cmd/agent-deck/session_remove_cmd.go cmd/agent-deck/session_cmd.go
git commit -m "feat(session-remove): CLI subcommand with --force, --all-errored, --prune-worktree"
```

---

## Task 4: Failing TUI Seam A tests

**Files:**
- Create: `internal/ui/session_remove_tui_test.go`

- [ ] **Step 1: Write the Seam A tests**

Create `internal/ui/session_remove_tui_test.go`:

```go
package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// newSessionItem builds a flatItems entry for Seam A tests with a given status.
func newSessionItem(id, title string, status session.Status) session.Item {
	return session.Item{
		Type: session.ItemTypeSession,
		Session: &session.Instance{
			ID:     id,
			Title:  title,
			Status: status,
		},
	}
}

// TestSessionRemoveTUI_CapitalX_OnStopped_OpensConfirm asserts that pressing
// 'X' over a stopped session opens the remove-confirm dialog (not the
// existing 'd' destructive-delete dialog).
func TestSessionRemoveTUI_CapitalX_OnStopped_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newSessionItem("id-1", "stopped-one", session.StatusStopped)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on stopped session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveSession {
		t.Fatalf("expected ConfirmRemoveSession, got %v", got.confirmDialog.GetConfirmType())
	}
	if got.confirmDialog.GetTargetID() != "id-1" {
		t.Fatalf("expected targetID 'id-1', got %q", got.confirmDialog.GetTargetID())
	}
}

// TestSessionRemoveTUI_CapitalX_OnErrored_OpensConfirm — error state also qualifies.
func TestSessionRemoveTUI_CapitalX_OnErrored_OpensConfirm(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newSessionItem("id-err", "err-one", session.StatusError)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after 'X' on errored session")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmRemoveSession {
		t.Fatalf("expected ConfirmRemoveSession, got %v", got.confirmDialog.GetConfirmType())
	}
}

// TestSessionRemoveTUI_CapitalX_OnRunning_ShowsError — safety gate in the UI.
func TestSessionRemoveTUI_CapitalX_OnRunning_ShowsError(t *testing.T) {
	h := newSeamATestHome()
	h.flatItems = []session.Item{newSessionItem("id-run", "running-one", session.StatusRunning)}
	h.cursor = 0

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should NOT open for a running session")
	}
	if got.err == nil {
		t.Fatalf("expected an error message steering user to 'd' for destructive delete")
	}
}

// TestSessionRemoveTUI_CtrlX_OpensBulkConfirmWithCount asserts Ctrl+X routes
// to the bulk-errored dialog and passes the correct count.
func TestSessionRemoveTUI_CtrlX_OpensBulkConfirmWithCount(t *testing.T) {
	h := newSeamATestHome()
	h.instances = []*session.Instance{
		{ID: "e1", Title: "err-1", Status: session.StatusError},
		{ID: "e2", Title: "err-2", Status: session.StatusError},
		{ID: "ok", Title: "running", Status: session.StatusRunning},
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := newModel.(*Home)

	if !got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should be visible after Ctrl+X")
	}
	if got.confirmDialog.GetConfirmType() != ConfirmBulkRemoveErrored {
		t.Fatalf("expected ConfirmBulkRemoveErrored, got %v", got.confirmDialog.GetConfirmType())
	}
	// mcpCount is reused by the dialog as a generic integer carrier for the bulk count.
	if got.confirmDialog.mcpCount != 2 {
		t.Fatalf("expected bulk count 2, got %d", got.confirmDialog.mcpCount)
	}
}

// TestSessionRemoveTUI_CtrlX_NoErrored_ShowsError — empty-set guard.
func TestSessionRemoveTUI_CtrlX_NoErrored_ShowsError(t *testing.T) {
	h := newSeamATestHome()
	h.instances = []*session.Instance{
		{ID: "ok", Title: "idle-one", Status: session.StatusIdle},
	}

	newModel, _ := h.Update(tea.KeyMsg{Type: tea.KeyCtrlX})
	got := newModel.(*Home)

	if got.confirmDialog.IsVisible() {
		t.Fatalf("confirm dialog should NOT open when there are no errored sessions")
	}
	if got.err == nil {
		t.Fatalf("expected an error message when no errored sessions exist")
	}
}
```

- [ ] **Step 2: Run tests — verify fail**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/ -run TestSessionRemoveTUI_ -v -count=1`
Expected: FAIL — symbols `ConfirmRemoveSession` and `ConfirmBulkRemoveErrored` don't exist; `KeyCtrlX` press is a no-op.

- [ ] **Step 3: Commit (red)**

```bash
git add internal/ui/session_remove_tui_test.go
git commit -m "test(session-remove): red — TUI Seam A for X + Ctrl+X status-gated remove"
```

---

## Task 5: TUI implementation (green)

**Files:**
- Modify: `internal/ui/confirm_dialog.go:16-24` and the View switch
- Modify: `internal/ui/home.go` keybind switch + confirmAction + add handlers

- [ ] **Step 1: Extend ConfirmType enum**

Edit `internal/ui/confirm_dialog.go:15-24`:

```go
const (
	ConfirmDeleteSession ConfirmType = iota
	ConfirmCloseSession
	ConfirmDeleteGroup
	ConfirmQuitWithPool
	ConfirmCreateDirectory
	ConfirmInstallHooks
	ConfirmDeleteRemoteSession
	ConfirmCloseRemoteSession
	ConfirmRemoveSession      // NEW: status-gated registry-only remove (TUI 'X')
	ConfirmBulkRemoveErrored  // NEW: bulk remove of all errored sessions (TUI Ctrl+X)
)
```

- [ ] **Step 2: Add Show methods**

Append after `ShowCloseRemoteSession` (around line 106):

```go
// ShowRemoveSession shows confirmation for status-gated registry removal (TUI 'X').
// This is safer than ConfirmDeleteSession: the caller has already verified
// the session is stopped or errored, and the dialog wording reflects the
// registry-only intent (transcripts + worktrees are preserved).
func (c *ConfirmDialog) ShowRemoveSession(sessionID string, sessionName string) {
	c.visible = true
	c.confirmType = ConfirmRemoveSession
	c.targetID = sessionID
	c.targetName = sessionName
	c.buttonCount = 2
	c.focusedButton = 1 // default to Cancel
}

// ShowBulkRemoveErrored shows confirmation for removing all errored sessions
// (TUI Ctrl+X). count is the number of errored sessions that will be removed.
func (c *ConfirmDialog) ShowBulkRemoveErrored(count int) {
	c.visible = true
	c.confirmType = ConfirmBulkRemoveErrored
	c.targetID = ""
	c.targetName = ""
	c.mcpCount = count // reuse mcpCount as a generic integer carrier
	c.buttonCount = 2
	c.focusedButton = 1
}
```

- [ ] **Step 3: Add View cases**

Edit the switch in `View()` at `confirm_dialog.go:261`, before the closing brace (before `ConfirmCreateDirectory`'s case — ordering matches enum):

```go
	case ConfirmRemoveSession:
		title = "Remove Session?"
		warning = fmt.Sprintf("Remove this session from the registry:\n\n  \"%s\"", c.targetName)
		details = "• The session record will be deleted from agent-deck\n• Claude transcripts (~/.claude/projects/) are preserved\n• Git worktrees are preserved (use 'd' to destroy them)"
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Remove", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y remove · n cancel · ←/→ navigate · Enter select · Esc"))

	case ConfirmBulkRemoveErrored:
		title = "Remove All Errored Sessions?"
		warning = fmt.Sprintf("Remove %d errored session(s) from the registry.", c.mcpCount)
		details = "• Only sessions currently in the 'error' state are affected\n• Claude transcripts are preserved\n• Git worktrees are preserved"
		borderColor = ColorYellow
		buttonRow := lipgloss.JoinHorizontal(lipgloss.Center,
			renderButton("Remove All", ColorYellow, c.focusedButton == 0), "  ",
			renderButton("Cancel", ColorAccent, c.focusedButton == 1))
		buttons = lipgloss.JoinVertical(lipgloss.Left, buttonRow,
			hintStyle.Render("y remove · n cancel · ←/→ navigate · Enter select · Esc"))
```

- [ ] **Step 4: Add the TUI keybinds**

In `internal/ui/home.go`, insert in the main key switch (near `case "d":` around line 6085):

```go
	case "X":
		// Status-gated registry-only remove. For stopped/errored sessions only;
		// use 'd' for destructive delete (kills process + removes worktree).
		if h.cursor < len(h.flatItems) {
			item := h.flatItems[h.cursor]
			if item.Type == session.ItemTypeSession && item.Session != nil {
				status := item.Session.Status
				if status == session.StatusStopped || status == session.StatusError {
					h.confirmDialog.ShowRemoveSession(item.Session.ID, item.Session.Title)
				} else {
					h.setError(fmt.Errorf("session must be stopped or errored to remove; use 'd' to destructively delete a %s session", status))
				}
			}
		}
		return h, nil

	case "ctrl+x":
		// Bulk remove all errored sessions from the registry.
		count := 0
		h.instancesMu.RLock()
		for _, inst := range h.instances {
			if inst.Status == session.StatusError {
				count++
			}
		}
		h.instancesMu.RUnlock()
		if count == 0 {
			h.setError(fmt.Errorf("no errored sessions to remove"))
			return h, nil
		}
		h.confirmDialog.ShowBulkRemoveErrored(count)
		return h, nil
```

- [ ] **Step 5: Wire confirmAction cases**

In `confirmAction()` (around `home.go:6532`), add before the trailing `h.confirmDialog.Hide()`:

```go
	case ConfirmRemoveSession:
		sessionID := h.confirmDialog.GetTargetID()
		if inst := h.getInstanceByID(sessionID); inst != nil {
			h.confirmDialog.Hide()
			return h.removeSession(inst)
		}
	case ConfirmBulkRemoveErrored:
		h.confirmDialog.Hide()
		return h.bulkRemoveErrored()
```

- [ ] **Step 6: Add the handler methods**

Append near `deleteSession` (after `closeSession`, around `home.go:8073`):

```go
// removeSession removes a session from the registry without killing the
// process or cleaning its worktree. Safe to call on any state but the
// caller (key handler) enforces the stopped/error gate.
func (h *Home) removeSession(inst *session.Instance) tea.Cmd {
	id := inst.ID
	return func() tea.Msg {
		return sessionDeletedMsg{deletedID: id}
	}
}

// bulkRemoveErrored removes every session currently in the 'error' state.
// Emits a single sessionDeletedMsg per removed session — Update is
// idempotent on repeated deletedIDs.
func (h *Home) bulkRemoveErrored() tea.Cmd {
	h.instancesMu.RLock()
	ids := make([]string, 0, len(h.instances))
	for _, inst := range h.instances {
		if inst.Status == session.StatusError {
			ids = append(ids, inst.ID)
		}
	}
	h.instancesMu.RUnlock()

	cmds := make([]tea.Cmd, 0, len(ids))
	for _, id := range ids {
		id := id
		cmds = append(cmds, func() tea.Msg { return sessionDeletedMsg{deletedID: id} })
	}
	return tea.Batch(cmds...)
}
```

- [ ] **Step 7: Run TUI tests — they pass now**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/ -run TestSessionRemoveTUI_ -v -count=1`
Expected: PASS.

- [ ] **Step 8: Run full TUI + confirm_dialog suite — no regressions**

Run: `GOTOOLCHAIN=go1.24.0 go test ./internal/ui/ -race -count=1`
Expected: PASS.

- [ ] **Step 9: Commit**

```bash
git add internal/ui/confirm_dialog.go internal/ui/home.go
git commit -m "feat(session-remove): TUI X/Ctrl+X keybinds with status-gated confirm"
```

---

## Task 6: Docs + version bump + persistence mandate

**Files:**
- Modify: `cmd/agent-deck/main.go:37`
- Modify: `CHANGELOG.md` (prepend new entry)
- Modify: `README.md:331-508` (session subcommand list)

- [ ] **Step 1: Bump version**

Edit `cmd/agent-deck/main.go` at line 37:

```go
var Version = "1.7.61"
```

- [ ] **Step 2: CHANGELOG entry**

Prepend to `CHANGELOG.md` after the top-level heading:

```markdown
## [1.7.61] - 2026-04-22

### Added
- `agent-deck session remove <id|title>` CLI subcommand — removes a session from the registry. Only stopped/errored sessions are removable by default; `--force` bypasses the gate.
- `agent-deck session remove --all-errored` — bulk-remove every session currently in the `error` state. Respects status filtering (stopped, idle, running sessions are untouched).
- `--prune-worktree` flag — opt-in destructive variant that also kills the tmux process and removes any git worktree.
- TUI `X` keybind (Home view) — status-gated remove with confirmation dialog; rejects non-stopped/non-errored sessions with a message steering the user to `d` for destructive delete.
- TUI `Ctrl+X` keybind — bulk remove of all errored sessions with a confirmation dialog that shows the count.

### Preserved
- Claude transcripts under `~/.claude/projects/` are never touched by `remove`. The `d` keybind and `deleteSession` path are unchanged.
```

- [ ] **Step 3: README update**

In `README.md` under the `agent-deck session` section (between `stop` and `restart`), add:

```markdown
- `agent-deck session remove <id>` — Remove a session from the registry. Only stopped/errored sessions are removable; use `--force` to bypass. Registry-only: Claude transcripts and git worktrees are preserved. Pass `--prune-worktree` to destructively clean the worktree too.
- `agent-deck session remove --all-errored` — Bulk remove every session in the `error` state.
```

And in the keybinds section (search for `d ` session delete entry) add:

```markdown
- `X` — Remove session from registry (stopped/errored only). Transcripts + worktrees preserved.
- `Ctrl+X` — Bulk remove all errored sessions.
```

- [ ] **Step 4: Run the full persistence mandate suite**

Per `CLAUDE.md` this PR touches `cmd/session_cmd.go`, which is on the persistence mandate path. Run:

```bash
GOTOOLCHAIN=go1.24.0 go test -run TestPersistence_ ./internal/session/... -race -count=1
```
Expected: PASS (no regressions from dispatcher change).

If running on Linux+systemd (which this worktree is), also run:

```bash
bash scripts/verify-session-persistence.sh
```
Expected: PASS.

- [ ] **Step 5: Run the full test suite**

```bash
GOTOOLCHAIN=go1.24.0 go build ./...
GOTOOLCHAIN=go1.24.0 go test ./... -race -count=1 -timeout 300s
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add CHANGELOG.md README.md cmd/agent-deck/main.go
git commit -m "docs(session-remove): CHANGELOG v1.7.61 + README + version bump"
```

- [ ] **Step 7: Final sanity — verify no --no-verify commits**

```bash
git log main..HEAD --pretty=format:'%h %s'
```
All six commits should be present with normal messages, no attribution lines. Do NOT push; do NOT tag. Those are explicit user actions.

---

## Self-Review

**Spec coverage:**
- Scope 1 — `session remove <id>` + `--force` — Task 1, 2, 3 ✓
- Scope 2 — `--all-errored` — Task 2 (test), Task 3 (impl) ✓
- Scope 3 — TUI `X` and `Ctrl+X` with confirm — Task 4, 5 ✓
- Scope 4 — help text + destructive warnings + transcript preservation — Task 3 (flag usage) + Task 4 (TestSessionRemove_PreservesTranscripts) + Task 6 (README/CHANGELOG) ✓
- TDD — red commits land before green in every task ✓
- eval-harness — Seam A tests are the model-level eval seam per `TUI_TESTS.md` ✓
- CHANGELOG + README — Task 6 ✓
- v1.7.61 — Task 6 ✓
- No `--no-verify`, no force-push, no `--admin` — final step guards ✓

**Placeholder scan:** none. Every step has concrete code or exact commands.

**Type consistency:** `ConfirmRemoveSession` / `ConfirmBulkRemoveErrored` / `ShowRemoveSession` / `ShowBulkRemoveErrored` / `removeSession` / `bulkRemoveErrored` named consistently across Tasks 4 and 5. `session.StatusStopped` / `session.StatusError` match the enum at `internal/session/instance.go:47-52`.

**Risks called out:**
- `dropInstance` uses in-place slice reuse — fine because the caller discards the old slice. If that changes, the in-place mutation is a bug.
- The TUI `removeSession` command returns a `sessionDeletedMsg` without actually calling `storage.DeleteInstance` — it relies on the existing `Update` handler at `home.go:3747` to do the persistence. This is intentional (mirrors the existing delete flow) but callers must not bypass `Update`.
