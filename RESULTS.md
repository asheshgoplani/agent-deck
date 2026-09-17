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

## Container verification

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
