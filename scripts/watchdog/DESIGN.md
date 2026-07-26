# Auto-Restart Watchdog for Critical Agent-Deck Sessions

Date: 2026-04-19 (initial design) · 2026-04-22 (lift-and-shift into repo for v1.7.63)
Target: agent-deck v1.7.63

## Problem

Conductor sessions (`conductor-innotrade`, `conductor-travel`, `conductor-opengraphdb`, etc.) are the brain of the orchestration system. A single SSH logout or tmux hiccup can flip them to `error`, and they stay there until a human manually runs `agent-deck session restart`. Today there is no automatic recovery for this class of session.

The existing reviver (`internal/session/reviver.go`) only revives `ClassErrored` (tmux alive, control pipe dead). Sessions where tmux itself died are `ClassDead` and are deliberately NOT auto-revived by design (disambiguation between intentional kill and crash).

The missing piece: **something external** that, for a small allow-list of critical sessions, WILL restart them when they flip to error — with rate limits, cascade guards, and escalation.

## Non-goals

- Auto-restart arbitrary worker sessions (keep opt-in explicit)
- Replace the existing transition-notifier daemon (reuse it as a signal source)

## Status note (v1.7.63, 2026-04-22)

The original non-goal "Modify agent-deck Go source (external-only; zero new mandate territory)" is **retired as of v1.7.63**. The watchdog is now a first-class distribution asset shipped under `scripts/watchdog/` in this repo so that all users benefit from bug fixes and feature additions through normal releases — not just the host where this file originated. Tests in `scripts/watchdog/test_watchdog.py` are under the repo's TDD gate.

## Architecture

One Python daemon, shipped from `scripts/watchdog/watchdog.py` in this repo, running under a user-level systemd unit (typical install: copy to `~/.agent-deck/watchdog/watchdog.py` and point a `systemd --user` unit at it).

```
     ┌──────────────────────────────┐
     │ agent-deck notify-daemon     │ (existing service, ~Restart=always)
     │   writes hook files          │
     │   ~/.agent-deck/hooks/*.json │
     └──────────────┬───────────────┘
                    │ inotify CLOSE_WRITE / MOVED_TO
                    ▼
     ┌──────────────────────────────┐
     │ watchdog.py                  │
     │  - inotify listener (fast)   │◄──────┐
     │  - 5s safety poll (slow)     │       │
     │  - per-session rate limiter  │       │ 5s tick
     │  - global cascade guard      │       │
     └──────────────┬───────────────┘       │
                    │                       │
         is_critical && status==error       │
                    │                       │
                    ▼                       │
        agent-deck session restart <id>     │
                    │                       │
                    ▼                       │
          (post-restart cool-down)──────────┘
```

### Trigger sources (in order of latency)

1. **Primary (inotify)**: `inotifywait -m ~/.agent-deck/hooks/` — kernel-level fsevent on every hook file write. Latency: ~100 ms end-to-end. Covers: Claude hooks that write `status:"dead"` or transitions to waiting/error.

2. **Safety-net (poll)**: every 5 s, run `agent-deck list --all --json`, for each critical session run `agent-deck session show <id> --json`, check `status`. Latency: up to 5 s. Covers: external tmux kills where the Claude process never got to write a hook. Cost: ~3 subprocess calls every 5 s for 3 conductors = negligible.

Both sources feed the same dispatcher. The dispatcher is idempotent (per-session state machine), so dual-firing is a non-issue.

### Critical-session criteria

A session is "critical" if **any** of:

- `title` starts with `conductor-` (current flagship case — 3 sessions today)
- `group` == `"conductor"` or `"watchers"` (forward-compat; no watchers exist yet)
- `is_conductor: true` in `session show` output
- File `~/.agent-deck/watchdog/autorestart/<id>` exists (explicit opt-in for one-offs)

A session is excluded if `status` == `"stopped"` (user deliberately stopped it).

### Restart path

Call `agent-deck session restart <id>` — this preserves ClaudeSessionID via `respawn-pane -k`, env_file, config_dir, channels. Exactly what conductors need. No new restart logic in the watchdog; we reuse the battle-tested path.

When agent-deck answers that call with `skipped: true`, a guard inside agent-deck declined on purpose (an auth hold, or the freshness guard from #30). The watchdog stops there: it does **not** escalate to `session start`, which would defeat the guard that just fired.

## Guardrails

### Liveness confirmation immediately before the restart (#1705)

A restart tears a live pane down, so the decision to fire one must be about the session **now** — not about whenever the triggering sample was taken. Two ways the authorizing `status == error` reading goes wrong:

- **Stale.** Restarts are globally serialized (`MIN_GLOBAL_RESTART_INTERVAL_S`) and a cascade revives serially, so a queued decision can execute minutes after its sample. The session may have recovered in between.
- **False.** `error` is partly derived from pane *content*, so a banner-shaped line sitting in scrollback keeps the verdict standing while the agent works on happily. Re-reading the status cannot see this: the content does not move, so every read agrees.

So, as the last step inside `_do_restart` (after the serialization wait, not before it), the watchdog re-reads the status and samples the pane twice `LIVENESS_CONFIRM_GAP_S` apart via `session output --pane`. It skips the restart when:

| Reason | Why |
|---|---|
| `recovered_before_restart` | the status is no longer `error` — the stale case |
| `pane_active` | the pane is still producing output; an agent emitting output is not dead |
| `status_unreadable` | we cannot see the session, so we cannot claim it is dead |
| `shutting_down` | the daemon is exiting mid-sample; there is no second reading, and exit time is the worst time to be wrong |
| `auth_hold` | a restart cannot fix a credential, and each doomed boot races the shared rotating refresh token |

A skip returns the rate-limit slot the caller reserved (a restart that never happened is not an attempt), so a live session the watchdog declined to touch cannot escalate as "keeps crashing". `LIVENESS_SKIP_ESCALATE_AFTER` consecutive `pane_active` skips escalate as `liveness-mismatch` — that is a status-classification problem to investigate, not a dead session. That alert and the auth-hold one fire once per error episode rather than once per poll (both conditions are observed on every poll until a human clears them); the skip counter resets when the session is next seen healthy.

Every decision, skip or restart, is appended to `~/.agent-deck/watchdog/liveness.log`: both status reads with timestamps and substates, both pane digests, whether the pane moved, the auth-hold reason, and the verdict. Digests, never pane text — the log is meant to be readable and pasteable into a bug report.

### Per-session rate limit

Max **3 restarts per 300 seconds per session_id**. Tracked in-memory dict `restart_history[id] = [ts1, ts2, ts3, ...]`, pruned on each check. Exceed → skip restart and escalate.

### Global cascade guard

If **5 or more** restarts fire across all critical sessions within a **10-second window**, pause all restarts for **60 seconds** and emit a single "cascade detected" escalation. This is the "SSH logout took everything down" case — wait for the system to settle, then resume normal operation.

### Post-restart cool-down

After each successful restart, suppress further restart attempts for that same session for **30 seconds**. This allows the new tmux session time to come up and prevents inotify-driven double-fire.

### Escalation channel

On any of:

- Per-session rate limit exceeded (3rd failure in 5 min)
- Cascade detected (5 in 10s)
- Restart subprocess returned non-zero 2x in a row

… the watchdog writes a structured line to `~/.agent-deck/watchdog/escalations.log` AND calls `~/.agent-deck/watchdog/escalate.sh <severity> <message>` if that script exists (stub initially, wire to Telegram bot later).

## Concurrency

Single-process Python with a threading model:

- Thread A: inotify listener (blocking `inotify.adapters.Inotify`)
- Thread B: 5 s safety poll
- Main thread: event queue consumer with a mutex around rate-limit state

No external store — state lives in-memory, resets on daemon restart. Acceptable because the only thing that's lost is the 5-min rolling window, and a restart is itself a signal that something went sideways.

## Failure modes

| Failure | Effect | Mitigation |
|---|---|---|
| Watchdog crashes | No auto-restart until systemd restarts it | `Restart=always`, `RestartSec=5` |
| `agent-deck` binary missing or broken | All restarts fail | Escalation after 2 consecutive non-zero exits |
| Conductor itself keeps crashing | Watchdog hits rate limit, escalates | Per-session cap at 3/5min |
| Notify-daemon down | inotify still fires (hooks are written by Claude hooks themselves) | Dual trigger source |
| Hook dir floods (100s of events/sec) | Backpressure on event queue | Bounded queue (size 100); drop oldest and log |

## Out of scope for v1

- Telegram escalation wiring (stub only; will be wired once the bot token story is finalized)
- Per-group restart policies (all critical sessions share one rate-limit policy for v1)
- Dry-run mode as a config flag (use `--dry-run` at invocation)
- Metrics endpoint (stdout logging is sufficient for now)

## Files

- `~/.agent-deck/watchdog/DESIGN.md` — this file
- `~/.agent-deck/watchdog/watchdog.py` — the daemon
- `~/.agent-deck/watchdog/escalate.sh` — escalation stub (template)
- `~/.agent-deck/watchdog/escalations.log` — written by daemon
- `~/.agent-deck/watchdog/restart.log` — one record per restart, skip or refusal
- `~/.agent-deck/watchdog/liveness.log` — one record per liveness decision, with the readings behind it (#1705)
- `~/.agent-deck/watchdog/autorestart/<id>` — marker files for explicit opt-in (dir created by daemon)
- `~/.config/systemd/user/agent-deck-watchdog.service` — user-level systemd unit (disabled until user approves)

## Rollout

1. Write script + systemd unit, leave DISABLED.
2. Synthetic dry-run with fake hook events and fake CLI responses — assert rate limit, cascade guard, critical-session filter all behave correctly.
3. User approval gate → real kill of one low-stakes conductor (e.g., `conductor-opengraphdb` if not actively in use), verify recovery within 10 s.
4. User approval gate → `systemctl --user enable --now agent-deck-watchdog.service`.
5. Monitor for 24 h, then adjust thresholds if needed.

## Coexistence with existing infrastructure

- **Does not conflict with existing reviver** — reviver handles `ClassErrored` (tmux alive, pipe dead); watchdog handles `ClassDead` (tmux gone) for critical sessions only. Disjoint domains.
- **Does not modify notify-daemon** — reads its outputs (hooks) but is a pure consumer.
- **No shared state with TUI** — watchdog never touches the SQLite statedb directly.

## Rate-limit tuning rationale

- 3 restarts / 5 min: a session that genuinely can't come up after 3 tries probably needs a human. 5 min window is long enough that a healthy session won't trip it even if it hiccups twice.
- 5 sessions / 10 s cascade: an SSH logout typically kills all sessions within ~2 s. 5-in-10s is a reliable cascade signal that is very unlikely in normal operation (normal crashes are isolated).
- 30 s post-restart cool-down: Claude's cold-start is typically 10-20 s. 30 s gives the new tmux session time to reach `running` status before the next inotify-driven re-check.
