# Goal — goal-driven worker autonomy

A small framework that lets an agent-deck user say "pursue this goal until done or stuck, nudge yourself with context if you stall, escalate to me if you can't make progress" — and have it actually happen without re-prompting.

This is the **next layer on top of** the [Self-Improvement pipeline](self-improvement.md). Self-improvement is post-hoc analysis of what already happened. Goal is the live mechanism that prevents the kinds of stalls self-improvement keeps surfacing.

## Why this exists (evidence from real transcripts)

The Self-Improvement run on the agent-deck conductor surfaced one specific stall pattern with overwhelming evidence:

| Conductor transcript | Hourly self-checks fired | Identical `[STATUS]` responses |
|---|---|---|
| 0a1f3a0b | 5 | yes |
| b6959397 | 5 | yes |
| bf3cdf01 | 15 | yes |
| b9403638 | **18** | yes |

Eighteen hours of cron firing the same prompt, the conductor returning the same `[STATUS]` reply, with no progress and no real escalation. The conductor literally said `next=finish completes -> ESCALATE for user` and didn't actually escalate, because the string "ESCALATE" had no mechanism behind it.

The diagnosis from the FINDINGS:

- **No external done-condition.** The conductor trusted agent-deck's `status: running` field, which means "tmux pane alive" not "work happening."
- **No external verifier.** No process independent of the conductor was checking whether the goal had been achieved.
- **No real escalation channel.** "ESCALATE for user" was text in a status reply, not a Telegram push.
- **No worker driving.** When the conductor noticed stale outputs, it didn't poke the worker — it just reported them.
- **No tactic-change enforcement.** The Behavior Rule "after 3 identical NEEDs change tactic" was a policy without a mechanism.

Goal fixes each of these by **separating the three concerns** that today are collapsed into one entity.

## Design principles

1. **Three entities, not one.** Worker (does the work). Verifier (judges done). Manager (decides when to nudge). Collapsing any two into one creates conflict of interest — the bug we observed for 18 hours.

2. **Done-conditions are external shell commands.** Not LLM judgment. Not the worker's self-assessment. A one-line shell command the manager runs independently. If you can't write the done-condition as a shell command, the goal is too fuzzy for autonomous goal. That's a forcing function, not a limitation.

3. **Progress receipts are the contract.** Each worker cycle must write one tangible artifact (a line in `task-log.md`, a commit, a comment, a file). The manager judges progress by receipts, not by anything the worker says about itself. No receipt = no progress.

4. **Nudges are context-rich.** A nudge that says "still working?" is worse than one that says "no receipt in 1h, last attempt was X, try Y or write STUCK with reason." The first reinforces the stall; the second breaks it.

5. **Escalation is mechanical.** When N nudges produce no receipts, the manager pages you via Telegram with a structured stuck-bundle (last receipts, current state, recent transcript snippet). No "ESCALATE for user" placeholders — real pushes.

6. **Goals feed back into self-improvement.** Each completed or failed goal is one structured data point. Patterns across N goals surface as findings: "Goals of type X stall at cycle 5 in 7/10 cases" → a filable bug or skill gap.

7. **Single responsibility for the conductor.** The conductor remains a status-reporter and orchestrator. When it identifies a goal worth pursuing, it *delegates* to the goal manager and goes back to its other duties. The conductor is no longer also the pursuer.

## The three entities

```
   ┌──────────────────────────────────────────────────────────────────┐
   │  WORKER                                                          │
   │  An agent-deck session spawned via `agent-deck launch`.          │
   │  Each cycle:                                                     │
   │    1. Run done_cmd. If exit 0 → write "DONE: <evidence>" and exit│
   │    2. Take ONE bounded step toward the goal                      │
   │    3. Append a progress receipt to task-log.md                   │
   │    4. Schedule next wake (ScheduleWakeup)                        │
   │  Cannot decide it's done. Cannot decide to escalate.             │
   └──────────────────────────────────────────────────────────────────┘
                                  │
   ┌──────────────────────────────────────────────────────────────────┐
   │  VERIFIER                                                        │
   │  An external shell command (or short script). NOT an LLM.        │
   │  Returns exit 0 when done, non-zero otherwise.                   │
   │  Run independently by the manager — never trusted to the worker. │
   │  Examples:                                                       │
   │    gh release view v1.6.0 --json publishedAt | jq -e '...'       │
   │    test -f /path/to/expected-artifact.txt                        │
   │    curl -sf https://api/health | jq -e '.status == "ok"'         │
   └──────────────────────────────────────────────────────────────────┘
                                  │
   ┌──────────────────────────────────────────────────────────────────┐
   │  MANAGER                                                         │
   │  A small Python daemon. Cron'd, ~150 lines, no LLM.              │
   │  Every check_interval (default 5 min), for each active goal:  │
   │    1. Run verifier (done_cmd).                                   │
   │       If exit 0 → mark done, stop worker, write done-artifact,   │
   │                   push "✅ Done: <goal>" via Telegram.            │
   │    2. Read task-log.md tail; compare to last_seen_receipt_ts.    │
   │       If newer receipt → reset nudge counter, continue.          │
   │       If no new receipt for max_idle minutes → send nudge.       │
   │    3. Nudge content is templated and context-aware:              │
   │       "No progress receipt since {ts}. Last step: {snippet}.     │
   │        Try a different angle, or write STUCK: <reason> + exit."  │
   │    4. If nudges_sent > escalate_after → push Telegram bundle.    │
   │       Bundle includes goal, last receipts, recent transcript,    │
   │        agent-deck status, worker session id.                     │
   │    5. If cycles > max_cycles → stop worker, mark failed.         │
   └──────────────────────────────────────────────────────────────────┘
```

## Goal registry schema

A goal is a single JSON file at `~/.agent-deck/goals/<id>.json`. The registry is just the directory listing.

```json
{
  "id": "v160-release-2026-04-16",
  "goal": "Ship agent-deck v1.6.0 to GitHub Releases",
  "done_cmd": "gh release view v1.6.0 -R asheshgoplani/agent-deck --json publishedAt | jq -e '.publishedAt != null'",
  "worker_session_id": "8e86ce6c-...",
  "worker_session_title": "v160-release",
  "conductor": "agent-deck",
  "workdir": "/home/ashesh-goplani/agent-deck",

  "schedule": {
    "check_interval_seconds": 300,
    "max_idle_seconds": 3600,
    "max_cycles": 24,
    "escalate_after_stuck_nudges": 3
  },

  "state": {
    "status": "active",
    "created_at": "2026-04-16T10:00:00Z",
    "last_verified_at": "2026-04-16T10:05:00Z",
    "last_receipt_seen_at": "2026-04-16T10:02:00Z",
    "last_receipt_text": "Tagged v1.6.0 locally, pushed to origin",
    "cycles_completed": 4,
    "nudges_sent": 0,
    "escalated_at": null,
    "ended_at": null,
    "ended_reason": null
  },

  "history": [
    {"ts": "...", "event": "spawned", "detail": "session_id=..."},
    {"ts": "...", "event": "receipt", "detail": "Tagged v1.6.0 locally"},
    {"ts": "...", "event": "verifier_check", "detail": "exit=1, not done"},
    {"ts": "...", "event": "nudge_sent", "detail": "Idle for 65 min..."}
  ]
}
```

Status values: `active`, `done`, `failed`, `escalated`, `stopped_by_user`.

## Worker contract — the launch prompt

This is what the worker sees on first wake (and on every subsequent ScheduleWakeup re-fire of the same prompt). It's a *contract*, not a request.

```
You are pursuing a goal autonomously. Your contract:

GOAL: {goal}

DONE-CONDITION (external shell, run by the manager):
  {done_cmd}

PROTOCOL — execute exactly:

  0. PRELUDE READS. Before any Edit/Write this cycle, Read every file
     you intend to modify. Read calls do NOT count against scope; they
     prevent Claude Code's "Read-before-Edit" tool guard from firing
     mid-cycle (which costs a wake and corrupts the receipt). Skip only
     for paths you will create fresh via Write to a new path. (#968)

  1. Check current state. Do NOT run the done-condition yourself; that's
     the manager's job. Read task-log.md to recall what you've already done.

  2. Take ONE bounded step toward the goal. Examples of "bounded":
     - run one CI check
     - file one PR comment
     - apply one patch and commit
     Do NOT do open-ended work; the manager will wake you again.

  3. Write a progress RECEIPT to task-log.md. Format:
        ## {ISO timestamp}
        - cycle: {N}
        - changed: <what concretely changed>
        - next: <what the next step is>
        - blockers: <none | description>

  4. If the step you tried produced NO concrete change, AND the same
     blocker has appeared in your last 2 receipts, write STUCK:
        ## {ts}
        - STUCK: <one-line reason>
        - context: <pointers to relevant files / sessions / errors>
     Then exit. Do NOT keep cycling.

  5. Schedule your next wake via ScheduleWakeup(delaySeconds={interval}).
     If the goal might be achieved by external events (CI, PR merge,
     release pipeline), use a longer interval.

CONSTRAINTS:
  - You may NOT decide you're done. Only the manager's verifier decides.
  - You may NOT escalate to the user yourself. The manager does that.
  - You MUST write a receipt every cycle, even if it's "investigated X, ruled out Y".
  - Bash work that mutates state should happen in a child worker session
    you spawn, not in your own shell.

GOAL ID: {goal_id}
RECEIPT PATH: {workdir}/task-log.md
```

`★ Insight ─────────────────────────────────────`
- **The contract is intentionally narrow.** It removes degrees of freedom that today's conductor has — specifically, "decide if I'm done" and "decide to escalate." Those caused 18 hours of stalls. Removing them isn't dumbing the worker down; it's specializing it.
- **Bounded steps prevent open-ended thinking-loops.** A common failure mode of autonomous agents is "let me think about this more deeply" — which produces nothing. One bounded step + receipt = forced progress per cycle.
- **The receipt format is rigid on purpose.** When the manager parses task-log.md, it needs to reliably find the last receipt. Rigid format = reliable parsing. The "changed/next/blockers" trio also forces honest self-reporting at each step.
`─────────────────────────────────────────────────`

## Manager loop — exact algorithm

```python
# Pseudocode for the manager daemon (cron'd every check_interval)

for goal_file in glob("~/.agent-deck/goals/*.json"):
    goal = load(goal_file)
    if goal.state.status != "active":
        continue

    # Step 1: external verifier
    rc = subprocess.run(goal.done_cmd, shell=True, timeout=30).returncode
    record_event(goal, "verifier_check", f"exit={rc}")
    if rc == 0:
        finalize(goal, status="done", reason="verifier passed")
        stop_worker(goal.worker_session_id)
        notify_user(f"✅ Done: {goal.goal}")
        save(goal_file, goal)
        continue

    # Step 2: look for new receipts
    last_receipt = parse_task_log_tail(goal.workdir + "/task-log.md")
    if last_receipt and last_receipt.ts > goal.state.last_receipt_seen_at:
        goal.state.last_receipt_seen_at = last_receipt.ts
        goal.state.last_receipt_text = last_receipt.summary
        goal.state.cycles_completed += 1
        goal.state.nudges_sent = 0  # progress resets nudge count
        record_event(goal, "receipt", last_receipt.summary)
    else:
        idle = now() - goal.state.last_receipt_seen_at
        if idle.total_seconds() > goal.schedule.max_idle_seconds:
            # Step 3: nudge
            goal.state.nudges_sent += 1
            nudge = build_nudge(goal, last_receipt, idle)
            agent_deck_session_send(goal.worker_session_id, nudge,
                                     no_wait=True)
            record_event(goal, "nudge_sent", nudge[:80])

            # Step 4: escalate if nudges aren't working
            if goal.state.nudges_sent >= goal.schedule.escalate_after_stuck_nudges:
                escalate_to_user(goal, idle)
                goal.state.status = "escalated"
                goal.state.escalated_at = now()

    # Step 5: hard cycle cap
    if goal.state.cycles_completed >= goal.schedule.max_cycles:
        finalize(goal, status="failed", reason="max_cycles_exceeded")
        stop_worker(goal.worker_session_id)
        notify_user(f"⚠️ Goal '{goal.goal}' hit max cycles, stopping.")

    save(goal_file, goal)
```

## Nudge generator — context-aware

The nudge content is computed from the goal state. Template:

```
[GOAL NUDGE - cycle {N}, nudge {M}/{escalate_after}]

No progress receipt for {idle_minutes} min on goal:
  "{goal}"

Last receipt ({ago}):
  changed: {last_receipt.changed}
  next:    {last_receipt.next}
  blockers: {last_receipt.blockers}

Done-condition status: NOT YET MET (manager just checked).

Pick ONE of the following:
  a) Try a different angle on '{last_receipt.next}'. The previous angle
     didn't produce a receipt — what's a different decomposition?
  b) If you're blocked on something external (CI, review, third-party),
     verify it's actually blocking (don't assume — check), then write
     STUCK with the specific external blocker.
  c) If you've genuinely tried everything, write STUCK: <reason> and exit
     cleanly. The manager will escalate to the user.

Reminder of your contract: ONE bounded step + ONE receipt this cycle.
Do not investigate forever; pick (a), (b), or (c) within 5 minutes.
```

`★ Insight ─────────────────────────────────────`
- **The nudge offers three explicit choices instead of an open question.** Open questions ("are you stuck?") let the agent ramble. Multiple-choice forces a decision. This is a known technique from human cognitive science applied to LLM prompting.
- **The "(c) write STUCK" option is the safety valve.** Without it, the worker has no honest way to admit failure — it'll just keep cycling. With it, "I tried and can't" becomes a structured signal the manager can act on.
- **The 5-minute time-box** prevents the most common LLM stall: spending an entire cycle "investigating" without taking any action. Investigation is fine; investigation-without-action is the stall pattern.
`─────────────────────────────────────────────────`

## Escalation bundle

When the manager escalates, it pushes a Telegram message to the conductor's bot. The message includes a compact summary; details live in a file the user can `cat` or attach.

```
⚠️ Goal escalated: {goal}
Status: stuck after {nudges_sent} nudges, no receipt in {idle_h} hours.

Worker:  {worker_session_title} ({worker_session_id})
Last receipt ({ago}):
  changed:  {last_receipt.changed}
  next:     {last_receipt.next}
  blockers: {last_receipt.blockers}

Verifier: {done_cmd}
  Last check: NOT MET ({last_verified_ago})

Recent attempts:
  - {history[-4].ts} {history[-4].event}: {history[-4].detail[:60]}
  - {history[-3].ts} {history[-3].event}: {history[-3].detail[:60]}
  - {history[-2].ts} {history[-2].event}: {history[-2].detail[:60]}
  - {history[-1].ts} {history[-1].event}: {history[-1].detail[:60]}

Full bundle: ~/.agent-deck/goals/{id}.json
Worker output: agent-deck session output {worker_session_title} -q

Options:
  - Resume: agent-deck session send {worker_session_title} "<your hint>"
  - Kill:   agent-deck goal cancel {id}
  - Take over manually
```

## Done-condition guidelines

Done-conditions must be:

- **Externally verifiable.** A shell command the manager can run independently of the worker.
- **Idempotent.** Running it multiple times must be safe and cheap.
- **Fast.** Returns within 30 seconds. If your check needs longer, wrap it in a cached helper.
- **Honest about partial success.** "Mostly done" should still be done=false. Be strict.

### Examples that work

| Goal | Done command |
|---|---|
| Ship v1.6.0 release | `gh release view v1.6.0 -R asheshgoplani/agent-deck --json publishedAt \| jq -e '.publishedAt != null'` |
| Get PR #890 merged | `gh pr view 890 -R asheshgoplani/agent-deck --json mergedAt \| jq -e '.mergedAt != null'` |
| Apply migration X to prod DB | `psql $PROD_DB -tAc "SELECT applied FROM schema_migrations WHERE id='X'" \| grep -q t` |
| Generate report file | `test -s /tmp/report-{date}.csv && [ $(wc -l < /tmp/report-{date}.csv) -gt 100 ]` |
| CI passing on branch X | `gh run list -b X -L 1 --json conclusion \| jq -e '.[0].conclusion == "success"'` |

### Examples that DON'T work

| Goal | Why bad |
|---|---|
| "Get this working" | Not testable. Refuse — too fuzzy. |
| "Make the code better" | Not measurable. Refuse. |
| "Worker says it's done" | Self-judgment. The whole point of this design is to avoid this. |
| "Tests pass and code looks good" | "Looks good" not externally verifiable. Drop the second clause. |

## Failure modes and recovery

| Failure | Recovery |
|---|---|
| Worker session crashes mid-cycle | Manager detects no new receipt → nudge cycle continues normally. Manager can also restart the session via `agent-deck session restart`. |
| Verifier command itself errors (e.g., `gh` rate-limited) | Manager records `verifier_error`, continues without flipping status. After N consecutive verifier errors, escalates differently: "can't verify, please check". |
| Telegram push fails | Manager logs locally, retries on next cycle. Goal doesn't progress to next state until escalation actually delivers. |
| `task-log.md` doesn't exist or is corrupt | Manager treats as "no receipt", nudges normally. After 1st nudge, the worker should recreate it. |
| Cycle count exceeded but goal isn't done | Hard stop. `failed` status. Push final Telegram. Don't silently retry. |
| Two goals target the same worker | Refuse at create-time. One goal per worker session. |
| Done-condition is buggy (always returns 0) | The goal completes spuriously. Manual recovery: user reads the done-condition before approving the goal. (Future: dry-run mode that runs done_cmd on creation and asks "this currently returns: <output>. Is that what 'done' looks like?") |

## Integration with existing agent-deck primitives

| Existing primitive | How goal uses it |
|---|---|
| `agent-deck launch -m "<prompt>"` | Spawn the worker with the goal contract baked in |
| `ScheduleWakeup(delaySeconds=N)` | Worker self-schedules between cycles |
| `agent-deck session send <id> "<msg>" --no-wait` | Manager sends nudges (non-blocking, doesn't compete with worker cycles) |
| `agent-deck session output <id> -q` | Manager can fetch transcript snippets for the escalation bundle |
| `agent-deck session stop <id>` + `rm <id>` | Manager finalizes goal, cleans up worker session |
| Conductor's CLAUDE.md Behavior Rule #10 | Goal *implements* this rule — "after 3 unchanged NEEDs change tactic" becomes "after `escalate_after_stuck_nudges` nudges, escalate to user with bundle" |
| Conductor's `state.json` / `task-log.md` | Goal reuses task-log.md as the receipt store. The conductor's own state stays separate. |
| Telegram bot per conductor | Manager uses the same channel for escalations |

## CLI surface

The full command (proposed):

```bash
agent-deck goal \
    [--id <slug>] \                  # optional; auto-generated if omitted
    --goal "<one sentence>" \
    --done '<shell command>' \
    [--check-every 5m] \             # how often the manager polls
    [--max-idle 1h] \                # before sending a nudge
    [--max-cycles 24] \              # hard cap
    [--escalate-after 3] \           # nudge count before paging user
    [--conductor <name>] \           # which conductor owns this
    [--workdir <path>] \             # cwd for the worker session
    [--worker-tool claude] \         # which agent to spawn (claude/codex/gemini)
    [--dry-run]                      # just print what would happen
```

Companion commands:

```bash
agent-deck goal list                  # show active goals + their state
agent-deck goal show <id>             # full JSON dump of one goal
agent-deck goal tail <id>             # tail the worker's task-log.md
agent-deck goal cancel <id>           # stop the worker, mark stopped_by_user
agent-deck goal resume <id> "<msg>"   # send a hint and reset nudge counter
```

## Implementation phases

Build in this order — each phase is independently shippable.

### Phase 1: Hand-wired goal (proof, no CLI yet)

Goal: prove the manager + worker contract works end-to-end on one real goal.

- Hand-write a goal JSON
- Hand-launch the worker via `agent-deck launch` with the contract prompt
- Run the manager as a one-shot Python script (no cron yet)
- Use Telegram via the existing conductor's bot

**Done when:** one real goal completes via the loop, with manager verifying done and notifying user.

### Phase 2: CLI wrapper

Goal: `agent-deck goal --goal X --done '<cmd>'` works.

- Add a bash wrapper (`agent-deck-goal.sh`) that:
  - Writes the goal JSON
  - Spawns the worker via `agent-deck launch` with the templated contract
  - Starts/refreshes the cron job
- Add `goal list / show / cancel / resume` subcommands

**Done when:** one command starts a goal; another command cancels it.

### Phase 3: Manager daemon as cron'd Python

Goal: `~/.agent-deck/goals/` is checked every 5 min unattended.

- Move manager logic into a standalone Python script
- Add to user's crontab (or systemd timer if available)
- Implement nudge generation, receipt parsing, escalation push
- Telegram push via the conductor's already-running bot

**Done when:** goal can run unattended overnight and either complete or escalate without manual intervention.

### Phase 4: Self-improvement feedback

Goal: each completed/failed goal produces a data point that the self-improvement pipeline can analyze.

- On finalize, write a summary to `~/.agent-deck/goals/history/<id>-{status}.md`
- Self-improvement's `distill.py` learns to read the history dir
- Next FINDINGS.md includes a "Goal patterns" section: success rates, common stuck reasons, average cycles to done by goal type

**Done when:** running the self-improvement pipeline surfaces goal-level patterns alongside transcript-level findings.

### Phase 5 (future): Polish

- TUI panel for active goals
- Web UI status page
- `goal retry <id>` to relaunch a failed goal with adjusted parameters
- Goal templates: `agent-deck goal --template release-merge --version v1.7.0`

## Open questions

1. **Should the conductor be able to spawn its own goals?** Yes, probably — when the conductor identifies a goal worth pursuing (e.g., during heartbeat), it should be able to delegate to a goal instead of trying to drive the work itself. Requires a "create goal from conductor" API.

2. **What about goals that need user input mid-cycle?** Current design escalates only on nudge failure. But some goals need human decision at known points (e.g., "approve this PR description"). Possibly add a `wait_for_user` worker primitive that pauses the goal and prompts via Telegram.

3. **How do goals compose?** Can a worker spawn its own sub-goals? Probably yes — multi-level goals become a dependency tree. But this is a future complexity, not a v1 feature.

4. **Conflict between goal ScheduleWakeups and conductor heartbeats?** The worker schedules its own wake. The cron heartbeats independent. If both fire close together, the worker gets two prompts. Need to handle gracefully — worker dedupes recent prompts, or one is dropped.

5. **Cost ceiling per goal?** A runaway worker could burn many tokens. Add `--max-tokens` or `--max-cost-usd` as a circuit breaker.

## Risks

| Risk | Severity | Mitigation |
|---|---|---|
| Buggy done-condition completes goal spuriously | High | Dry-run mode that prints `done_cmd` output at creation; user reviews before launching |
| Worker ignores the contract (LLMs being LLMs) | Medium | Manager doesn't trust the worker anyway; verifier is the source of truth |
| Nudge floods the worker context | Medium | Hard cap on nudges before escalation; nudges are short; `--no-wait` means they don't block the worker |
| Manager misses receipts due to task-log.md format drift | Medium | Rigid receipt format; parser is strict; mismatch → no-receipt = nudge fires anyway |
| Goal accumulates indefinitely | Low | `max_cycles` hard cap; auto-cleanup of goals in `done` / `failed` after N days |
| Telegram bot rate-limited during burst escalation | Low | Manager exponential-backoffs on Telegram errors; falls back to local log file |

## Relationship to other pieces

- **Self-Improvement** (post-hoc analysis): goal history feeds in; goal failures become findings
- **Observer / Watchdog** (live stuck detection): goal manager subsumes part of this; the rest (non-goal conductors that stall) still needs a generic watchdog
- **Conductor's CLAUDE.md Behavior Rules**: goal implements Rule #10 ("change tactic after 3 unchanged") as a mechanism, not just policy
- **Existing agent-deck primitives**: goal is glue code; no agent-deck core changes required

## What this is NOT

- **Not a planning agent.** Goal doesn't decompose goals into sub-tasks. The worker plans its own cycles. Goal just enforces "do something, write a receipt, check if done".
- **Not a multi-agent orchestrator.** One goal = one worker. Multi-worker coordination is out of scope.
- **Not a replacement for the conductor.** The conductor is still the long-lived orchestrator. Goals are spawned BY the conductor (or by you directly) for specific goal-bounded work.
- **Not for fuzzy goals.** "Improve the codebase" is not a goal. "Get PR #X merged" or "Apply migration Y" or "Reach 80% test coverage on package Z" — those are goals.

## Verification this design closes the 18-hour stall

Walk through the gsd-v160 release scenario with goal in place:

| Hour | Today's behavior | Goal behavior |
|---|---|---|
| 0 | Worker spawned manually with vague prompt | `agent-deck goal --goal "Ship v1.6.0" --done '<gh release check>' --max-idle 1h --escalate-after 3` |
| 1 | Cron fires, conductor reports `[STATUS] running` | Manager runs `done_cmd` → not yet. Reads receipt from cycle 1 → "tagged locally, pushed origin". Quiet, all good. |
| 2 | Cron fires, conductor reports same `[STATUS]` | Manager: receipt from cycle 2 → "goreleaser running, waiting". Quiet. |
| 3 | Cron fires, same `[STATUS]` | Manager: receipt from cycle 3 → "goreleaser passed, release published". Manager runs `done_cmd` → **0**. ✅ Done. Worker stopped, user notified "Done: Ship v1.6.0". |
| **OR** if the worker actually stalls: |  |  |
| 3 | Cron fires, identical `[STATUS]` | Manager: no new receipt in 1h → **nudge 1** with context |
| 4 | Cron fires, identical `[STATUS]` | Still no receipt → **nudge 2** with different framing |
| 5 | Cron fires, identical `[STATUS]` | Still no receipt → **escalate** with full Telegram bundle |
| **You see the page at hour 5, with full context, instead of finding out at hour 18.** |  |  |

That's the win.
