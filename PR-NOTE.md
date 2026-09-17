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
