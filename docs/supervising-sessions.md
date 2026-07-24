# Supervising sessions

How to drive other sessions from a supervisor (a watchdog loop, a conductor, an
orchestrator agent) without silently losing messages.

## The failure this page exists for

On 2026-07-24 an orchestrator session hit:

```
API Error: Unable to connect to API (ConnectionRefused)
```

Its turn state machine never returned to idle. The pane kept repainting and kept
**accepting keystrokes** — text typed into it appeared in the composer normally —
but the submit handler stayed gated, so **Enter was a no-op**. The session sat
wedged for an hour with three children idling behind it.

Two things went wrong, and they compound:

1. **The wedge was invisible.** Every status heuristic reads one pane capture,
   and in a single frame a wedged session is indistinguishable from a healthy
   one: quiet pane, visible prompt. agent-deck reported `idle-at-empty-prompt`
   (the prompt was not empty), and its coarse status flapped
   `running`↔`waiting` several times a minute.

2. **The supervisor reported success anyway.** Its watchdog ran:

   ```bash
   agent-deck session send "$SID" "Auto-nudge..." >/dev/null 2>&1
   nudges=$((nudges+1)); echo "NUDGED ... (nudge #$nudges)"
   ```

   `session send` had done its job — it detected the message sitting unsent and
   exited nonzero with `delivery=typed_not_submitted` — but stdout, stderr and
   the exit code were all discarded, and `NUDGED` printed unconditionally. The
   watchdog claimed 56 nudges against a session that had received 53.

## Use `session nudge`, not bare `session send`

```
agent-deck session nudge <id|title> <message> [--json] [--force]
```

A nudge is a send **with preconditions and a verified outcome**. The exit status
is the contract:

| Exit | Meaning |
|------|---------|
| `0`  | delivered, **or** skipped because the target is busy — check `outcome` |
| `1`  | not delivered: stalled, not running, or typed but never submitted |
| `2`  | session not found |

```bash
if agent-deck session nudge "$SID" "resume supervising" --json; then
  : # delivered, or the session was already working
else
  : # escalate — do NOT count this as a nudge
fi
```

The `--json` payload always carries `outcome` (`delivered`, `skipped_busy`,
`refused_stalled`) and, on a send attempt, `delivery` (`submitted`,
`typed_not_submitted`, `no_evidence`). The envelope is flat — there is no `data`
wrapper. Send `--json` with `2>/dev/null`: stderr carries free-text warnings that
break a strict parse.

Why the verbs differ:

- **busy is not an error.** A polling watchdog hits a working session constantly;
  reporting that as a failure trains people to ignore the exit code, which is the
  habit that caused the incident. `skipped_busy` exits 0 with `delivered: false`.
- **stalled is refused, not retried.** A gated composer swallows everything sent
  to it. Re-sending only queues more text it cannot submit.

`--force` bypasses the busy and stalled gates for a deliberate operator
override. It cannot conjure a pane: a session that is not running is still
refused.

## The `stalled` substate

`stalled` is an additive Honest-Status-v2 substate (it never changes the
canonical status string). It means: **the composer holds unsent text that has
not changed for `StallDwell` (10 minutes) while nothing is running.**

Only time distinguishes a wedge from a healthy prompt, so the detector tracks
dwell on the composer draft:

| Observation | Verdict |
|---|---|
| composer holds text, text keeps changing | a human is typing — not stalled |
| composer holds text, text frozen for the dwell | **stalled** |
| composer empty | genuinely idle |
| anything running or erroring | not applicable, left alone |

It surfaces in `session show --json` (`substate`), in `list --json`, and as a
`🧊` glyph in the TUI. It is **reporting only** — nothing recovers a session
automatically from this signal, because the draft in a stalled composer might be
an operator's, and submitting someone else's text is not a status probe's call.

The render hot path reads dwell state from memory and never adds a pane capture,
so stall awareness costs nothing per row per tick.

## Recovering a wedged pane

**Escape, then Enter.**

Escape releases the gate (it does not visibly clear the stale spinner, and it
preserves the composer text); the following Enter submits. If Escape clears the
composer on your version of the tool instead, retype the message.

`session send` now does this automatically: after three consecutive checks where
the composer still holds the message and plain Enter has been refused, it
escalates to Escape+Enter, bounded at two attempts. That turns the hour-long
outage above into a few seconds of self-healing. The escalation only ever fires
on a message agent-deck itself just typed — the operator-draft guard has already
saved and cleared any human draft before that point — so it can never Escape
away someone's in-flight work.

If both recovery attempts fail, the send still reports `typed_not_submitted`
honestly, and the pane needs a manual Escape+Enter or a restart.

## Checklist for a watchdog loop

- [ ] Use `session nudge`; branch on the exit code.
- [ ] Never `>/dev/null 2>&1` a delivery command.
- [ ] Only count a nudge when `outcome` is `delivered`.
- [ ] Treat `refused_stalled` as an escalation, not a retry.
- [ ] Reconcile your own counter against reality periodically — for Claude
      targets, `grep -c` your nudge phrase in
      `~/.claude/projects/<proj>/<session-uuid>.jsonl`. A drift between "nudges
      I think I sent" and "prompts the target received" is the cheapest possible
      detector for this entire class of bug, and it is what exposed the original
      56-vs-53.
