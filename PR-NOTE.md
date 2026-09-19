# Note for the contributor (PR #2085)

Thanks for the shell-completion work — it's a genuinely useful feature and the test coverage is thorough.

The PR had drifted behind `main` and showed as conflicting (CI had also never run on it, only the intake check). We rebased it forward for you so it's clean against current `main`:

- Kept all four of your commits as-is (authorship untouched).
- Resolved two small conflicts on top:
  - `cmd/agent-deck/main.go`: `main` had grown a few extra entries in the `commandRegistry` map (`--version`, `-v`, `--help`, `-h`, `telemetry`) since you branched. Merged those with your `completion`/`__complete` entries — nothing from either side was dropped.
  - `skills/agent-deck/references/cli-reference.md`: both your branch and `main` added a new doc section right after "migrate-paths". Kept both, in the order update-docs then your Shell Completion section.
- `README.md` merged automatically with no manual changes needed.

Verified after rebase: `go build ./...` and `go vet ./...` are clean, and the full completion test suite (`cmd/agent-deck` package, all `Completion`/`Complete` tests plus the rest of the package) passes in a sandboxed Docker container. Only the zsh/fish shell-syntax subtests skip, because those shells aren't installed in the bare `golang:1.25` test image — that's an environment gap, not something wrong with your code.

Nothing in your implementation needed changing. This is purely a rebase to clear the conflict flag so it can go through review/CI cleanly.

# Note for the contributor (PR #2120)

Thanks for the fix — the sizing-policy propagation logic was solid and needed
no code changes. Here's what we did on top:

## What we did

1. Rebased `fix/2061-shell-window-sizing` onto current `origin/main` (it was 60
   commits behind). The rebase was clean, no conflicts, your commit carried
   over verbatim.
2. Re-ran build, vet, and lint on the rebased tree, and re-ran the touched
   package's tests in an isolated Docker container.
3. Trimmed the PR body below to plain, checkable evidence — the original draft
   included specific benchmark numbers, named review models, and per-run
   receipt counts that we can't independently verify from this pass, so
   they're replaced with what we actually confirmed here.

## Result

- The gosec G702 lint finding that was showing on `internal/tmux/socket.go:212`
  is gone after the rebase — it was a stale-branch artifact (current `main`
  already carries the `#nosec G204,G702` annotation on that call site your
  branch predates). Confirmed with `golangci-lint run ./internal/tmux/...` →
  0 issues.
- `go build ./...` and `go vet ./...` are clean.
- Your new test, `TestSession_NewShellWindowSizePolicy` (and its 5 subtests),
  passes reliably in the Docker tmux sandbox, run twice back-to-back.
- One unrelated test in the same package, `TestKill_LiveSessionThenSecondKillBothSucceed`,
  failed once under full-package concurrent load and then passed 3/3 times in
  isolation — a pre-existing flake, not something your change touches or
  causes.

## Suggested replacement PR body

---

**What problem does this solve?**

Open Shell Here in window mode loses the managed session's sizing policy: the
initial window has `largest/on`, while the new shell window inherits
`latest/off`. Explicit sizing overrides are also lost. This addresses the
Deck-created-window portion of #2061, reported by @rafi-rr.

**Why this change**

Configure the exact window ID returned by `new-window`, using the same sizing
defaults and explicit option values as session creation. `set-window-option
-oq` preserves local options installed by a user's hook while still applying
the next option. Actual window-creation errors remain errors; optional
post-creation configuration failures are logged, not fatal.

**User impact**

Deck-created shell windows retain their intended sizing policy. Global
defaults and existing windows are untouched. Native `prefix c` and
agent-created windows keep the existing documented workaround. The
macOS/tmux 3.7a height-collapse observation from #2061 stays open and
unaddressed by this PR.

**Testing**

- `go build ./...`, `go vet ./...`: clean.
- `golangci-lint run ./internal/tmux/...`: 0 issues.
- `go test ./internal/tmux/...` in the project's sandboxed Docker tmux
  container: the new `TestSession_NewShellWindowSizePolicy` suite (5 subtests)
  passes reliably.
- Rebased onto current `main`; no conflicts.

**AI disclosure**

- [x] AI-assisted implementation and review; commit authorship and issue
  credit as stated above.

---

## perf(conductor): delete polling turns

### What this does

Removes two sources of avoidable conductor-supervision overhead:

1. **Heartbeat rules are referenced, not replayed.** Both the OS heartbeat
   script and the Python bridge's `heartbeat_loop` used to `cat` the whole
   `HEARTBEAT_RULES.md` into the conductor's message on every tick. They now
   send a path (`Read heartbeat rules from $RULES_FILE.`) and let the
   conductor read it itself — a fixed-size reference instead of an
   ever-growing, cache-busting blob replayed every heartbeat.
2. **A blocking child-stream replaces manual polling.** New
   `agent-deck session children --follow [--until-done] [--interval] [--heartbeat]`
   streams JSONL child-state events (`snapshot`/`added`/`status`/`done`/`removed`/
   `heartbeat`/`complete`) and, with `--until-done`, blocks in one call until
   every child is terminal (`waiting`, `error`, `stopped`, or `idle` with a
   recorded completion). Conductor templates and `skills/fleet/SKILL.md` now
   tell conductors to make this one blocking call instead of spending a
   turn per `list --json`/`session children --json` poll.

Because regenerating the conductor templates for this new guidance meant
already-installed `CONDUCTOR.md`/instruction files needed to be migrated in
place, this PR also hardens the generated-file writer
(`writeGeneratedFileOrMigrate` + per-platform atomic-rename helpers) so an
upgrade never clobbers a user's edits, an inode still open in an editor, or a
custom symlink — covered by new clobber/migration-recovery tests.

### Before / after

- **Heartbeat payload** (measured against a representative 856-byte
  `HEARTBEAT_RULES.md`): 856 bytes/tick → 211 bytes/tick (75% smaller), and
  the new cost is flat regardless of rules-file size while the old cost grew
  with it.
- **Supervision turns** (measured via
  `TestRunChildrenFollowEmitsWaitingAndErrorImmediately`): observing two
  children transition to `waiting`/`error` took 2 separate poll-and-decide
  turns before this change; `--follow --until-done` does it in 1 blocking
  call, with all 6 JSONL events on that single stream. The saving is O(1)
  vs. the old O(polls), so it grows with supervision duration.

Full method and raw output: `RESULTS.md`.

### Verification

- `go build ./...`, `go vet ./...`: clean.
- Docker (`golang:1.25`, non-root, `--network none`, `--cap-drop ALL`),
  narrowed to the touched packages:
  - `cmd/agent-deck`: `TestRunChildrenFollow*`, `TestDiffChildEvents`,
    `TestChildTerminal`, `TestAllChildrenTerminal`, `TestSummarizeChildren`,
    `TestFollowEventJSONShape` — all pass.
  - `internal/session`: heartbeat-rules-reference test plus the full
    clobber/migration-recovery suite (`TestWriteGeneratedFileOrMigrate*`,
    `TestGeneratedConductorInstructionsMigrateExactPriorTemplate`,
    `TestSetupConductorWithAgent_PreservesEditsAndMetaOnRerun`,
    `TestInstallSharedConductorInstructions_PreservesEditedRegularFile`,
    `TestInstallPolicyMD_PreservesEditedRegularFile`,
    `TestMigrationPreservesBothConcurrentEdits`,
    `TestMigrationRetainsOpenEditorInode`,
    `TestMigrationDefaultRerunPreservesCustomSymlinks`) — all pass.
- Full unnarrowed `go test ./...` intentionally not run locally (standing
  rule against local agent-deck suites); the required CI "Full test suite
  (PR gate)" job is the merge gate for that.

### Compatibility

Backward compatible: with no `HEARTBEAT_RULES.md` resolved, the heartbeat
message falls back to the same pre-PR text it always did. `childTerminal`
never lets a stale ledger `DoneStatus` override a live `running`/`queued`/
`unknown` status. Template migration only replaces on-disk files that still
match the exact prior generated template; edited files, open-editor inodes,
and custom symlinks are left alone.

---

Thanks for #2080 — the bounded-skip fix for the stale-pane starvation case is
solid and unchanged here.

Independent review held on one point: interactive-state detection still
inferred an open AskUserQuestion picker purely from a raw tmux pane-text
capture, when a real busy/interactive signal already exists — the same
fresh hook-driven status (`"running"`/`"starting"`) the send path treats as
authoritative after the queued-delivery fix in #2273. A picker's
`PreToolUse` event never advances to `Stop`/`PostToolUse` until the human
answers, so that hook status alone already tells you the pane is
interactive, without guessing from glyphs.

On top of your two commits I added two more, `carry/2080`:

- `session show --json` now always reports `hook_status` and
  `hook_status_fresh`, mirroring what `--defer-if-busy` already reads
  internally.
- The bridge's `_pane_blocks_automated_send` gates on that hook signal
  first when it's known (confirmed `"hook-busy-interactive"`); pane-text
  picker detection now runs only as a fallback when the hook is unknown,
  and that fallback verdict is reported with an `"unknown:"` prefix so it's
  never confused with confirmed evidence. The composer-unsent-draft check
  is unchanged (it's orthogonal to turn state).
- Added a failing-first test proving the guard blocks on a completely
  neutral pane when the hook says interactive, plus coverage for the
  unknown-hook fallback and backward compatibility with the old call shape.

Full root-cause writeup and test evidence in `RESULTS.md`. Your original
commits are untouched.
