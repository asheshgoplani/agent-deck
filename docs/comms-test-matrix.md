# Comms Ledger test matrix

Every harness, local and remote, urgent and info: a scripted scenario that
proves produce -> ledger -> consume -> ack with timing. Automated cases run
in CI (Docker, `go test`); lab cases run against a live harness on a
machine with the binary installed and are recorded in the build worker's
RESULTS. The matrix grows with each phase; the status column says where a
cell stands today.

Legend: **auto** = a Go test in this repository runs the scenario;
**lab** = a scripted manual run (command and expected output below);
**P2** / **P3** = the stage that adds the consume/ack or remote leg.

## Scenario shape

1. Produce: the harness finishes a turn (the hook fires with the text), or
   a status edge is observed (shell).
2. Ledger: the notify daemon drains the spool and commits one `turn` (or
   `status`) record with `tier`, `trigger`, `text`, `t_signal`, `t_record`.
3. Consume: the parent's next prompt carries the record (prompt-time
   injection) or a typed nudge wakes it; `t_pushed` / `t_seen` are stamped.
4. Ack: the consumer advances its cursor; `msg stats` shows the latency.

Timing: `latency_ms` (signal to commit) is on every record; P2 adds
`t_pushed` and `t_seen` so signal-to-seen is measured per record.

## Matrix

| Harness | Leg | Tier | Produce -> ledger | Consume -> ack | Status | Test |
|---|---|---|---|---|---|---|
| Claude | local | urgent | human prompt, Stop with text (fixtures `claude_stop_v1.json`, `claude_userpromptsubmit_v1.json`) | UPS additionalContext | **auto** (P0+P1) / P2 | `TestCommsIngest_ClaudeTurnMatchesTheInboxClassification`, `TestHookHandler_ClaudeFixturesSpoolBothEdges`, `TestCommsIngest_LongClaudeReplyStillMatchesItsTranscriptTurn`, `TestCommsIngest_SameTextClaudeBacklogIsThreeRecords` |
| Claude | local | info | task-notification turn, Stop with text | rides next turn / digest | **auto** / P2 | same, plus `TestCommsIngest_ClaudeBacklogKeepsEveryTurnsOwnText` (daemon down, three turns queued) |
| Claude | remote | urgent, info | same on the remote; `msg export` over ssh; `Import(origin)` | as local, origin shown | **auto** (import semantics) / P3 (transport) | `TestRemoteFirstExportImportIsIdempotentPerOrigin` |
| Codex | local | urgent | `[agent-deck from:]` prompt, notify with `last-assistant-message` (fixture `codex_notify_v1.json`); a Codex Stop hook never spools (fixture `codex_stop_v1.json`) | typed nudge when idle | **auto** / P2 | `TestCommsIngest_CodexTurnTakesTriggerFromThePromptEdge`, `TestCodexNotify_FixtureSpoolsLastAssistantMessageOnce` |
| Codex | local | info | `[HEARTBEAT]` prompt, notify with text | next typed nudge bundle | **auto** / P2 | same |
| Codex | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Gemini | local | urgent | **status-only in P1** (text producer specified: BeforeAgent prompt, AfterAgent `prompt_response`; needs its own fixture + lab before enablement) | BeforeAgent additionalContext (P2) | **auto** (status record) / deferred | `TestCommsIngest_StatusEdgesCollapseByContentNotTime` (gemini row), `TestHookHandler_OnlyClaudeSpoolsInP1` |
| Gemini | local | info | n/a until the producer is enabled | | deferred | |
| Gemini | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Cursor | local | urgent | **status-only in P1** (text producer specified: beforeSubmitPrompt, afterAgentResponse `text`, which needs one more event in the installed hooks.json and a lossless merge; deferred) | stop `followup_message` / typed nudge (P2) | **auto** (status record) / deferred | `TestHookHandler_OnlyClaudeSpoolsInP1` |
| Cursor | local | info | n/a until the producer is enabled | | deferred | |
| Cursor | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Pi | local | urgent | **status-only in P1** (text producer specified: extension v3 with `input` prompt and `agent_settled` text; deferred, needs the re-versioned extension and lab L1) | extension `pi.sendUserMessage()` (P2) | **auto** (status record) / deferred | `TestHookHandler_OnlyClaudeSpoolsInP1` |
| Pi | local | info | n/a until the producer is enabled | | deferred | |
| Pi | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Hermes | local | urgent | **status-only in P1** (text producer specified: pre_llm_call `user_message`, post_llm_call `assistant_response`; the shell-hook payload shape is unverified) | pre_llm_call `{context}` / typed nudge (P2) | **auto** (status record) / deferred | `TestHookHandler_OnlyClaudeSpoolsInP1` |
| Hermes | local | info | n/a until the producer is enabled | | deferred | |
| Hermes | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| OpenCode | local | urgent | **status-only in P1** (text producer specified: root idle with a 2 s debounce and `GET /session/:id/message`, stream-decoded; deferred, needs lab L2) | `POST /session/:id/prompt_async` (P2) | **auto** (status record) / deferred | `TestCommsIngest_ShellToolGetsAStatusRecordOnce` (same path) |
| OpenCode | local | info | n/a until the producer is enabled | | deferred | |
| OpenCode | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Shell | local | urgent | observed edge -> `status` record | one typed line | **auto** / P2 | `TestCommsIngest_ShellToolGetsAStatusRecordOnce` |
| Shell | local | info | n/a (status records have no tier) | | n/a | |
| Shell | remote | urgent | as local + import | as local | P3 | matrix row only |
| any | local | off | `[comms] ledger = false`: no spool, no ledger dir | | **auto** | `TestCommsIngest_OffByDefaultWritesNothing`, `TestHookHandler_SpoolsNothingWithLedgerOff` |
| any | local | isolation | ledger disk failure leaves the inbox record and wake untouched; inbox parity with the ledger on | | **auto** | `TestCommsIngest_LedgerDiskFailureNeverTouchesTheInboxPath`, `TestCommsIngest_InboxParityWithTheLedgerOn` |
| any | local | identity | one event replayed 100 times is one record; two identical answers are two; status edges collapse by content only | | **auto** | `TestCommsIngest_OneEventReplayedAHundredTimesIsOneRecord`, `TestCommsIngest_SameTextDifferentTurnsAreTwoRecords`, `TestCommsIngest_StatusEdgesCollapseByContentNotTime` |
| any | local | ownership | a second daemon never ingests while the first owns the ledger | | **auto** | `TestCommsIngest_SecondDaemonDoesNotOwnTheLedger` |
| any | local | hostile input | symlink, oversized, malformed spool entries and traversal ids are rejected | | **auto** | `TestCommsSpoolRejectsSymlinksOversizedAndMalformedEntries`, `TestHookHandler_RejectsATraversalInstanceID` |
| primitive | | | torn tail truncated, mid-history corruption kept and skipped, owner-only modes, short write rolled back | | **auto** | `TestRecoveryTruncatesOnlyATornTailAndKeepsMidHistoryCorruption`, `TestPrivateBusCreatesOwnerOnlyFiles`, `TestCommitShortWriteIsRolledBackAndReported` |
| contract | | | receipt states, watermark + sparse acks, retention and gaps (fixtures) | | **auto** | `TestReceiptTransitionsMatchFixture`, `TestConsumerStateWatermarkAndSparseAcksMatchFixture`, `TestConsumerStateNeverSkipsPendingInfoWhenUrgentOvertakes`, `TestRetentionKeepsPendingAndReportsGaps`, `TestRecordCarriesSchemaVersionStoreAndEpoch` |
| any | local | restart | daemon restart keeps keys and sequences | | **auto** | `TestCommsIngest_RecordsSurviveADaemonRestart`, `TestLedgerCommitStampsSequencesAndDedupsByKey` |
| any | local | failure | a failed commit keeps the spool entry and its trigger; the ledger reopens | | **auto** | `TestCommsIngest_FailedCommitIsRetriedWithItsTrigger` |
| any | local | config | per-conductor `[conductors.<c>.inbox]` applies to the ledger tier | | **auto** | `TestCommsIngest_UsesTheParentConductorsInboxConfig` |
| CLI | | | `events follow\|stats --bus comms` open the ledger read-only | | **auto** | `TestOpenBusForRead` |
| primitive | | | synchronous commit, two writers, read-only follower, retention | | **auto** | `internal/events/commit_test.go` |

## Lab scripts

Run on a machine with the built binary and the harness installed, with
`[comms] ledger = true`, the notify daemon restarted, and a conductor plus
one child of the harness under test.

Lab L1 and L2 apply once the pi and OpenCode producers are enabled (not in P1).

**L1 pi (urgent, local)**
```
agent-deck launch /tmp/lab-pi -t lab-pi -c pi -m "Reply with exactly: PI READY"
agent-deck events follow --bus comms --json --session <lab-pi id>
```
Expect within 10 s of pi's prompt returning: one `turn` record, `tool: pi`,
`trigger: human`, `tier: urgent`, `text: PI READY`, `latency_ms` < 5000.

**L2 opencode (urgent, local)**
```
agent-deck launch /tmp/lab-oc -t lab-oc -c opencode -m "Reply with exactly: OC READY"
agent-deck events follow --bus comms --json --session <lab-oc id>
```
Needs a TUI open (the SSE watcher lives there) and the session launched
with a port. Expect one `turn` record with `tool: opencode`, `text: OC READY`.

**L3 any harness (info, local)**
Send `[HEARTBEAT] anything new?` with `agent-deck session send`; expect the
reply as a `turn` record with `trigger: inbox`, `tier: info`.

**L4 remote (P3)**
`agent-deck msg export --after <cursor> --json` on the remote over ssh,
import on the conductor's host; expect `origin: <remote>`, `src_cursor`
set, a second pull adds nothing.

Consume and ack rows are filled by the P2 PR (`agent-deck msg read|ack`,
`t_pushed`, `t_seen`, `msg stats`).
