# RESULTS — fix/issue-verify-gaps-r2-20260918

Branch: `fix/issue-verify-gaps-r2-20260918`, off `release/v1.16.11-rc3` (`rc3` = `2704bc17`),
merged with `fix/launch-truncation` (`b3ca5e93`, required prerequisite for item 4).
Nothing pushed; no GitHub writes.

Final head: see `git log --oneline -1` on this branch at delivery time.

## Item 1 — #2214: panes inherit the tmux server cwd (shell-tool gap)

**Root cause.** rc.3's fix for #2214 (a poisoned tmux server ignoring `-c` for
new panes) prefixes every pane's own command with `cd -- <dir> &&`
(`cwdAssertCommand`) so the pane asserts its own directory instead of trusting
the server's. This is applied both when a tool launches as the pane's initial
process (`RunCommandAsInitialProcess=true`, e.g. claude/codex) and, as a
fallback, via `SendKeysAndEnter` for the generic "shell" tool
(`RunCommandAsInitialProcess=false`). But `Session.Start` ran
`verifyPaneWorkDirUnlessPlaceholder` **immediately after pane creation** —
before that deferred send-keys fallback ever executed. For a shell-tool
session on a poisoned server, the guard inspected the still-bare, still-
poisoned pane and rejected the session with `ErrPaneCwdDeleted`, even though
the pending cd-assert would have recovered it exactly like the
initial-process path already does.

**Fix.** `internal/tmux/tmux.go`: for the shell-tool path
(`command != "" && !RunCommandAsInitialProcess`), the guard is deferred until
*after* `SendKeysAndEnter(cwdAssertCommand(...))` actually runs. Extracted a
`killAfterPaneCwdFailure` helper (code-simplifier pass) so both guard sites
share the same kill+log+return behavior.

**Red/green.** `TestStart_SurvivesExternallyPoisonedServer_ShellTool`
(`internal/tmux/issue2214_server_cwd_test.go`) builds the same poisoned-server
fixture as the existing `TestStart_SurvivesExternallyPoisonedServer`, but with
`RunCommandAsInitialProcess: false` and a non-empty command. Before the fix:
`Start()` fails with `ErrPaneCwdDeleted`. After: `Start()` succeeds and the
pane's real process resolves to `workDir`.

## Item 2 — #2062: no CLI/TUI surface to retry or clear dead letters

**Why carry/2230 was reverted (confirmed).** `carry/2230` (PR #2230,
**Nandana Dileep**, head `17b54667`) implemented `inbox dead-letter
retry|purge` and the `Alt+D` TUI panel. It merged into local integration
cleanly at the text level (`f3a4a14a`) but was reverted 22 seconds later
(`05c1a8cf`) because it redeclared `DeadLetterRecord` with a shape
incompatible with the one `carry/2111`'s read-only `list`/`show` inspection
had already introduced:

| | #2111's `DeadLetterRecord` | carry/2230's `DeadLetterRecord` |
|---|---|---|
| identifier | `Ref` (source snapshot + byte offset, stale on any append/rewrite) | `ID` (content hash of the raw line, stable across unrelated mutations) |
| purpose | forensic inspection, incl. malformed/undecodable records | operator-safe retry/purge/TUI metadata |

`go build` failed with "redeclared in this block"; the revert dropped the
whole feature — retry, purge, and the TUI panel — leaving only `list`/`show`
in rc.3, which #2062's own triage comment already calls insufficient.

**Fix.** Reconciled the two shapes into **one** `DeadLetterRecord`
(`internal/session/deadletter_inspection.go`) instead of picking a side: it
is the union of both field sets. `list`/`show` keep populating `Ref`/
`Source`/`Offset`/`Problem`/`Raw` exactly as before (unchanged behavior,
unchanged tests); `InspectDeadLetters` now also computes each record's `ID`
using the same input bytes `deadLetterRecordID` hashes (mirroring
`bufio.Scanner`'s line-ending stripping), so an ID printed by `list`/`show`
is exactly the one `retry`/`purge` (which read through that scanner) accept.
`retry`/`purge`/the TUI panel keep populating `ID`/`ChildTitle`/
`TargetSessionID`/`Reason`/`Timestamp`/`AgeSeconds`/`Attempts`/
`PayloadSummary`/`Corrupt` exactly as carry/2230 did.

Reapplied unmodified in logic: `internal/session/dead_letter_management.go`,
`internal/ui/dead_letter_panel.go` (`Alt+D` TUI), and carry/2230's own
follow-up fix (`ddcad7d3`, hashing raw scanner bytes for retry/purge
matching — a real bug the PR author found and fixed the same day). Collapsed
a duplicate `inbox dead-letter` CLI dispatch and a second, incompatible
`list`/`show` reimplementation that the cherry-pick's auto-merge introduced
in `cmd/agent-deck/inbox_cmd.go`; `list`/`show` stay wired to
`InspectDeadLetters` (#2111), `retry`/`purge` dispatch to the reapplied
management functions, and `inbox dead-letter help` now lists all four
subcommands.

**Red/green.** `cmd/agent-deck/issue2062_deadletter_cli_test.go` (rewritten
to use `session.ListDeadLetters` directly for setup/lookup, matching what the
TUI does, since `list`/`show`'s own JSON shape is #2111's, not management's):
retry on a missing target fails honestly and retains the record; a
successful retry removes only that record and is idempotent on re-run; purge
refuses without `--older-than`/`--yes` and a bounded purge deletes only
matching records; help lists `list`/`show`/`retry`/`purge`.
`internal/ui/dead_letter_panel_test.go`'s existing
`TestIssue2062DeadLetterPanelListShowRetryAndConfirmedPurge` covers the
Alt+D panel end to end (list → show → failed retry → confirmed purge).

**Frames.** Golden frame of the `Alt+D` panel (list view and show-detail
view, seeded with two dead-letter records) captured via `panel.View()` — see
`frame-altd-panel.txt` alongside this file.

**Credit.** carry/2230 / PR #2230 by **Nandana Dileep**
(`@nandanadileep`); follow-up fix by Ashesh Goplani. Both credited in
PR-BODY.md.

## Item 3 — #2148: apply the maintainer's review-hold revisions to the structure advisory workflow

**Root cause (confirmed via `gh pr view 2149 --comments`).** The workflow
that landed in rc.3 (`fae07a2e`, merging PR #2149) is exactly the commit the
maintainer put a review hold on (2026-09-06, head
`19cb5f977e1f0d14532266c9369d8f2b0a1ca83d`), 11 days before the local
integration merge. None of the three requested corrections were applied:

1. `github.event.pull_request.base.sha` is the base branch's current tip,
   not the PR's actual merge base.
2. The summary step read each whole log file into memory before truncating
   to the displayed tail, with no size bound and no validation that the
   subtracted values were numeric.
3. Checkout steps kept push credentials they never use; the sentrux binary
   download had no integrity check beyond a pinned version tag.

**Fix** (`.github/workflows/structure-advisory.yml`):
1. Checks out the PR head with full history, resolves `git merge-base
   <base.sha> <head.sha>`, and checks out *that* commit as `base` instead of
   the base branch's tip.
2. The Python summary step now seeks to the last 200,000 bytes of each log
   before reading (bounding the read itself, not just the displayed tail),
   and validates `bv`/`hv` are numeric before subtracting — a non-numeric
   value is reported as `invalid` rather than crashing or silently
   misformatting.
3. `persist-credentials: false` on every `actions/checkout` step; the
   sentrux binary download is now checksum-verified via `sha256sum -c`
   against a pinned `SENTRUX_SHA256` (computed once from the exact bytes
   GitHub served for `v0.5.7` — there is no published checksums file for
   this release to verify against instead).

**Red/green.** `tests/ci/structure-advisory-workflow.test.sh` greps the
workflow for all of the above. Confirmed failing against the pre-revision
(rc.3) workflow (7 of 8 checks fail) and passing after (8/8).

**Status.** The issue's "worker MCP" half (an `[mcps.sentrux]` config entry
so worker sessions can query the structural delta before opening a PR) is
untouched by PR #2149 and **stays open** — #2148 should get a milestone
note, not a close.

## Item 4 — rc.4 paste-truncation follow-ups (conductor addendum, from the truncation-fix review)

Merged `fix/launch-truncation` (`b3ca5e93`, rc.3 P1: compare the collapsed
`[Pasted text #N +M lines]` marker against hard line breaks, not a physical
line count) as the prerequisite this branch needed for `send.CheckPasteMarker`
to exist. Then applied the review's two follow-up findings
(`/Users/ashesh/agent-deck-recovery/plans-20260917/receipts/review-launch-truncation/RESULTS.md`
P2-1, P2-2):

**P2-1 — tail-loss detection gap.** `ExpectedPasteMarkerLineBreaks` excluded
a message's trailing hard break as a defensive "floor" against not knowing
whether Claude's composer trims one before counting. A floor is ambiguous by
construction: `"a\nb\nc\n"` (3 lines, trailing newline) expected only 2
breaks, so a paste truncated to `"a\nb\n"` (line `c` lost) also declared 2
and was accepted as `PasteMarkerIntact`. Fixed by counting every hard break
literally, including a trailing one, matching Claude's own documented
formula `(text.match(/\r\n|\r|\n/g) || []).length` rather than hedging
against an unverified trim assumption. This is a deliberate resolution of a
known ambiguity (not previously provable live, per the review's own P2-3),
made in the direction the regex's literal definition supports.

**P2-2 — `session send` had no truncation guard.** `session send`
(`cmd/agent-deck/session_cmd.go`), including its `--message-file` form (which
most commonly carries a multi-line body), called `SendKeysAndEnter` directly
with no #2079 check — only the launch/instance send path had one. Wired
`sendRetryOptions.expectedPasteBreaks` into `executeSend` for Claude-
compatible targets, added `sendInitialKeysChecked`, and extended
`sendRetryTarget` with `SendKeysAndEnterChecked`. A single-line message or a
non-Claude target is unaffected (`expectedPasteBreaks` stays 0, sends go out
exactly as before). A code-simplifier pass then extracted the identical
pre-Enter check closure duplicated between this new code and
`internal/session/instance.go`'s `sendMessageWhenReady` into
`send.PasteTruncationCheck`, used by both.

**Red/green.**
- `internal/send/issue2079_paste_truncation_test.go`:
  `TestCheckPasteMarker_DetectsLastLineLostFromNewlineTerminatedMessage` is
  the failing-first regression for the exact P2-1 scenario; updated the two
  existing tests that had encoded the old floor as intentional.
- `cmd/agent-deck/session_send_test.go`:
  `TestExecuteSend_MultilineMessageRefusedOnTruncatedPasteMarker` is the
  failing-first end-to-end regression for P2-2 (confirmed failing —
  `typed_not_submitted` instead of the truncation error — with the
  `executeSend` wiring reverted); plus unit tests for
  `sendInitialKeysChecked` directly.

## VERIFY

- Host: `go build ./...` and `go vet ./...` — clean, one build at a time.
- Docker (`agentdeck-gotest:1.25-tmux`, `--init`, lock at
  `/tmp/agentdeck-docker.lock.d`):
  `go test -timeout 25m ./internal/tmux/... ./internal/session/... ./internal/ui/... ./cmd/agent-deck/...`
  — all green except the pre-declared known-pre-existing failures:
  `TestKill_LiveSessionThenSecondKillBothSucceed`,
  `TestStartupNamePermissionChangeFailsClosed`,
  `TestDeployScript_NonRootKeepsGroup`,
  `TestDeployScript_NonRootGroupFailureAborts`,
  `TestWriteJSONFileAtomic_SkipsUnchangedWrite` (the 5 root-permission
  tests), `TestCleanupReviewCrossProfileBoundary`. `internal/ui` was fully
  green on both runs (an earlier `TestStorageWatcherBuildCacheDefaultsSurviveHomeIsolation`
  cleanup flake — `TempDir RemoveAll: directory not empty` — did not
  reproduce in isolation or on the final run; confirmed environmental, not a
  regression).
- `bash tests/ci/structure-advisory-workflow.test.sh` — 8/8 PASS.
- code-simplifier dispatched twice (before the final Docker run each time):
  once over items 1–3's diff, once over item 4's diff. Both passes made
  small, behavior-preserving cleanups (deduped kill/log helper in tmux.go,
  collapsed duplicate CLI dispatch, deduped purge loops, deduped the
  identical pre-Enter paste-truncation check into `send.PasteTruncationCheck`)
  — see individual commits for detail.
- Alt+D TUI panel: golden frame captured (`frame-altd-panel.txt`), list and
  show-detail views, seeded with two dead-letter records.

## Safety

No writes to `~/.agent-deck` beyond nothing (all work happened in a fresh
clone under `/private/tmp/exec-fix-verify-gaps-r2/src`); no `rm` (used
`trash`); no `claude -p`; no push; no GitHub writes (`gh pr view --comments`
and `gh issue view` were read-only lookups).
