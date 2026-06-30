---
name: fleet
description: Fan out a fleet of independent agent-deck child sessions from inside a session and check their progress non-blockingly. Use when the user wants to "launch several/N sessions", "fan out", "run agents in parallel", "spin up a fleet", "kick off background agents", or "check progress from the main session" without blocking — covers launching parented children, polling status + completion via `session children`, and collecting results via `session output`.
metadata:
  compatibility: "claude, opencode"
---

# Fleet

Fan out several independent agent-deck child sessions from inside your current
session, keep working, and check their progress on demand — **without blocking**
and **without consuming any delivery events**.

**Requires:** the `agent-deck session children` and `launch --assert-done`
features. If `agent-deck session children --help` succeeds, you have them.

## When to use

Use this when the user wants more than one agent working at once and wants to
supervise from the parent session — e.g. "launch 5 sessions to each tackle a
file", "fan out these tasks", "spin up a fleet and tell me when they're done".

This differs from the single sub-agent pattern in the `agent-deck` skill (one
child + fire-&-forget / on-demand / blocking retrieval). Fleet is **many
children + a non-blocking peek** across all of them.

**Run from inside an agent-deck session.** Launching auto-parents each child to
the launching session, which is what makes them show up nested in the TUI and
routes their completion back to you. (If you are not in a session, the children
still launch but won't be grouped under a parent.)

**Need a *specific* parent?** Auto-parenting picks the launching session. To
parent a child to a different session — e.g. fanning out under a named conductor,
or launching from outside that session — pass `--parent <session-id-or-title>`.
Spell out the long form: **never use the short `-p` to set a parent** (see the
`-p` pitfall in Notes).

## The loop

### 1. Fan out (one `launch` per child; loop it)

```bash
agent-deck launch <path> -c claude --inherit-group -m "<task for this child>"
```

- **Auto-parents** to your current session — children appear nested under you in
  the TUI session list, each with its own live status.
- **Children land in your group automatically.** A child launched into a git
  worktree auto-inherits the parent's group, so a worktree fleet stays
  co-located with you with no extra flags. For a non-worktree path that doesn't
  inherit, add `--inherit-group` to force it.
- **Do NOT pass a custom `-g/--group` for fleet children.** An explicit group
  overrides inheritance and drops the child into its own detached group
  (e.g. a stray `fleet-issues` sitting next to — not under — your group). Leave
  the group off and let it inherit; only set `-g` when you deliberately want a
  child somewhere other than with the parent.
- **`--assert-done` is on by default for `-c claude`**: the child's message gets
  a final-step instruction to print the completion sentinel
  (`===AGENTDECK_DONE=== status=ok summary=…`) so "done" is trustworthy.
- Run it N times (different `<path>` and `-m` per child) to fan out a fleet.

Useful flags:
- `--inherit-group` — force the parent's group for a non-worktree child (worktree
  children already inherit automatically).
- `-t "<title>"` — give each child a readable title (otherwise auto-named).
- `--parent <id|title>` — explicitly parent the child to a specific session
  instead of the auto-detected one. One step, no follow-up needed. **Long form
  only** — see the `-p` pitfall in Notes.
- `--no-assert-done` — skip the completion-sentinel instruction.
- `--no-parent` — launch flat instead of nested (you lose completion routing).

### 2. Keep working

Nothing blocks. Do other work in this session, in any chat, while the fleet runs.

### 3. Check progress (non-blocking, non-destructive)

```bash
agent-deck session children --json
```

Lists your sub-sessions with, per child: `id`, `title`, live `status`
(running / waiting / idle / error), and the last asserted completion
(`done_status` = ok|fail, `done_summary`, `done_at`). Defaults to the current
session; pass an id/title to inspect another parent. **Read-only** — it never
clears the inbox, so you can poll it as often as you like from any chat without
disturbing the conductor or other readers.

A child with a `done_status` has finished and asserted its result.

### 4. Unblock a child that's waiting on you

A child in `waiting` status has stopped and is asking for input (a question, a
decision, a permission). This is **pushed to you by default**: a child's
`running → waiting` transition is delivered to your inbox unless it was launched
with `--no-transition-notify`. To answer it:

```bash
# See what the child is asking:
agent-deck session output <child-id> --json
# Send the answer (child keeps running afterward):
agent-deck session send <child-id> "<your answer>"
```

`session send` flags: `--wait` (block until it finishes the turn, then print
output), `--stream` (stream its JSONL events), `--no-wait` (fire and return),
`--draft` (pre-fill the prompt without submitting). Default waits only until the
child is ready to receive, then returns — so it does **not** freeze your session.

You can send follow-ups any time, not just when a child is waiting — e.g. to
add scope, redirect, or course-correct a still-running child.

### 5. Collect a finished child's result

```bash
agent-deck session output <child-id> --json
```

Returns that child's latest full response. Use it once `session children` shows
the child is done (or any time you want its current output).

## Worked example

```bash
# Fan out 3 children, each on a different package:
agent-deck launch ./pkg/a -c claude --inherit-group -t "lint-a" -m "Fix all lint errors in this package."
agent-deck launch ./pkg/b -c claude --inherit-group -t "lint-b" -m "Fix all lint errors in this package."
agent-deck launch ./pkg/c -c claude --inherit-group -t "lint-c" -m "Fix all lint errors in this package."

# ...keep working, then whenever convenient:
agent-deck session children --json
#   → each child: status + done_status/done_summary

# If a child shows status "waiting", see its question and answer it:
agent-deck session output lint-a --json
agent-deck session send lint-a "Yes, drop the deprecated shim — don't keep a fallback."

# For each child reporting done, pull its result:
agent-deck session output lint-a --json
```

## Supervision tools the parent can use

All read-only / on-demand — none of them block your session:

- `agent-deck session children [id] --json` — **the default monitor.** Live
  status + last completion per child. Non-destructive (never clears the inbox),
  so poll it as often as you like. Start here every heartbeat.
- `agent-deck session output <id> --json` — a child's latest full response.
- `agent-deck session send <id> "<msg>" [--wait|--stream|--no-wait|--draft]` —
  send a follow-up / answer a `waiting` child.
- `agent-deck status -q` — global count of sessions currently `waiting`; a cheap
  coarse heartbeat across everything, not just your children.
- `agent-deck inbox drain --json <your-session-id>` — **consumes** the pushed
  completion events from your durable inbox (last-wins per child, deduped).
  Optional: `session children` already surfaces the same `done_status` without
  consuming anything, so only drain if you specifically want to clear the queue.
- `agent-deck session stop <id>` / `agent-deck session remove <id>` — teardown.

There is no always-on background watcher started for you — "monitored by
default" means transition/completion events **queue** in your inbox; you still
choose when to look (poll `session children`, or `inbox drain`).

## Notes

- **Completion signal:** trustworthy "done" comes from the child printing
  `===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>` as its last line.
  `--assert-done` (default-on for Claude) bakes this into the child's prompt; a
  child that never prints it shows live status but no `done_status`.
- **Non-blocking by design:** there is intentionally no "wait until all finish"
  command — checking is a cheap, repeatable query so a parent's other chats are
  never frozen.
- **Grouping:** worktree children inherit the parent's group automatically;
  for non-worktree paths add `--inherit-group`. Never pass a custom `-g` for a
  fleet child — it overrides inheritance and detaches the child into its own
  group. If a fleet did scatter (a stray group, or per-branch groups), move them
  back without restarting: `agent-deck group move <child-id> <parent-group>`,
  then `agent-deck group delete <stray-group>` once it's empty.
- **The `-p` pitfall — use `--parent`, never `-p`, for a parent.** `-p` is the
  *global* `--profile` shorthand, parsed before the subcommand. On older builds it
  swallows your intended parent id as a profile name and routes the child into a
  phantom `~/.agent-deck/profiles/<id>/state.db` — the child runs in tmux but is
  invisible to the TUI / `ls` / `session children` (which read the default
  profile), and a retry then fails with "session already exists". The long-form
  `--parent <id>` is never affected and is the one-step way to set an explicit
  parent. If you already launched a child and need to (re)parent it after the
  fact, `agent-deck session set-parent <id|title> <parent-id>` also works.
  To clean up phantom DBs from a past `-p` slip: the orphaned rows live under
  `profiles/<parent-id>/state.db`; back up and remove that dir (the child's
  worktree/branch stay on disk).
- **Stopping / cleanup:** `agent-deck session stop <id>` and
  `agent-deck session remove <id>` (add `--force` if needed) tear a child down.
