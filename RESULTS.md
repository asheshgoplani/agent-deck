# Round 3 — carry/2011

Base: `carry/2011` HEAD `80b340db` (round 2, unchanged). New commit
`6c790f69` on top. Read-only clone at `/tmp/exec-carry-2011-r3/src`;
origin never touched, nothing pushed.

Review read first: `/Users/ashesh/agent-deck-recovery/plans-20260917/receipts/review-carry-2011/RESULTS.md`.
Both P1 findings and the P2 finding are addressed below.

## Findings

### P1a — `TestValidateRejectsAvailableItemsThatClaimACost` (`internal/ctxinspect/report_test.go:393`)

Real fix, not a skip. The validator's rejection was already correct — an
`Available` item reporting a nonzero actual cost is rejected — but the two
tests that pin this contract from different angles expected different
substrings in the same error:

- `report_test.go:396` expects `"certain zero"` (the value an available item
  is allowed to cost when its absence was established).
- `honest_unknown_test.go:59` expects `"positive cost"` (the breach itself).

Both are legitimate slices of the same contract, so the message now says
both instead of picking one: `internal/ctxinspect/report.go:1153` —

> "... is marked available but reports a positive cost: an available item
> may only cost a certain zero or an explicit unknown, never a positive
> number for content that is not loaded"

---

# Issue #2079 — round 2 verification results

## What round 1 missed

Round 1 (head `c14ae7a4`) guarded only the first of the two call sites the
issue names: `sendMessageWhenReady` (`internal/session/instance.go`), which
withholds Enter before it is pressed if the composer's collapsed paste marker
declares fewer lines than the message.

The second call site — the `launch --no-wait` post-send verifier,
`pollPromptConsumed` in `cmd/agent-deck/launch_verify_prompt.go` — was left
checking only "did the composer clear?". A truncated fragment that gets
submitted clears the composer exactly like a clean delivery, so this poller
reported success on the same truncation round 1 fixed at the other site.

## Fix

`pollPromptConsumed` now applies the same declared-line-count check
(`send.ExpectedPasteMarkerLines` / `send.PasteMarkerLineCounts`) once the
composer looks consumed:

- Marker declares fewer lines than the message → `promptTruncated`. Reported
  to the caller's warning writer as `"prompt truncated in transit"`; the
  retry is skipped (retyping and re-pressing Enter on an already-submitted
  fragment risks a duplicate submission, not a fix).
- Composer looks consumed but no marker is visible at all, for a message
  that expects one → `promptUnknown` (held as a render-lag candidate across
  the poll window, only classified at the deadline). Reported as
  `"...delivery unknown..."`; never reported as success.
- Marker declares at least as many lines as the message (or the message is
  single-line and never collapses behind a marker) → `promptConsumed`,
  unchanged silent success.

`verifyPromptConsumedAfterLaunchAttributed` now switches on this outcome at
both poll points (before and after the one retry) instead of treating any
"composer is empty" shape as success.

## Failing-first test

New file: `cmd/agent-deck/issue2079_launch_verify_prompt_test.go` (uses the
existing `mockSendRetryTarget` fake from `session_send_test.go`, no new test
infrastructure).

Revert proof (production logic only reverted — the declared-line-count check
in `pollPromptConsumed` was disabled to reproduce pre-fix behavior; the new
test file was left in place), run in `golang:1.25`:

```text
=== RUN   TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess
    issue2079_launch_verify_prompt_test.go:61: a truncated marker must be reported, not silently treated as success
--- FAIL: TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess (0.00s)
=== RUN   TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess
    issue2079_launch_verify_prompt_test.go:81: an unconfirmed delivery must be reported, not silently treated as success
--- FAIL: TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess (0.00s)
=== RUN   TestPollPromptConsumed_MatchingMarker_StillReportsSuccess
--- PASS: TestPollPromptConsumed_MatchingMarker_StillReportsSuccess (0.00s)
FAIL
```

With the fix restored, the same three tests plus the full pre-existing
`TestVerifyPromptConsumedAfterLaunch_*` suite in the same file pass:

```text
--- PASS: TestPollPromptConsumed_TruncatedMarker_ReportsTruncatedNotSuccess (0.00s)
--- PASS: TestPollPromptConsumed_NoMarkerObserved_ReportsUnknownNotSuccess (0.01s)
--- PASS: TestPollPromptConsumed_MatchingMarker_StillReportsSuccess (0.00s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_ConsumedFirstPoll_NoRetry_NoWarning (0.00s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_UnsentFirstWindow_RetryThenConsumed_OneRetry_NoWarning (0.03s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_UnsentBothWindows_OneRetry_WarningEmitted (0.02s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_WelcomeScreenNoComposer_NotConsumed_TriggersRetry (0.01s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_RespectsWallTimeBudget (0.06s)
--- PASS: TestVerifyPromptConsumedAfterLaunch_ForeignComposerContent_NoRetry_WarningEmitted (0.01s)
PASS
ok  	github.com/asheshgoplani/agent-deck/cmd/agent-deck	2.025s
```

`internal/send` (declaration-count primitives this test also depends on,
unchanged this round) also passes in full in the same container.

No behavior change — `Validate()`'s decision to reject is untouched, only
the message's wording.

### P1b — `TestInspectReportsALowMatchRateRatherThanGuessing` (`internal/ctxinspect/codex/adapter_test.go:271`)

Real fix. The Codex adapter already computed the correct per-file
`"agents-md-file-unmatched"` caveat when a file's on-disk bytes no longer
matched the injected block (`internal/ctxinspect/codex/adapter.go:492-497`,
unchanged this round) — but it only ever attached that caveat to the
item's own `Caveats` slice, never to the report's top-level `Caveats`. The
report-level list only carried the newly-added generic
`"disk-derived-as-of-now"` freshness caveat (`report.go:870`,
`noteDiskDerivedFreshness`), which says nothing about *which* file drifted.
The test asserts against the report-level list (`hasCaveat`/`caveatCodes`
in `adapter_test.go`), so it saw only the generic caveat and failed.

Fix (`internal/ctxinspect/codex/adapter.go:489-501`): build the
`agents-md-file-unmatched` caveat once and append it to both the item's
`Caveats` (so the row explains its own pricing when drilled into) and the
report's `Caveats` (so the CLI overview / Verify tab, which list caveats
without walking every item, name the file too). Both caveats — the generic
freshness note and the specific per-file one — are now emitted side by
side, matching the task's ask.

### P2 — footer still said `self-check:`

Real fix. `internal/ui/context_pager.go:1758` (`verdictLine`, the
always-visible footer row) now emits `"reconciliation: " +
contextReconLabel(rec)`, matching the CLI overview
(`cmd/agent-deck/session_context_render.go:154`) and the Verify tab, which
already used "reconciliation" this round.

`internal/ctxinspect/ctxtext/glossary.go` updated to match:
- The `"reconciliation / self-check"` term is now just `"reconciliation"`
  (line ~39) — the only place in the UI that ever said "self-check" was
  the footer, and that's gone now, so the glossary no longer needs to list
  two names for one concept.
- The RECON entry's parenthetical (line ~42) used to say the footer
  intentionally kept `self-check` as "a different thing entirely, which is
  why it no longer shares this abbreviation" — that sentence read as
  documenting the split on purpose, which is exactly what made this look
  intentional to a reviewer. It now says `reconciliation` (the footer's new
  label) instead, so the glossary and the screen agree.
- A stray width-comment example (`GlossaryLines`, line ~57) referenced the
  now-deleted `"reconciliation / self-check"` string as a width example;
  replaced with `"unattributed remainder"`, the actual longest term.

Test added: `TestContextPagerVerdictLineUsesReconciliationLabel`
(`internal/ui/context_pager_test.go`), asserting `verdictLine()` starts
with `"reconciliation: "` and no longer contains `"self-check"`.

No golden frame fixtures reference the footer's literal text (checked:
no `.golden`/testdata file anywhere in the repo contains `"self-check"` or
`"reconciliation:"` as a rendered TUI frame — the only byte-for-byte golden
suite, `cmd/agent-deck/session_context_golden_test.go`, asserts the JSON
`report` document, which the footer text is not part of), so there was
nothing to regenerate.

## Disclosure list

Empty. Both P1 tests fixed for real; no `t.Skip` used.

## Verify

- Host: `go build ./...` — clean. `go vet ./...` — clean.
- Docker (`agentdeck-gotest:1.25-tmux`, `--network none --cap-drop ALL -u
  1000:1000 --init`, lock acquired/released around the run):
  `go test -timeout 25m ./internal/ctxinspect/... ./internal/ui/...
  ./cmd/agent-deck/...`.
  - First attempt hit the same module-cache gap the round-2 review already
    documented: `--network none` plus an image whose baked module cache
    doesn't cover every dependency the current `go.mod` pulls in for
    `internal/ui`/`cmd/agent-deck` (`internal/session`'s newer deps:
    `al.essio.dev/pkg/shellescape`, `BurntSushi/toml`, `fsnotify`,
    `charmbracelet/bubbles` etc.) — `go test` failed at the build step with
    DNS-unreachable errors, not real test failures.
  - Fixed by mounting the **host's own** `$(go env GOMODCACHE)`
    (`/Users/ashesh/go/pkg/mod`) into the container read-only alongside the
    source, still with `--network none`: no network access is used or
    needed, the container just reads modules already present on disk. With
    that mount, every targeted package built and ran.
  - Result: all packages pass —
    `internal/ctxinspect`, `.../claude`, `.../codex`, `.../ctxfixture`,
    `.../ctxtext`, `.../sessionhost`, `.../verify`, `internal/ui` all `ok`.
  - `cmd/agent-deck`: 4 top-level test functions fail, all the known
    uid-1000/no-SSH-user sandbox limitation named in the task (`No user
    exists for uid 1000`, `remote creation catalog unavailable; check the
    SSH connection`): `TestHealthRemoteExecJSONParity`,
    `TestRemoteCommandParity`, `TestRemoteCompositionForwardsCommandHelp`,
    `TestRemoteSuccessfulMutationParity`. Every subtest under these fails
    with the same uid-1000/SSH-unreachable message — no other failures
    anywhere in the three target trees.
  - Lock directory created before the run and removed immediately after
    (both attempts).
- Read-only real-session proof: built `./out/agent-deck` from this branch
  and, separately, the unmodified round-2 binary
  (`/tmp/exec-carry-2011-r2/src`, HEAD `80b340db`) into a throwaway path.
  Ran both as `agent-deck -p personal session context
  fbfd250e-1787566889` (a real Claude session, read-only). Byte-identical
  output. The footer label only renders in the interactive TUI pager
  (`verdictLine`), which the CLI `session context` command does not print,
  so an unchanged CLI diff is exactly what "unchanged except the footer
  label" predicts — the CLI's own reconciliation line was already
  "reconciliation:" before this round (unrelated to this fix) and stayed
  so.

Nothing written outside the clone at `/tmp/exec-carry-2011-r3/src`: no
`~/.agent-deck` data touched beyond the two read-only `session context`
calls, no real `$HOME` writes, no host tmux server touched, no `rm` (no
deletions at all), no `claude -p`, no push, no GitHub write.

---

- `go build ./...`: PASS in `golang:1.25`.
- `go vet ./...`: PASS in `golang:1.25`.
- `go test ./cmd/agent-deck/...`: PASS in `golang:1.25` (includes the new
  test file and the full pre-existing suite in the same package).
- `go test ./internal/send/...`: PASS in `golang:1.25`.
- A broader `go test ./internal/tmux/... ./internal/session/...` run in the
  same stock `golang:1.25` image (not required by this round's scope, run
  for extra confidence) fails on pre-existing environment gaps unrelated to
  this change — `tmux` is not installed in the plain Go image, so every test
  that spawns a real tmux session errors with `exec: "tmux": executable file
  not found in $PATH`. Neither touched file (`launch_verify_prompt.go`,
  `issue2079_launch_verify_prompt_test.go`) is exercised by those failures.
  Same caveat round 1 documented: the authoritative full suite is CI's PR
  gate, which installs tmux.
- Host `go build ./...` and `go vet ./...`: PASS (no `go test` run on the
  host, per instructions).

## Docker serialization

The shared `/tmp/agentdeck-docker.lock.d` lock was held for the duration of
all `docker run` invocations above and removed immediately after the last
one completed.

## Scope

Only `cmd/agent-deck/launch_verify_prompt.go` (production) and
`cmd/agent-deck/issue2079_launch_verify_prompt_test.go` (new test) plus
`CHANGELOG.md`/`RESULTS.md`/`PR-BODY.md` changed this round.
`internal/send/send.go`'s `ExpectedPasteMarkerLines` / `PasteMarkerLineCounts`
(added in round 1) are reused as-is, not modified.
