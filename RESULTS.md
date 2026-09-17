# PR #2051 verification results — "perf(conductor): delete polling turns"

## What the diff actually does

Reviewed with `git diff bf506898..pr-2051` (12 commits, base `main` at `bf506898`).
The PR removes two sources of avoidable conductor-supervision overhead and adds
the safety work that fell out of doing that on a system with already-generated
files on disk:

1. **Heartbeat rules are referenced, not replayed.** `conductor.go`'s embedded
   heartbeat script and `conductor_bridge.py`'s `heartbeat_loop` used to `cat`
   the full `HEARTBEAT_RULES.md` into the message on every tick. Both now send
   `Read heartbeat rules from $RULES_FILE.` and let the conductor read the file
   itself. This drops a growing, cache-busting blob from every heartbeat and
   was the one line item literally named "delete polling turns" in the
   original commit.
2. **A blocking child-stream replaces repeated `list --json` polling.**
   `cmd/agent-deck/session_cmd.go` adds `session children --follow
   [--until-done] [--interval] [--heartbeat]`, implemented in
   `cmd/agent-deck/session_children_follow.go`. One call now blocks on the
   existing child-status stream and exits once every child is terminal
   (`waiting`, `error`, `stopped`, or `idle` with a ledger completion —
   `childTerminal`/`allChildrenTerminal`), emitting `snapshot` / `added` /
   `status` / `done` / `removed` / `heartbeat` / `complete` JSONL events as
   they happen. `conductor_templates.go` and `skills/fleet/SKILL.md` were
   rewritten to tell conductors to make this one blocking call instead of
   turn-by-turn `list --json`/`session children --json` polling.
3. **Follow-on fixes discovered by review, all in scope of the same change:**
   - `childTerminal` treats a `waiting` child as a supervision terminal so
     `--until-done` doesn't hang on a child that needs human input
     (`58b597fe`).
   - Regenerating `CONDUCTOR.md`/templates for the new guidance meant the
     on-disk generator (`writeGeneratedFileOrMigrate` in `conductor.go`,
     platform atomic-rename helpers in `generated_exchange_{darwin,linux,other}.go`)
     had to migrate already-installed instruction files without clobbering a
     user's edits, an open editor's inode, or custom symlinks
     (`f7c02f5c`, `d2f295fd`, `e1945dcc`, `5d3193ab`, `57ba1fc2`), covered by
     `conductor_clobber_test.go` and `conductor_migration_recovery_test.go`.

None of this is what the previous `RESULTS.md` in this branch described — that
file (removed by this pass) documented PR #1952's remote-inbox
`SourceRemote`/fingerprint identity work, a different PR entirely. It was
carried over by mistake and did not describe any code in this diff. This
revision replaces it with the verification actually run against PR #2051's
code.

## Host verification (this host only builds/vets; tests run in Docker per policy)

```
$ go build ./...
$ go vet ./...
```

Both exit 0, no output, from `/tmp/exec-heldpr-2051/src` at `carry/2051`
(`pr-2051` plus this results/PR-note pass, no production code changed).

## Docker test evidence (golang:1.25, non-root, `--network none`, `--cap-drop ALL`)

Dependencies were fetched once into the shared `agentdeck-gomod` volume with
network enabled (`go mod download all`); the test run itself used
`--network none` as required. Serialized with a local lock so no other agent's
Docker test run overlapped.

Touched-package, narrowed by `-run`:

```
$ docker run ... golang:1.25 sh -c 'go test ./cmd/agent-deck/... -run \
  "TestRunChildrenFollow|TestDiffChildEvents|TestChildTerminal|TestAllChildrenTerminal|TestSummarizeChildren|TestFollowEventJSONShape" -v'
...
--- PASS: TestDiffChildEvents (0.00s)                              (6 subtests)
--- PASS: TestChildTerminal (0.00s)                                (10 subtests)
--- PASS: TestRunChildrenFollowWaitsForCurrentTurnFinish (0.00s)
--- PASS: TestAllChildrenTerminal (0.00s)
--- PASS: TestSummarizeChildren (0.00s)
--- PASS: TestRunChildrenFollowStopsOnDeadStream (0.00s)           (2 subtests)
--- PASS: TestRunChildrenFollowEmitsWaitingAndErrorImmediately (0.00s)
--- PASS: TestFollowEventJSONShape (0.00s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	0.042s
```

```
$ docker run ... golang:1.25 sh -c 'apt-get install -y tmux; go test ./internal/session/... -run \
  "TestConductorHeartbeatScript_ReferencesHeartbeatRules|TestWriteGeneratedFileOrMigrate|TestGeneratedConductorInstructionsMigrateExactPriorTemplate|TestSetupConductorWithAgent_PreservesEditsAndMetaOnRerun|TestInstallSharedConductorInstructions_PreservesEditedRegularFile|TestInstallPolicyMD_PreservesEditedRegularFile|TestMigrationPreservesBothConcurrentEdits|TestMigrationRetainsOpenEditorInode|TestMigrationDefaultRerunPreservesCustomSymlinks" -v'
...
--- PASS: TestWriteGeneratedFileOrMigrateReplacesInodeAndRejectsUnsafeTargets (0.00s)  (2 subtests)
--- PASS: TestWriteGeneratedFileOrMigratePreservesEditedAndNewerAssets (0.00s)
--- PASS: TestWriteGeneratedFileOrMigrateExchangeFailureCleansTemporaryFile (0.00s)
--- PASS: TestWriteGeneratedFileOrMigratePublishesOnlyCompleteContent (0.00s)
--- PASS: TestGeneratedConductorInstructionsMigrateExactPriorTemplate (0.01s)          (3 subtests)
--- PASS: TestSetupConductorWithAgent_PreservesEditsAndMetaOnRerun (0.00s)
--- PASS: TestInstallSharedConductorInstructions_PreservesEditedRegularFile (0.00s)
--- PASS: TestInstallPolicyMD_PreservesEditedRegularFile (0.00s)
--- PASS: TestMigrationPreservesBothConcurrentEdits (0.00s)                            (2 subtests)
--- PASS: TestMigrationRetainsOpenEditorInode (0.00s)
--- PASS: TestMigrationDefaultRerunPreservesCustomSymlinks (0.00s)
--- PASS: TestConductorHeartbeatScript_ReferencesHeartbeatRules (0.00s)
PASS
ok  	github.com/asheshgoplani/agent-deck/internal/session	0.113s
```

A full, unnarrowed `go test ./...` was not run on this host, per the standing
rule against local agent-deck test suites (two same-day tmux fleet deaths on
2026-07-26) — the required CI "Full test suite (PR gate)" job covers that with
proper tmux/zoxide provisioning and is a merge gate independent of this pass.

## Before/after evidence for the "delete polling turns" claim

**1. Heartbeat message payload (measured, synthetic 6-rule/856-byte
`HEARTBEAT_RULES.md`, representative of a real conductor's rules file — not
live production traffic, which isn't accessible from this host):**

Reconstructed the pre-PR (`bf506898`) and post-PR heartbeat-message assembly
logic from `conductor.go` verbatim into two standalone shell snippets and ran
both against the same rules file, in the scratch directory (no repo or host
state touched):

```
old bytes per heartbeat message: 856
new bytes per heartbeat message: 211
reduction per tick: 645 bytes (75%)
old bytes/day at 48 ticks (30 min cadence): 41088
new bytes/day at 48 ticks: 10128
```

The reduction scales with the rules file's size and heartbeat cadence; a
larger rules file (several of Ashesh's per-conductor `HEARTBEAT_RULES.md`
files run into multiple KB, per `MEMORY.md`) makes the per-tick saving larger,
not smaller, since the new code sends a fixed ~90-byte path reference
regardless of file size while the old code was unbounded.

**2. Conductor supervision turns (measured from
`TestRunChildrenFollowEmitsWaitingAndErrorImmediately`, `cmd/agent-deck/session_children_follow_test.go:237`):**

The test's fixture models two children going from `running` to `waiting`
(with a fail ledger entry) and `running` to `error` over two internal polls.

- **Before this PR** (no `--follow`), a conductor discovering that same
  transition had to spend one full LLM turn per poll: call
  `session children --json`/`list --json`, decide nothing changed or
  something did, and — if still running — come back next heartbeat and call
  again. Observing the transition in this fixture takes **2 separate
  conductor turns** (2 process invocations, 2 LLM round-trips), and a
  longer-running fleet scales linearly: N polls before the last one shows
  `waiting`/`error` cost N turns.
- **After this PR**, `runChildrenFollow(..., untilDone=true, ...)` is **1
  process invocation** (one shell call, one LLM turn) that blocks across
  both internal polls and streams all 6 JSONL events —
  `snapshot`, `snapshot`, `status(waiting)`, `done(waiting)`,
  `status(error)`, `complete` — from that single call, verified by the test's
  assertion on `len(lines) == 6`.
- Net: **2 turns → 1 turn** in this reproducible fixture, and by construction
  the win grows (not shrinks) with supervision duration, since the old
  per-tick-turn cost was O(polls) and the new cost is O(1) regardless of how
  long the blocking call runs internally.

These are conservative, code-derived measurements (a synthetic rules file and
a two-poll unit-test fixture), not fabricated production telemetry; both are
reproducible with the commands recorded above.

## Invariant / regression check

- `childTerminal` never lets a stale `DoneStatus` override a live
  `running`/`queued`/`unknown` status (see `TestChildTerminal` subtests
  `stale_done_cannot_override_*`) — matches the "liveness is not identity"
  standard.
- `runChildrenFollow` treats a dead stdout (`... | head -1`) as done, not as
  an infinite background poll (`TestRunChildrenFollowStopsOnDeadStream`).
- Template/instruction migration never clobbers a user's edited
  `CONDUCTOR.md`, an inode still open in an editor, or a custom symlink
  (`conductor_clobber_test.go`, `conductor_migration_recovery_test.go`); it
  only replaces files that still match the exact prior generated template.
- Backward compatible: an old-controller/new-remote or new-controller/old-remote
  pairing degrades to the pre-PR text prefix (`Check if any need auto-response
  or user attention.`) when no `HEARTBEAT_RULES.md` resolves, unchanged from
  before this PR.

## Status

Not blocked. The code is a complete, self-consistent unit: the polling-turn
elimination plus the migration safety work it required. `go build`/`go vet`
are clean; the touched-package Docker tests above all pass; the two claimed
"turns deleted" mechanisms (heartbeat-rules inlining, child-status polling)
are demonstrated with measured before/after numbers rather than asserted.
