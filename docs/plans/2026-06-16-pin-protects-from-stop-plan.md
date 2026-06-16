# Pin Protects Sessions From Auto/Bulk Stops — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a pinned session (`Pin != PinNone`) immune to the idle-timeout watcher and to bulk "remove all errored" (unless `--force`), while leaving explicit single stops untouched.

**Architecture:** Add a pin guard at the two *decision* sites that initiate automatic/bulk kills — `IdleTimeoutWatcher.Tick` and the CLI/TUI bulk-remove paths — never in the shared `killInternal()` primitive (so explicit stops keep working).

**Tech Stack:** Go; existing `session` package, `cmd/agent-deck` CLI, `internal/ui` Bubble Tea TUI. Tests: standard `go test`.

## Global Constraints

- Pinned = `inst.Pin != session.PinNone` (covers `PinTop` and `PinBottom`).
- No new DB field, no schema bump, no new config flag, no new CLI flag (`--force` is reused).
- Do NOT modify `Kill()`, `KillAndWait()`, or `killInternal()`.
- CLI tests are subprocess-based and skipped under `testing.Short()`.

---

### Task 1: Idle-timeout watcher skips pinned sessions

**Files:**
- Modify: `internal/session/idle_timeout_watcher.go` (`Tick`, ~line 154 loop)
- Test: `internal/session/issue1143_idle_timeout_test.go` (add tests; reuse existing `newRunningInstance`, `recordingStopper`, `fakeCapture`, `newFakeClock`)

**Interfaces:**
- Consumes: existing `IdleTimeoutWatcher`, `IdleTimeoutWatcherConfig{Now,Capture,Stop}`, `newRunningInstance(id,title,idleTimeoutSecs)`, `recordingStopper.StoppedIDs()`.
- Produces: no new exported symbols. Behavior: `Tick` drops pinned instances from `lastSeen` and `continue`s before any Stop.

- [ ] **Step 1: Write the failing tests**

Add to `internal/session/issue1143_idle_timeout_test.go`:

```go
// Pinned sessions are immune to idle auto-stop (pin-protects-from-stop).
func TestIdleTimeoutWatcher_PinnedSessionNeverStopped(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}
	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-pinned", "worker-pinned", 10)
	inst.Pin = PinTop
	capture.Set(inst.ID, "static pane content")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: capture.Capture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst})
	clock.Advance(120 * time.Second)
	w.Tick([]*Instance{inst})

	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("pinned session must not be auto-stopped, got Stop for %v", got)
	}
	// No lifecycle event should have been logged for a skipped session.
	logPath := GetSessionLifecycleLogPath()
	if data, err := readFileQuiet(logPath); err == nil && strings.Contains(string(data), inst.ID) {
		t.Fatalf("pinned skip must not emit a lifecycle event, log had: %s", string(data))
	}
}

// Unpinning re-arms idle tracking: a session pinned then unpinned auto-stops.
func TestIdleTimeoutWatcher_UnpinReArmsAutoStop(t *testing.T) {
	clock := newFakeClock(time.Unix(1_700_000_000, 0))
	capture := newFakeCapture()
	stopper := &recordingStopper{}
	t.Setenv("HOME", t.TempDir())

	inst := newRunningInstance("inst-rearm", "worker-rearm", 10)
	inst.Pin = PinTop
	capture.Set(inst.ID, "static")

	w := NewIdleTimeoutWatcher(IdleTimeoutWatcherConfig{
		Now: clock.Now, Capture: capture.Capture, Stop: stopper.Stop,
	})

	w.Tick([]*Instance{inst}) // pinned: skipped
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst}) // still pinned: skipped
	if got := stopper.StoppedIDs(); len(got) != 0 {
		t.Fatalf("expected no stop while pinned, got %v", got)
	}

	inst.Pin = PinNone
	w.Tick([]*Instance{inst}) // re-arms: records last-seen
	clock.Advance(11 * time.Second)
	w.Tick([]*Instance{inst}) // idle elapsed: stop
	if got := stopper.StoppedIDs(); len(got) != 1 || got[0] != inst.ID {
		t.Fatalf("expected stop after unpin+idle, got %v", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/session/ -run 'TestIdleTimeoutWatcher_PinnedSessionNeverStopped|TestIdleTimeoutWatcher_UnpinReArmsAutoStop' -v`
Expected: FAIL — pinned session gets stopped (Stop called).

- [ ] **Step 3: Add the pin skip in `Tick`**

In `internal/session/idle_timeout_watcher.go`, inside the `for _, inst := range instances` loop, after the `inst == nil` check and before/with the `IdleTimeoutSecs <= 0` check, add:

```go
		if inst.Pin != PinNone {
			// pin-protects-from-stop: a pinned session is exempt from idle
			// auto-stop. Drop tracking so unpinning re-arms cleanly next tick.
			delete(w.lastSeen, inst.ID)
			idleLog.Debug("idle_timeout_skip_pinned",
				slog.String("instance_id", inst.ID),
				slog.String("pin", string(inst.Pin)),
			)
			continue
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/session/ -run 'TestIdleTimeoutWatcher_PinnedSessionNeverStopped|TestIdleTimeoutWatcher_UnpinReArmsAutoStop' -v`
Expected: PASS. Also run the existing suite to confirm no regression:
Run: `go test ./internal/session/ -run TestIssue1143 -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/session/idle_timeout_watcher.go internal/session/issue1143_idle_timeout_test.go
git commit -m "feat(session): idle-timeout watcher skips pinned sessions"
```

---

### Task 2: CLI bulk remove-all-errored skips pinned unless --force

**Files:**
- Modify: `cmd/agent-deck/session_remove_cmd.go` (`handleSessionRemove` call site ~line 56; `removeAllErrored` ~line 134)
- Test: `cmd/agent-deck/session_remove_cmd_test.go` (add tests; reuse `addTestSession`, `forceSetStatus`, `runAgentDeck`, `readSessionsJSON`)

**Interfaces:**
- Consumes: `*force` flag already parsed in `handleSessionRemove`; `session.PinNone`.
- Produces: `removeAllErrored(out, storage, instances, groups, pruneWorktree bool, force bool)` — new trailing `force` param. Skipped-pinned count surfaced in success output (human + JSON field `skipped`).

- [ ] **Step 1: Write the failing tests**

Add to `cmd/agent-deck/session_remove_cmd_test.go`:

```go
// Pinned errored sessions survive --all-errored unless --force is given.
func TestSessionRemove_AllErrored_SkipsPinned(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	pinnedID := addTestSession(t, home, filepath.Join(home, "pin-proj"), "pinned-err")
	plainID := addTestSession(t, home, filepath.Join(home, "plain-proj"), "plain-err")
	if _, stderr, code := runAgentDeck(t, home, "session", "set", pinnedID, "pin", "top"); code != 0 {
		t.Fatalf("set pin failed: %s", stderr)
	}
	forceSetStatus(t, home, pinnedID, session.StatusError)
	forceSetStatus(t, home, plainID, session.StatusError)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--json")
	if code != 0 {
		t.Fatalf("--all-errored failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if !strings.Contains(listJSON, pinnedID) {
		t.Errorf("pinned errored session must survive --all-errored; list:\n%s", listJSON)
	}
	if strings.Contains(listJSON, plainID) {
		t.Errorf("unpinned errored session should have been removed; list:\n%s", listJSON)
	}
}

// --force includes pinned errored sessions in the bulk sweep.
func TestSessionRemove_AllErrored_ForceIncludesPinned(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	pinnedID := addTestSession(t, home, filepath.Join(home, "pin-proj"), "pinned-err")
	if _, stderr, code := runAgentDeck(t, home, "session", "set", pinnedID, "pin", "top"); code != 0 {
		t.Fatalf("set pin failed: %s", stderr)
	}
	forceSetStatus(t, home, pinnedID, session.StatusError)

	stdout, stderr, code := runAgentDeck(t, home, "session", "remove", "--all-errored", "--force", "--json")
	if code != 0 {
		t.Fatalf("--all-errored --force failed (exit %d) stdout=%s stderr=%s", code, stdout, stderr)
	}
	listJSON := readSessionsJSON(t, home)
	if strings.Contains(listJSON, pinnedID) {
		t.Errorf("--force should remove pinned errored session; list:\n%s", listJSON)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/agent-deck/ -run 'TestSessionRemove_AllErrored_SkipsPinned|TestSessionRemove_AllErrored_ForceIncludesPinned' -v`
Expected: FAIL — `TestSessionRemove_AllErrored_SkipsPinned` fails because the pinned session is removed.

- [ ] **Step 3: Thread `force` into `removeAllErrored` and skip pinned**

In `cmd/agent-deck/session_remove_cmd.go`, update the call site (~line 56):

```go
	if *allErrored {
		removeAllErrored(out, storage, instances, groups, *pruneWorktree, *force)
		return
	}
```

Update the signature and loop body of `removeAllErrored`:

```go
func removeAllErrored(
	out *CLIOutput,
	storage *session.Storage,
	instances []*session.Instance,
	groups []*session.GroupData,
	pruneWorktree bool,
	force bool,
) {
	var removed []map[string]interface{}
	remaining := instances[:0]
	var removedIDs []string
	skipped := 0
	for _, inst := range instances {
		if inst.Status == session.StatusError {
			// pin-protects-from-stop: a pinned errored session is retained
			// unless --force is given.
			if inst.Pin != session.PinNone && !force {
				skipped++
				remaining = append(remaining, inst)
				continue
			}
			_ = inst.KillAndWait()
			if pruneWorktree {
				pruneSessionWorktree(inst)
			}
			if err := storage.DeleteInstance(inst.ID); err != nil {
				out.Error(fmt.Sprintf("failed to remove session %s: %v", inst.ID, err), ErrCodeInvalidOperation)
				os.Exit(1)
			}
			removedIDs = append(removedIDs, inst.ID)
			removed = append(removed, map[string]interface{}{"id": inst.ID, "title": inst.Title})
			continue
		}
		remaining = append(remaining, inst)
	}
```

Then update the success output at the end of the function. Locate the existing
`out.Success(fmt.Sprintf("Removed %d errored session(s)", len(removed)), map[string]interface{}{ ... })`
call and change it to include the skipped count:

```go
	msg := fmt.Sprintf("Removed %d errored session(s)", len(removed))
	if skipped > 0 {
		msg += fmt.Sprintf(" (skipped %d pinned — use --force to include)", skipped)
	}
	out.Success(msg, map[string]interface{}{
		"removed": removed,
		"skipped": skipped,
	})
```

(Preserve any other existing keys already present in that success payload; just add `"skipped": skipped`.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/agent-deck/ -run 'TestSessionRemove_AllErrored' -v`
Expected: PASS (including the existing `TestSessionRemove_AllErrored_RemovesOnlyErrored`).

- [ ] **Step 5: Commit**

```bash
git add cmd/agent-deck/session_remove_cmd.go cmd/agent-deck/session_remove_cmd_test.go
git commit -m "feat(cli): session remove --all-errored skips pinned unless --force"
```

---

### Task 3: TUI bulk remove errored skips pinned

**Files:**
- Modify: `internal/ui/home.go` (`bulkRemoveErrored`, ~line 11309)

**Interfaces:**
- Consumes: `h.instances`, `session.StatusError`, `session.PinNone`.
- Produces: `bulkRemoveErrored` no longer emits `sessionDeletedMsg` for pinned errored sessions.

- [ ] **Step 1: Modify `bulkRemoveErrored` to skip pinned**

In `internal/ui/home.go`, update the collection loop:

```go
func (h *Home) bulkRemoveErrored() tea.Cmd {
	h.instancesMu.RLock()
	ids := make([]string, 0, len(h.instances))
	for _, inst := range h.instances {
		// pin-protects-from-stop: pinned errored sessions are left alone in
		// bulk removal; an explicit Shift+D on the session still works.
		if inst.Status == session.StatusError && inst.Pin == session.PinNone {
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

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./internal/ui/`
Expected: builds clean (no test harness exists for this TUI helper; the change is a one-line filter mirroring Task 2's CLI logic, already covered by the CLI bulk tests).

- [ ] **Step 3: Commit**

```bash
git add internal/ui/home.go
git commit -m "feat(tui): bulk remove errored skips pinned sessions"
```

---

### Task 4: Full verification

- [ ] **Step 1: Vet and build the whole module**

Run: `go vet ./... && go build ./...`
Expected: no errors.

- [ ] **Step 2: Run the touched packages' tests**

Run: `go test ./internal/session/ ./cmd/agent-deck/ -run 'IdleTimeout|SessionRemove'`
Expected: PASS.

- [ ] **Step 3: Confirm explicit stop paths are untouched**

Grep to verify no guard leaked into the kill primitive:
Run: `grep -n "Pin" internal/session/instance.go | grep -i kill`
Expected: no output (killInternal/Kill/KillAndWait never reference Pin).
