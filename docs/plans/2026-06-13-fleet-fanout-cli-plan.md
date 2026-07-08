# Fleet fan-out CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a parent agent-deck session fan out N child sessions and, non-blockingly, read each child's status + last completion (ok/fail + summary) on demand from any chat.

**Architecture:** Three changes in the existing Go CLI. (1) A new durable, non-destructive **completion ledger** file-store (mirroring the proven `CompletionRecord` pattern but in its own directory, so it does not collide with the task-worker claim-guard semantics) written at both the interactive done-signal emit site and the task-worker path. (2) A read-only `session children` subcommand that merges live status with ledger entries. (3) A `launch --assert-done` flag (default-on for Claude) that appends the completion-sentinel instruction to the child's initial message.

**Tech Stack:** Go 1.24, standard library only. Tests via `go test`. Spec: `docs/superpowers/specs/2026-06-13-fleet-fanout-cli-design.md`.

**Design refinement vs spec:** The spec said "persist completion on the session record." Implementation uses a dedicated ledger file-store instead of new `Instance` SQLite columns — lower risk (no schema migration) and avoids overloading the existing `completions/` claim store, whose presence makes the daemon stand down (`transition_daemon.go:311`). Same observable behavior: completion becomes a non-destructively queryable property.

---

## File Structure

- Create: `internal/session/completion_ledger.go` — ledger entry type + atomic write / non-destructive read / profile load.
- Create: `internal/session/completion_ledger_test.go` — ledger unit tests.
- Modify: `internal/session/transition_daemon.go:340-344` — write ledger entry at the interactive emit site.
- Modify: `internal/session/taskworker.go:246-250` — write ledger entry for the task-worker path.
- Modify: `cmd/agent-deck/session_cmd.go` — add `children` subcommand + dispatch + help.
- Create: `cmd/agent-deck/session_children_test.go` — children-command filtering test.
- Modify: `cmd/agent-deck/launch_cmd.go` — `--assert-done` / `--no-assert-done` flag + message append.
- Create: `cmd/agent-deck/launch_assert_done_test.go` — assert-done append unit test.

---

### Task 1: Completion ledger store

**Files:**
- Create: `internal/session/completion_ledger.go`
- Test: `internal/session/completion_ledger_test.go`

- [ ] **Step 1: Write the failing test**

```go
package session

import "testing"

func TestCompletionLedgerWriteReadLastWins(t *testing.T) {
	t.Setenv("AGENT_DECK_TEST_PROFILE", "ledgertest")
	if _, ok := ReadLedgerEntry("child-1"); ok {
		t.Fatalf("expected no entry before write")
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "child-1", Profile: "p", Title: "T", Status: "ok", Summary: "first"}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "child-1", Profile: "p", Title: "T", Status: "fail", Summary: "second"}); err != nil {
		t.Fatalf("write 2: %v", err)
	}
	got, ok := ReadLedgerEntry("child-1")
	if !ok {
		t.Fatalf("expected entry after write")
	}
	if got.Status != "fail" || got.Summary != "second" {
		t.Fatalf("last-wins failed: got %+v", got)
	}
}

func TestCompletionLedgerWriteRejectsEmptyID(t *testing.T) {
	if err := WriteLedgerEntry(CompletionLedgerEntry{ChildID: "  "}); err == nil {
		t.Fatalf("expected error on empty child id")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./internal/session/ -run TestCompletionLedger -v`
Expected: FAIL — undefined `CompletionLedgerEntry`, `WriteLedgerEntry`, `ReadLedgerEntry`.

- [ ] **Step 3: Write minimal implementation**

Mirror the existing `taskworker.go` record store. Reuse `safeRecordName` (same package) and `runtimeDataPath`.

```go
package session

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// CompletionLedgerEntry is the durable, non-destructive last-known completion
// for a child session. Unlike the task-worker CompletionRecord (whose presence
// makes the daemon stand down from poll-inference), the ledger is purely
// informational: it records the most recent asserted completion so a parent can
// query "which of my fleet finished" without consuming any delivery event.
// Last-wins per child.
type CompletionLedgerEntry struct {
	ChildID    string    `json:"child_id"`
	Profile    string    `json:"profile"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status"` // "ok" | "fail"
	Summary    string    `json:"summary,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

func completionLedgerDir() (string, error) {
	return runtimeDataPath("completion-ledger")
}

func completionLedgerPath(childID string) (string, error) {
	dir, err := completionLedgerDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safeRecordName(childID)+".json"), nil
}

// WriteLedgerEntry persists an entry atomically (tmp + rename), last-wins.
func WriteLedgerEntry(e CompletionLedgerEntry) error {
	if strings.TrimSpace(e.ChildID) == "" {
		return errors.New("completion ledger: empty child id")
	}
	if e.FinishedAt.IsZero() {
		e.FinishedAt = time.Now()
	}
	path, err := completionLedgerPath(e.ChildID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadLedgerEntry returns the last-known completion for a child, if any. Read
// is non-destructive.
func ReadLedgerEntry(childID string) (CompletionLedgerEntry, bool) {
	path, err := completionLedgerPath(childID)
	if err != nil {
		return CompletionLedgerEntry{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return CompletionLedgerEntry{}, false
	}
	var e CompletionLedgerEntry
	if err := json.Unmarshal(data, &e); err != nil {
		return CompletionLedgerEntry{}, false
	}
	return e, true
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./internal/session/ -run TestCompletionLedger -v`
Expected: PASS. (If `runtimeDataPath` is not isolated by `AGENT_DECK_TEST_PROFILE`, the test still passes because it writes/reads the same real dir; the assertions are self-contained for `child-1`. Verify it does not error.)

- [ ] **Step 5: Commit**

```bash
git add internal/session/completion_ledger.go internal/session/completion_ledger_test.go
git commit -m "feat(session): add non-destructive completion ledger store"
```

---

### Task 2: Write ledger at the interactive emit site

**Files:**
- Modify: `internal/session/transition_daemon.go` (inside `emitDoneSignals`, right after `d.lastDone[profile][id] = sig`)

- [ ] **Step 1: Add the ledger write**

After the existing line `d.lastDone[profile][id] = sig` (around line 343), append:

```go
		_ = WriteLedgerEntry(CompletionLedgerEntry{
			ChildID:    id,
			Profile:    profile,
			Title:      inst.Title,
			Status:     sig.Status,
			Summary:    sig.Summary,
			FinishedAt: hs.UpdatedAt,
		})
```

Rationale: this is the interactive (persistent-session) done path. The write is best-effort (`_ =`) — a ledger failure must never block the existing notification.

- [ ] **Step 2: Build**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go build ./...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add internal/session/transition_daemon.go
git commit -m "feat(session): record interactive completions to the ledger"
```

---

### Task 3: Write ledger for the task-worker path

**Files:**
- Modify: `internal/session/taskworker.go` (inside `RunTaskWorker`, after the finalized record is written ~line 247)

- [ ] **Step 1: Add the ledger write**

In `RunTaskWorker`, after `if err := WriteCompletionRecord(rec); err != nil { return rec, err }` and before `return rec, nil`, add:

```go
	if strings.TrimSpace(rec.Status) != "" {
		_ = WriteLedgerEntry(CompletionLedgerEntry{
			ChildID:    rec.ChildID,
			Profile:    rec.Profile,
			Title:      rec.Title,
			Status:     rec.Status,
			Summary:    rec.Summary,
			FinishedAt: rec.FinishedAt,
		})
	}
```

(`strings` is already imported in taskworker.go.)

- [ ] **Step 2: Build**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go build ./...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add internal/session/taskworker.go
git commit -m "feat(session): record task-worker completions to the ledger"
```

---

### Task 4: `session children` subcommand

**Files:**
- Modify: `cmd/agent-deck/session_cmd.go` (dispatch switch ~line 84, new handler, help text)
- Test: `cmd/agent-deck/session_children_test.go`

- [ ] **Step 1: Write the failing test (pure filtering helper)**

Add a small pure helper `childrenOf(parentID, instances)` so filtering is unit-testable without a live registry.

```go
package main

import (
	"testing"

	"github.com/<module>/internal/session" // replace with actual module path from go.mod
)

func TestChildrenOfFiltersByParent(t *testing.T) {
	a := &session.Instance{ID: "p"}
	b := &session.Instance{ID: "c1", ParentSessionID: "p"}
	c := &session.Instance{ID: "c2", ParentSessionID: "other"}
	d := &session.Instance{ID: "c3", ParentSessionID: "p"}
	got := childrenOf("p", []*session.Instance{a, b, c, d})
	if len(got) != 2 {
		t.Fatalf("expected 2 children, got %d", len(got))
	}
	if got[0].ID != "c1" || got[1].ID != "c3" {
		t.Fatalf("unexpected children: %v %v", got[0].ID, got[1].ID)
	}
}
```

Confirm the module path first: `head -1 go.mod`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./cmd/agent-deck/ -run TestChildrenOf -v`
Expected: FAIL — undefined `childrenOf`.

- [ ] **Step 3: Implement helper + handler + dispatch**

Add to `session_cmd.go`:

```go
func childrenOf(parentID string, instances []*session.Instance) []*session.Instance {
	var out []*session.Instance
	for _, inst := range instances {
		if inst != nil && inst.ParentSessionID == parentID {
			out = append(out, inst)
		}
	}
	return out
}

func handleSessionChildren(profile string, args []string) {
	fs := flag.NewFlagSet("session children", flag.ExitOnError)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	quiet := fs.Bool("quiet", false, "Minimal output")
	quietShort := fs.Bool("q", false, "Minimal output (short)")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session children [id|title] [options]")
		fmt.Println()
		fmt.Println("List a session's sub-sessions with live status and last completion.")
		fmt.Println("Defaults to the current session. Read-only; does not clear the inbox.")
		fmt.Println()
		fmt.Println("Options:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		os.Exit(1)
	}
	identifier := fs.Arg(0)
	quietMode := *quiet || *quietShort
	out := NewCLIOutput(*jsonOutput, quietMode)

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		os.Exit(1)
	}
	parent, errMsg, errCode := ResolveSessionOrCurrent(identifier, instances)
	if parent == nil {
		out.Error(errMsg, errCode)
		os.Exit(2)
	}

	kids := childrenOf(parent.ID, instances)
	session.RefreshInstancesForCLIStatus(kids)

	type childRow struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Status      string `json:"status"`
		DoneStatus  string `json:"done_status,omitempty"`
		DoneSummary string `json:"done_summary,omitempty"`
		DoneAt      string `json:"done_at,omitempty"`
	}
	rows := make([]childRow, 0, len(kids))
	var human strings.Builder
	fmt.Fprintf(&human, "Children of %s (%s):\n", parent.Title, parent.ID)
	for _, k := range kids {
		_ = k.UpdateStatus()
		row := childRow{ID: k.ID, Title: k.Title, Status: StatusString(k.Status)}
		if e, ok := session.ReadLedgerEntry(k.ID); ok {
			row.DoneStatus = e.Status
			row.DoneSummary = e.Summary
			if !e.FinishedAt.IsZero() {
				row.DoneAt = e.FinishedAt.Format(time.RFC3339)
			}
		}
		rows = append(rows, row)
		done := row.DoneStatus
		if done == "" {
			done = "-"
		}
		fmt.Fprintf(&human, "  %s  %-20s  %-8s  done=%s  %s\n", k.ID, k.Title, row.Status, done, row.DoneSummary)
	}
	if len(kids) == 0 {
		human.WriteString("  (no sub-sessions)\n")
	}
	out.Print(human.String(), map[string]interface{}{"parent": parent.ID, "children": rows})
}
```

Add to the dispatch switch (after the `case "output":` block ~line 84):

```go
	case "children":
		handleSessionChildren(profile, args[1:])
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./cmd/agent-deck/ -run TestChildrenOf -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Add help line + commit**

In `printSessionHelp` add a line near the `output` entry:
```go
	fmt.Println("  children [id]           List sub-sessions with status + last completion")
```

```bash
git add cmd/agent-deck/session_cmd.go cmd/agent-deck/session_children_test.go
git commit -m "feat(cli): add session children fleet view"
```

---

### Task 5: `launch --assert-done`

**Files:**
- Modify: `cmd/agent-deck/launch_cmd.go`
- Test: `cmd/agent-deck/launch_assert_done_test.go`

- [ ] **Step 1: Write the failing test (pure append helper)**

```go
package main

import (
	"strings"
	"testing"
)

func TestApplyAssertDoneAppendsSentinel(t *testing.T) {
	got := applyAssertDone("do the thing", true)
	if !strings.Contains(got, "===AGENTDECK_DONE===") {
		t.Fatalf("expected sentinel instruction appended, got: %q", got)
	}
	if !strings.HasPrefix(got, "do the thing") {
		t.Fatalf("expected original message preserved, got: %q", got)
	}
}

func TestApplyAssertDoneDisabledIsNoop(t *testing.T) {
	if got := applyAssertDone("msg", false); got != "msg" {
		t.Fatalf("expected no-op when disabled, got: %q", got)
	}
}

func TestApplyAssertDoneEmptyMessageStaysEmpty(t *testing.T) {
	if got := applyAssertDone("", true); got != "" {
		t.Fatalf("expected empty message untouched (nothing to attach to), got: %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./cmd/agent-deck/ -run TestApplyAssertDone -v`
Expected: FAIL — undefined `applyAssertDone`.

- [ ] **Step 3: Implement helper + wire flag**

Add to `launch_cmd.go`:

```go
const assertDoneInstruction = "\n\n## Final step — assert completion\n" +
	"When the task is fully done, print exactly this as the last line of your final message:\n" +
	"  ===AGENTDECK_DONE=== status=ok summary=<what you accomplished, one line>\n" +
	"Use status=fail if you could not complete it; put the blocker in the summary."

func applyAssertDone(message string, enabled bool) string {
	if !enabled || strings.TrimSpace(message) == "" {
		return message
	}
	return message + assertDoneInstruction
}
```

Wire the flags near the other `fs.*` declarations (~line 27):

```go
	assertDone := fs.Bool("assert-done", false, "Append a completion-sentinel instruction to the message (default on for -c claude)")
	noAssertDone := fs.Bool("no-assert-done", false, "Disable the completion-sentinel instruction")
```

After `initialMessage := mergeFlags(*message, *messageShort)` (~line 177), resolve the effective toggle and apply. `*tool` / `*command` resolution already exists in this file — reuse the resolved tool variable used to set the session command (find the variable holding the tool, e.g. via `mergeFlags(*command, *commandShort)` or `*tool`); name it `resolvedTool` below:

```go
	assertDoneOn := *assertDone
	if !*assertDone && !*noAssertDone && isClaudeTool(resolvedTool) {
		assertDoneOn = true // default-on for Claude children
	}
	if *noAssertDone {
		assertDoneOn = false
	}
	initialMessage = applyAssertDone(initialMessage, assertDoneOn)
```

Use the existing tool predicate `session.IsClaudeCompatible(resolvedTool)` instead of a new `isClaudeTool` if the tool string is available; otherwise gate on the command containing "claude". Confirm the exact resolved-tool variable name by reading lines 130-180 of `launch_cmd.go` during implementation.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go test ./cmd/agent-deck/ -run TestApplyAssertDone -v && go build ./...`
Expected: PASS + clean build.

- [ ] **Step 5: Add help/example + commit**

Add an example line in `launch`'s usage:
```go
		fmt.Println("  agent-deck launch . -c claude -m \"Refactor X\"   # auto-appends completion sentinel")
```

```bash
git add cmd/agent-deck/launch_cmd.go cmd/agent-deck/launch_assert_done_test.go
git commit -m "feat(cli): launch --assert-done appends completion sentinel (default-on for claude)"
```

---

### Task 6: Full build + test sweep + manual smoke

- [ ] **Step 1: Full package build + tests**

Run: `cd /Users/doozyx/DoozyX/Adaptam/ui/agent-deck && go build ./... && go test ./internal/session/ ./cmd/agent-deck/ 2>&1 | tail -20`
Expected: build clean, target tests pass. (Pre-existing unrelated failures in the wider suite are out of scope — note them, don't fix.)

- [ ] **Step 2: Manual smoke (optional, requires install)**

```bash
go build -o /tmp/agent-deck ./cmd/agent-deck
/tmp/agent-deck session children --json   # from inside a session: lists children, empty if none
```

- [ ] **Step 3: Final commit if any fixups**

```bash
git commit -am "test: fleet fan-out sweep fixups" || true
```

---

## Self-Review

- **Spec coverage:** change 1 (persist completion) → Tasks 1-3 (ledger + both emit sites); change 2 (non-destructive fleet view) → Task 4 (`session children`, read-only); change 3 (reliable signaling) → Task 5 (`--assert-done`). Non-goals (no blocking wait, no manifest) respected.
- **Placeholder scan:** module path in Task 4 test and resolved-tool variable name in Task 5 are explicitly flagged to confirm by reading the file during implementation — not silent TODOs.
- **Type consistency:** `CompletionLedgerEntry` fields used identically in Tasks 1-4; `WriteLedgerEntry`/`ReadLedgerEntry` names consistent; `childrenOf` / `applyAssertDone` signatures match their tests.
