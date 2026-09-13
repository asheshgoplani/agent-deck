# PR fix/1978-send-delivery-truth verification results

Supersedes PR #2043 on current `main` (27fac197, v1.16.9). Closes #1978 and
#2033.

## Rebase evidence

- Base: `main` at `27fac197` (v1.16.9). PR #2043 head: `a55ffb5c`
  (merge-base `01c011b5`, v1.15.0).
- All four #2043 commits were cherry-picked in order with their author's
  attribution: `b1449dac`, `e93619d1`, `562ca158`, `a55ffb5c`.
- Two textual conflicts, both in `cmd/agent-deck/session_cmd.go` against
  #2263 (composer guard never interrupts; full-body Ctrl-C-then-resend
  recovery removed). Both resolved in favour of `main`: no interrupt branch
  exists to gate, so #2043's hook probe now classifies the verdict instead of
  gating a recovery. `deliveryQueued` sits beside `deliveryComposerBlocked`.
- `RESULTS.md` conflict: this file is a per-PR record; rewritten for this PR.
- The #2043 test file was adapted at the first cherry-pick: assertions that
  the Ctrl-C recovery *fires* for an idle target became assertions that it
  never fires (#2263 policy).

## Round-2 findings addressed

1. **Queued is an acknowledgement, not a snapshot.** #2043 returned `queued`
   the moment the hook read busy. Now `queued` (Claude targets only) needs
   all of: the hook read busy *before* the send, token movement after it (a
   new copy of the body relative to the pre-send pane baseline; a composer
   paste marker is never movement), the composer not holding the body, and
   Claude's own queued-messages affordance in the same frame. A target that
   was already mid-turn gets no `submitted` from the activity heuristic or
   from held-then-cleared either; it settles on turn advancement (the
   message's own user record in the transcript, `session.TurnAdvanced`), on
   the queue acknowledgement, or falls through to today's failure verdict.
   Hook idle before the send and busy after, with the body landed and the
   composer clear, is `submitted` (the hook edge is the harness's
   prompt-submit acknowledgement); that is also the only hook verdict on the
   non-Claude arrival path.
2. **Interrupts.** No automated path sends Ctrl-C (inherited from #2263);
   every busy-target test asserts zero `SendCtrlC` calls and exactly one
   `SendKeysAndEnter`.
3. **Turn identity.** `--wait`/`--stream` bind to the transcript user record
   of the exact message. With a pre-send cursor, position is the proof. With
   the path learned only after the send (fresh session), the search from
   offset 0 accepts only records whose timestamp parses and is at or after
   `sentAt` — no tolerance window, and a missing or malformed timestamp is
   rejected. Prompt comparison is whitespace/CRLF normalised. Slash commands
   (Claude records them as `<command-name>` meta records) and non-Claude
   tools keep the timestamp path.
4. **Stream boundary.** A turn-scoped stream stops with an error event when
   a later human prompt appears before end_turn (`ErrStreamTurnInterrupted`);
   the `--wait` reader already refused that boundary.
5. **One `--timeout` budget** shared by identity, completion and reply
   (#2043 spent it up to three times). A reply with text but no end-of-turn
   at the deadline returns as incomplete with a warning; `stop_sequence` and
   `max_tokens` end a turn like the streamer.
6. **Stream errors** before streaming are emitted as JSONL error events.
7. **Conductor reply attribution.** The bridge's wait path
   (`conductor_bridge.py`) returned `session output` (the latest reply)
   after `--wait -q` instead of the turn-bound stdout the CLI printed. It
   now returns that stdout and falls back to `session output` only when
   stdout is empty. The remaining `sentAt` consumers are the `last_sent_at`
   self-heal clock and `waitForFreshOutput` (non-Claude and slash commands).
8. **Truncation** (CodeRabbit on #2273). A transcript shorter than the
   pre-send cursor or a turn's start offset has lost the boundary; identity,
   reply and turn-scoped stream refuse with `ErrTranscriptTruncated` instead
   of rescanning from offset 0 and replaying earlier turns
   (`TestIssue1978_TruncatedTranscriptRefusesInsteadOfReplaying`; mutation
   back to the reset-to-zero paths fails it).
9. **Test determinism.** The partial-record case is proven by one scan
   (`scanTurnIdentity`) returning a cursor before the partial line; the
   `--wait` red-path test drives `awaitClaudeWaitReply`, the single helper
   `handleSessionSend` obtains a Claude reply from (identity, completion and
   reply in one call), rather than the phases separately.

What this does not claim: `handleSessionSend` itself is not exercised by a
test (it calls `os.Exit` and needs a tmux pane); the wiring from it to
`awaitClaudeWaitReply` and `executeSend` is reviewed, not tested.

## Test evidence

All `go test` runs in `golang:1.25` under Docker (`--network none`,
`--cap-drop ALL`, unprivileged); the host ran only `gofmt`, `go build`,
`go vet`.

### Red (failing first)

Against the rebased #2043 head (before the round-2 commit), the new CLI tests:

```
--- FAIL: TestIssue1978_HookBusyWithoutArrivalIsNotQueued
--- FAIL: TestIssue1978_StaleIdenticalBodyIsNotTokenMovement
--- FAIL: TestIssue1978_HookIdleBeforeSendThenBusyIsSubmitted
--- FAIL: TestIssue1978_NoWaitQueuedNeedsArrivalToo
--- FAIL: TestIssue1978_NonClaudeArrivalPathReportsQueuedWhenHookBusy (since replaced by ...SubmittedOnHookEdge)
```

Mutation proofs on the final branch (tests untouched):

- drop the `NotBefore` guard / fresh reply budget / drop the movement gate:
  `IdentityRejectsOlderIdenticalPromptBeforeSentAt`,
  `HookBusyWithoutArrivalIsNotQueued`, `StaleIdenticalBodyIsNotTokenMovement`,
  `NoWaitQueuedNeedsArrivalToo`, `WaitReplyHonoursOneDeadline` fail.
- Codex round (restore the 2s tolerance and pass missing timestamps, disable
  the stream boundary, drop the affordance requirement, ignore
  `turnAdvanced`, re-enable the marker-based success on the arrival path):
  `IdentityGuardRejectsRecordInsideOldToleranceWindow`,
  `IdentityGuardRejectsMissingOrMalformedTimestamp`,
  `TurnScopedStreamEndsAtInterruption`, `QueuedNeedsTheQueueAffordance`,
  `TurnAdvancementIsSubmission`, `NonClaudeNewPasteMarkerIsNeverASuccess`
  fail; re-enabling the active shortcut on a busy-before target alone fails
  `ActiveHeuristicIsNotSubmissionOnABusyTarget`.
- Bridge: `test_wait_reply_is_the_turn_bound_stdout_not_the_latest_output`
  fails on the unpatched bridge (`LATEST OTHER TURN` returned).

### Green

- `go test -race -count=2 ./internal/send/... ./internal/session/... ./cmd/agent-deck/... -run
  'TestIssue1978|TestInterrupt|TestNoWaitClassifies|TestTurnIdentity|TestAwaitTurn|TestStreamTranscript|TestResend|TestNoResend|TestIssue2104|TestWaitForFreshOutput|TestSendWithRetry|Issue1409|Issue1413|Issue876|Issue1793|Issue1855|Issue1777|Stream|Guard'`
  — all three packages `ok`.
- Full `./internal/send/... ./internal/session/... ./cmd/agent-deck/...`:
  `internal/send` ok; the failures in `internal/session` and
  `cmd/agent-deck` are byte-identical to unpatched `main` in the same image
  (every one is `tmux not found`; the `golang:1.25` image has no tmux).
- Bridge: `pytest conductor/tests/` under a throwaway HOME: the two new
  tests pass; `test_bridge_proxy.py` fails 6 cases only when run after the
  other files and passes alone, identically on the unpatched tree
  (pre-existing ordering dependence).
- Host: `gofmt -l` clean, `go build ./...` ok, `go vet` ok on the touched
  packages, `py_compile` ok on the bridge.
