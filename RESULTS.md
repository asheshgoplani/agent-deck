# Token-efficiency item #2 results

## Pre-registered endpoint

Implement only spec item #2. The primary endpoint is `list --json` calls during
conductor triage: expected to fall to zero. The outcome metric is conductor
turns per completed child task, expected to fall by 40% by replacing repeated
model-driven checks with the existing blocking
`session children --follow --until-done` stream. The guardrail is time from a
child entering `waiting` or `error` to that transition being emitted; neither
state may wait for a success sentinel or a heartbeat.

## Baseline (before code changes, 2026-08-22 UTC)

The default profile's `internal/costs`-backed `cost_events` spool was queried
read-only for the prior 14 days (2026-08-08 through 2026-08-22):

- Events/turn records: 3,641
- Input tokens: 7,284
- Output tokens: 3,399,819
- Cache-read tokens: 1,355,547,269
- Cache-write tokens: 8,170,840
- Median cache-read tokens per event/turn: 270,997

The required `agent-deck session context --json` probe exited 1 on the current
installed v1.14.0 binary with `Error: unknown session command: context`; current
`github/main` also has no `session context` handler. This absence is recorded
rather than substituting an unrelated measurement. The researched pipeline
baseline supplied by `SPEC.md` remains 115,000 median prompt tokens replayed per
conductor turn, 6.40M cache-read tokens versus 5.5k output tokens, and 98.5%
cache-read share.

For payload context, the spec's measured command sizes are 35,834 bytes for
`list --json` and 70 bytes for `status --json`. A live status probe against the
current default profile timed out after 15 seconds, so no new payload-size value
was substituted for those controlled measurements.

## Change and verification

Changed only token-efficiency item #2:

- Conductor startup and shared operating instructions now require compact
  `status --json` triage, reserve `list --json` for an explicitly requested full
  inventory, and direct in-flight child waits to one
  `session children --follow --until-done` call.
- The shell and bridge heartbeat producers still resolve the same
  per-conductor → per-profile → global `HEARTBEAT_RULES.md` precedence, but put
  only the resolved path in the heartbeat message instead of copying the file
  contents into every turn.
- No new CLI/TUI flag was added. Existing `--follow`/`--until-done` help and
  examples remain the user-facing CLI contract.

Red-path tests:

- `TestConductorInstructionsUseCompactTriageAndBlockingWait` prevents startup
  from returning to full-list triage and requires the blocking wait command.
- `TestConductorHeartbeatScript_ReferencesHeartbeatRules` and
  `TestConductorBridgeReferencesHeartbeatRulesWithoutInlining` reject either
  heartbeat producer if it reads and embeds the rules body.
- `TestRunChildrenFollowEmitsWaitingAndErrorImmediately` drives two children
  from running to waiting/error and asserts both transition events appear in
  the next poll before the final `complete` event. This is the time-to-detection
  guardrail; the test interval is 1 ms and heartbeat is deliberately one hour,
  proving detection is poll-bound rather than heartbeat- or success-bound.

Expected metric effect: triage `list --json` calls fall to zero and repeated
model polling turns are replaced by one shell wait, targeting 40% fewer
conductor turns per completed child task. The treatment will be measured after
deployment as strictly paired conductor tasks: count turns from dispatch to
terminal child state and count `list --json` tool calls in those trajectories.
Report per-task medians (and Wilcoxon signed-rank at the prescribed ladder), not
aggregate totals.

Verification (containerized Go 1.25 only):

- Focused red-path tests: pass.
- `go build ./... && go vet ./...`: pass.
- Full `./internal/session ./cmd/agent-deck` package run was attempted but is
  not a clean container target: unrelated tests require `tmux`, enforce host
  cold-start timing, or depend on non-root read-only-directory semantics. The
  failures included `tmux not found`, the pre-existing 40 ms performance gate,
  and `TestWriteJSONFileAtomic_SkipsUnchangedWrite`; none touch changed paths.
