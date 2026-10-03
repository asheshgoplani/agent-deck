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
| Claude | local | urgent | human prompt, Stop with text | UPS additionalContext | **auto** (P0+P1) / P2 | `TestCommsIngest_ClaudeTurnMatchesTheInboxClassification` |
| Claude | local | info | task-notification turn, Stop with text | rides next turn / digest | **auto** / P2 | same, plus `TestCommsIngest_ClaudeBacklogKeepsEveryTurnsOwnText` (daemon down, three turns queued) |
| Claude | remote | urgent, info | same on the remote; `msg export` over ssh; `Import(origin)` | as local, origin shown | **auto** (import semantics) / P3 (transport) | `TestRemoteFirstExportImportIsIdempotentPerOrigin` |
| Codex | local | urgent | `[agent-deck from:]` prompt, notify with `last-assistant-message` | typed nudge when idle | **auto** / P2 | `TestCommsIngest_CodexTurnTakesTriggerFromThePromptEdge`, `TestCodexNotify_SpoolsLastAssistantMessage` |
| Codex | local | info | `[HEARTBEAT]` prompt, notify with text | next typed nudge bundle | **auto** / P2 | same |
| Codex | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Gemini | local | urgent | BeforeAgent prompt, AfterAgent `prompt_response` | BeforeAgent additionalContext | **auto** (hook payload; ledger leg shared with the Codex ingest tests: same prompt-derived path) / P2 | `TestHookHandler_SpoolsTextPerHarness` (gemini rows), `TestCommsIngest_SameTextDifferentTurnsAreTwoRecords`, `TestCommsIngest_RecordsSurviveADaemonRestart` |
| Gemini | local | info | same with an inbox prompt | same | **partial** (spool auto; ledger leg via `TestCommsIngest_CodexTurnTakesTriggerFromThePromptEdge`) / P2 | same |
| Gemini | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Cursor | local | urgent | beforeSubmitPrompt, afterAgentResponse `text` | stop `followup_message` / typed nudge | **partial** (spool auto; ledger leg shared with the prompt-derived ingest tests) / P2 | `TestHookHandler_SpoolsTextPerHarness` (cursor rows), `TestInjectCursorHooks_LedgerGatesAfterAgentResponseAndKeepsUserFields` |
| Cursor | local | info | same | sessionStart `additional_context` | **partial** / P2 | same |
| Cursor | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Pi | local | urgent | `input` prompt, `agent_settled` text | extension `pi.sendUserMessage()` | **auto** (hook payload) / P2; **lab** (extension on a live pi) | `TestHookHandler_SpoolsTextPerHarness` (pi rows); lab L1 |
| Pi | local | info | same | same | **partial** (spool auto; ledger leg shared) / P2 | same |
| Pi | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Hermes | local | urgent | pre_llm_call `user_message`, post_llm_call `assistant_response` | pre_llm_call `{context}` / typed nudge | **auto** (hook payload) / P2 | `TestHookHandler_SpoolsTextPerHarness` (hermes rows) |
| Hermes | local | info | same | same | **partial** (spool auto; ledger leg shared) / P2 | same |
| Hermes | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| OpenCode | local | urgent | root idle (2 s), `GET /session/:id/message` | `POST /session/:id/prompt_async` / `/tui/submit-prompt` | **auto** (httptest server) / P2; **lab** L2 | `TestOpenCodeSSEWatcher_IdleSpoolsRootSessionText` |
| OpenCode | local | info | same with an inbox prompt | same | **auto** / P2 | same |
| OpenCode | remote | urgent, info | as local + import | as local | P3 | matrix row only |
| Shell | local | urgent | observed edge -> `status` record | one typed line | **auto** / P2 | `TestCommsIngest_ShellToolGetsAStatusRecordOnce` |
| Shell | local | info | n/a (status records have no tier) | | n/a | |
| Shell | remote | urgent | as local + import | as local | P3 | matrix row only |
| any | local | off | `[comms] ledger = false`: no spool, no ledger dir | | **auto** | `TestCommsIngest_OffByDefaultWritesNothing`, `TestHookHandler_SpoolsNothingWithLedgerOff` |
| any | local | restart | daemon restart keeps keys and sequences | | **auto** | `TestCommsIngest_RecordsSurviveADaemonRestart`, `TestLedgerCommitStampsSequencesAndDedupsByKey` |
| any | local | failure | a failed commit keeps the spool entry and its trigger; the ledger reopens | | **auto** | `TestCommsIngest_FailedCommitIsRetriedWithItsTrigger` |
| any | local | config | per-conductor `[conductors.<c>.inbox]` applies to the ledger tier | | **auto** | `TestCommsIngest_UsesTheParentConductorsInboxConfig` |
| CLI | | | `events follow\|stats --bus comms` open the ledger read-only | | **auto** | `TestOpenBusForRead` |
| primitive | | | synchronous commit, two writers, read-only follower, retention | | **auto** | `internal/events/commit_test.go` |

## Lab scripts

Run on a machine with the built binary and the harness installed, with
`[comms] ledger = true`, the notify daemon restarted, and a conductor plus
one child of the harness under test.

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
