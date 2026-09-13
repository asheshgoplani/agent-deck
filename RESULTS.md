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

1. **Queued is not a snapshot.** #2043 returned `queued` the moment the hook
   read busy, before any evidence the body arrived. Now `queued` requires the
   hook to have read busy *before* the send and token movement after it: a
   new copy of the body, or a new composer paste marker, relative to the
   pre-send pane baseline (the same delta idiom `verifyContentArrival` uses).
   Hook busy with no arrival keeps today's failure verdict; an identical body
   already on screen (heartbeat) is not movement. Hook idle before the send
   and busy after, with the body landed, is `submitted`.
2. **Interrupts.** No automated path sends Ctrl-C (inherited from #2263);
   every busy-target test asserts zero `SendCtrlC` calls and exactly one
   `SendKeysAndEnter`.
3. **Turn identity freshness.** `--wait`/`--stream` bind to the transcript
   user record of the exact message. The transcript path unknown before the
   send (fresh session) is resolved after it and searched from offset 0 under
   a `NotBefore = sentAt - 2s` guard, so an older identical prompt is never
   adopted; #2043 exited 1 there. Prompt comparison is whitespace/CRLF
   normalised. Slash commands (recorded by Claude as `<command-name>` meta
   records) and non-Claude tools keep the timestamp path.
4. **One `--timeout` budget** shared by identity, completion and reply
   (#2043 spent it up to three times). A reply with text but no end-of-turn
   at the deadline returns as incomplete with a warning; `stop_sequence` and
   `max_tokens` end a turn like the streamer.
5. **Stream errors** before streaming are emitted as JSONL error events
   (colliding transcript, identity failure), never a bare exit.
6. **Conductor delivery-evidence check.** Swept every `sentAt` consumer in
   the tree: the only remaining ones are the `last_sent_at` self-heal clock
   (a dwell anchor, not reply selection) and `waitForFreshOutput`, which is
   now reached only for non-Claude tools and slash commands. No conductor
   path selects a reply by timestamp.

## Test evidence

All `go test` runs in `golang:1.25` under Docker (`--network none`,
`--cap-drop ALL`, unprivileged); the host ran only `gofmt`, `go build`,
`go vet`.

### Red (failing first)

Against the rebased #2043 head (before the round-2 commit), the new CLI tests:

```
--- FAIL: TestIssue1978_HookBusyWithoutArrivalIsNotQueued
    delivery = queued with the body never on screen — hook-busy is not arrival evidence
--- FAIL: TestIssue1978_StaleIdenticalBodyIsNotTokenMovement
    delivery = queued from a pre-existing copy of the body; want the token count to move (#876 phantom)
--- FAIL: TestIssue1978_HookIdleBeforeSendThenBusyIsSubmitted
    delivery="queued" err=<nil>, want submitted
--- FAIL: TestIssue1978_NoWaitQueuedNeedsArrivalToo
    --no-wait dropped: delivery="queued" err=<nil>, want a failure verdict
--- FAIL: TestIssue1978_NonClaudeArrivalPathReportsQueuedWhenHookBusy
    delivery="typed" ... want queued
```

On unpatched `main` none of the new tests compile (`deliveryQueued`,
`TurnQuery`, `awaitClaudeTurnReply` do not exist).

Mutation proof on the final branch (drop the `NotBefore` guard, hand the reply
phase a fresh 10s budget, drop the token-movement gate), tests only:

```
--- FAIL: TestIssue1978_IdentityRejectsOlderIdenticalPromptBeforeSentAt
    bound to "old-heartbeat", want the record written after the send
--- FAIL: TestIssue1978_HookBusyWithoutArrivalIsNotQueued
--- FAIL: TestIssue1978_StaleIdenticalBodyIsNotTokenMovement
--- FAIL: TestIssue1978_NoWaitQueuedNeedsArrivalToo
--- FAIL: TestIssue1978_WaitReplyHonoursOneDeadline
    reply phase ran 10.148106255s past a 300ms shared deadline
```

### Green

- `go test -race -count=3 ./internal/session/... ./cmd/agent-deck/... -run
  'TestIssue1978|TestInterrupt|TestNoWaitClassifies|TestTurnIdentity|TestAwaitTurn|TestStreamTranscript|TestResend|TestNoResend|TestIssue2104|TestWaitForFreshOutput|TestSendWithRetry'`
  — both packages `ok`.
- Full `./internal/send/... ./internal/session/... ./cmd/agent-deck/...`:
  `internal/send` ok; the 43 failures in `internal/session` and
  `cmd/agent-deck` are byte-identical to unpatched `main` in the same image
  (every one is `tmux not found`; the `golang:1.25` image has no tmux).
  `comm` of the two sorted `--- FAIL` lists is empty in both directions.
- Host: `gofmt -l` clean, `go build ./...` ok, `go vet` ok on the touched
  packages.
