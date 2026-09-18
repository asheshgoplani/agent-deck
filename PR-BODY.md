# Fix gaps left by the rc.3 re-verification, plus rc.4 paste-truncation follow-ups

Refs #2214, #2062, #2230, #2148, #2149.

## #2214 — shell-tool sessions still hit ErrPaneCwdDeleted on a poisoned tmux server

rc.3's fix for #2214 covers sessions whose tool launches as the pane's initial
process (claude/codex/etc: `RunCommandAsInitialProcess=true`). It does not
cover sessions whose tool resolves to the generic "shell" launcher
(`RunCommandAsInitialProcess=false`): the pane opens as a bare interactive
shell first, and `verifyPaneWorkDirUnlessPlaceholder` ran immediately after
that bare pane was created — before the deferred `SendKeysAndEnter(cwdAssert
Command(...))` fallback ever sent the cd-assert — so it inspected the
still-poisoned pane and rejected the session with `ErrPaneCwdDeleted`, even
though the pending cd-assert would have recovered it exactly like the
initial-process path does.

Fix: defer the guard for that path until after the cd-assert command has
actually been sent.

Adds `TestStart_SurvivesExternallyPoisonedServer_ShellTool` (failing before
the change, passing after).

## #2062 — no CLI/TUI surface to retry or clear dead-letter records

`inbox dead-letter retry`/`purge` and the `Alt+D` TUI panel (PR #2230, by
**@nandanadileep**) were merged into local integration on 2026-09-17 and
reverted 22 seconds later, because the PR redeclared `DeadLetterRecord` with
a shape incompatible with the one #2111's read-only `list`/`show` inspection
had already introduced — `go build` failed with "redeclared in this block".
The revert dropped retry, purge and the TUI panel entirely, leaving only
`list`/`show` in rc.3, which #2062 itself says is insufficient.

Fix: reconcile the two `DeadLetterRecord` shapes into one type instead of
picking a side. It keeps #2111's `Ref` (an exact source snapshot + byte
offset — `list`/`show` never act on a record, so refusing a stale ref is
safe) and adds #2230's `ID` (a content hash, stable across unrelated store
mutations — what `retry`/`purge` key off, since removing one record must not
invalidate every other record's identifier). `list`/`show` output now
includes each record's `id` too, so a record found via `list` can be acted
on via `retry`/`purge`.

Reapplies PR #2230's `dead_letter_management.go`, `dead_letter_panel.go`
(`Alt+D` TUI) and its own follow-up fix (hashing raw scanner bytes for
retry/purge matching) unmodified in logic. `inbox dead-letter help` now
lists all four subcommands.

New/updated tests: retry on a missing target fails honestly and keeps the
record; retry on a deliverable record succeeds, removes only that record,
and is idempotent; purge refuses without `--older-than`/`--yes` and deletes
only matching records; a TUI test for the `Alt+D` panel (list/show/retry/
confirmed purge).

## #2148 — apply the maintainer's review-hold revisions to the structure advisory workflow

PR #2149's workflow landed in rc.3 exactly as the pre-review draft the
maintainer put a review hold on (2026-09-06). None of the three requested
corrections were applied:

1. `github.event.pull_request.base.sha` is the base branch's current tip, not
   the PR's actual merge base — an unrelated main-branch commit landed after
   the PR branched would get attributed to the PR's own structural delta.
   Now computes the real common ancestor via `git merge-base`.
2. The summary step read each whole log file into memory before truncating
   to the displayed tail, with no bound and no validation that the values
   being subtracted were numeric. Now bounds the read itself (seeks from the
   end) and skips (rather than crashing or silently misformatting) a
   non-numeric value.
3. Checkout steps kept push credentials they never use, and the sentrux
   binary download had no integrity check beyond a pinned version tag. Added
   `persist-credentials: false` to every checkout and a pinned sha256
   checksum on the binary download.

Adds `tests/ci/structure-advisory-workflow.test.sh` (failing against the
pre-revision workflow, passing after).

The issue's second half — an `[mcps.sentrux]` config entry so worker
sessions can query the structural delta before opening a PR — is untouched
by PR #2149 and stays open; #2148 should get a milestone note, not a close.

## Paste-truncation follow-ups (conductor addendum, from the launch-truncation fix's review)

Merges `fix/launch-truncation` (rc.3 P1: compare Claude's collapsed
`[Pasted text #N +M lines]` marker against hard line breaks, not a physical
line count) as this branch's prerequisite, then applies two follow-up
findings from that fix's review:

- **Tail-loss detection gap.** `ExpectedPasteMarkerLineBreaks` excluded a
  message's trailing hard break as a defensive "floor". A floor is
  ambiguous by construction: a message ending in `\n` and a paste truncated
  exactly one line short of it could land on the same floored expectation,
  so a lost last line went undetected. Now counts every hard break
  literally, including a trailing one, matching Claude's own documented
  formula.
- **`session send` had no truncation guard.** Only the launch/instance send
  path checked the paste marker before pressing Enter; `session send`
  (including `--message-file`, which most commonly carries a multi-line
  body) did not. Wired the same guard into `executeSend` for Claude-
  compatible targets; single-line messages and non-Claude targets are
  unaffected. A follow-up simplification pass extracted the identical
  pre-Enter check into `send.PasteTruncationCheck`, shared by both send
  paths.

## r2 review follow-ups (P2-1, P2-2)

Two P2 findings from the independent review of this branch's own `fb142a6f`:

- **`purge` must never touch the `_unowned` ledger.** It previously could
  (`--yes`, `--older-than`, or a single-ID purge), in tension with that
  ledger's own documented invariant that only the TTL sweep removes a
  record. Fixed: `purge` now always skips `_unowned` records and reports how
  many it skipped, both in its human-readable summary and its new `--json`
  output. `retry` is unchanged — it may still redeliver an `_unowned`
  record.
- **`retry`/`purge` now accept `--json`**, matching `list`/`show`. Output is
  a JSON array of `{"id","action","outcome","reason"}` objects, one per
  record considered.

Documented in `skills/agent-deck/references/cli-reference.md`. New tests:
`TestIssue2062PurgeYesNeverTouchesUnowned`,
`TestIssue2062PurgeSingleUnownedRecordRefused`,
`TestIssue2062RetryJSONShape`, `TestIssue2062PurgeJSONShape`.

## Test plan

- [x] `go build ./...`
- [x] `go vet ./...`
- [x] `go test ./internal/tmux/... ./internal/session/... ./internal/ui/... ./cmd/agent-deck/...` (Docker, `agentdeck-gotest:1.25-tmux`) — clean except the pre-declared known-pre-existing failures (the 5 root-permission tests, `TestKill_LiveSessionThenSecondKillBothSucceed`)
- [x] `bash tests/ci/structure-advisory-workflow.test.sh`
- [x] Alt+D TUI panel captured on screen (golden frame)
