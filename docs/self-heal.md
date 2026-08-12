# Self-heal

Self-heal is the transition daemon's supervision pass. It watches for sessions
that are genuinely stuck, and — in exactly one mode — delivers exactly one
continuation prompt to get them moving again.

**It ships disabled.** `[selfheal] enabled` defaults to `false` and `mode`
defaults to `"observe"`. Nothing below happens until you opt in.

## Configuration

All keys live under `[selfheal]` in `config.toml`.

| Key | Default | What it does |
|---|---|---|
| `enabled` | `false` | Global kill switch. While false, self-heal does nothing at all — not even observe logging. |
| `mode` | `"observe"` | Authority level. See below. |
| `audit_path` | per-profile default | Where the NDJSON audit lands. Empty uses `<data-dir>/runtime/selfheal/selfheal-audit-<profile>.ndjson`. |
| `per_session_per_window` | `2` (per 6 h) | Per-session recovery cap. `auth_401` is always 1 regardless. |
| `global_per_hour` | `5` | Fleet-wide hourly recovery cap. |
| `opt_out_groups` | none | Group paths excluded entirely. |
| `opt_out_sessions` | none | Session ids or titles excluded entirely. |

### Modes

- **`observe`** (default) — evaluates every session, records what it *would* have
  done in the audit, and takes no action. The engine holds no executor at all,
  so "observe takes no action" is structural, not a runtime check.
- **`resume`** — the one acting mode. It authorises exactly one thing: deliver a
  single continuation prompt to a session whose selected model is at capacity
  (`model-unavailable`), is wedged by a transport error (`api-error`), or has an
  exhausted usage window (`usage-limit`). Capacity recovery keeps the selected
  model; it never selects a fallback model or restarts the session. Every other
  action — including restarts — still refuses. The prompt goes through the same
  verified send path `session nudge` uses, so it inherits that path's
  composer-draft guard, submit verification and Escape+Enter escalation.
- **`single_action`, `full`** — defined but guarded. They refuse to act.

**Changing `mode` requires restarting the transition daemon.** The engine is
built once per profile and holds the two-read confirm plus every cap, backoff and
breaker window; rebuilding it when config changed would silently reset all of
them. Editing `mode` in `config.toml` therefore has no effect until the daemon
restarts.

### Recommended settings for a large fleet

```toml
[selfheal]
enabled = true
mode    = "resume"
global_per_hour = 30
```

`global_per_hour` is the one dial worth changing. Its default of 5 was sized for
*restarts*, and it is wrong for this workload: a transport outage is correlated
and wedges every session at once, so 5 would heal 5 of 30. A resume is a single
delivered message and is far cheaper than a restart. **30 is a recommended
operator setting, not a new default — the shipped default stays 5.**

## What self-heal will never do

- **Submit text a human typed.** If the composer holds an operator draft, the
  verdict downgrades to escalate and self-heal stands down. It also spends no
  recovery budget doing so, so the session is still resumable the moment the
  draft clears. (Such a session is additionally reported as `stalled` — 🧊 — once
  its draft has sat unchanged for 10 minutes, and `session nudge` refuses to send
  to it.)
- **Restart anything.** This feature only ever sends a message.
- **Act on a session that is opted out, flapping, stopped, or past its caps.**

## Reading the audit

Every evaluation appends one NDJSON record to the audit path. The outcome field
is the fastest way to answer "why was this session not resumed":

| `outcome` | Meaning |
|---|---|
| `observe_noop` | Mode is `observe`. It would have acted; it did not. |
| `held_stage_2_3` | Mode is `single_action`/`full`, which are guarded and refuse to act. |
| `held_composer_draft` | The composer holds text a human typed. No action, and no cap spent. |
| `resumed:submitted` | The prompt was delivered and accepted. The only healthy outcome. |
| `resumed:typed_not_submitted` | The bytes reached a composer that is not accepting Enter. Counted as a **failed** recovery; two consecutive failures open the circuit breaker. |
| `resumed:<other>` | Any other `session send` delivery verdict, recorded verbatim. Counted as a failed recovery, same as above. |
| `error:<message>` | The executor itself failed before any delivery. No action taken — and **not** counted by the circuit breaker. |

Which outcomes open the circuit breaker, precisely: only the ones where an
action actually ran, i.e. the `resumed:*` family. `resumed:submitted` is the
success that resets the consecutive-failure count; every other `resumed:*` value
is a failure, and two consecutive failures open the breaker for that session.

`error:*` is different and it is worth knowing why. An executor error is
reported as "no action taken", so the breaker never sees it and a session that
errors on every attempt never trips one — but the attempt was already recorded
before the executor ran, so **it still consumes one of the two recoveries the
session gets per 6 hours**. A session erroring repeatedly therefore goes quiet
after two attempts by hitting `cap_hit`, not `breaker_open`. If a session stops
being resumed and the audit shows `error:*` records, read the error text: the
breaker will not tell you about it.

The `decision` field records the gate that stopped a candidate earlier:
`skip_dwell`, `skip_confirm`, `skip_not_before` (a usage window that has not
reopened yet), `cap_hit`, `breaker_open`.

## Deployment note

Self-heal runs inside the **transition daemon**, so the running daemon must be
the new binary or the feature is silently absent. Two known hazards:

- a stale `com.agentdeck.menubar` LaunchAgent has previously kept a pre-fix build
  alive across a rebuild;
- the transition-notifier launchd agent needs `bootout` + `bootstrap` after a
  `make install` — `kickstart` does not pick up the new binary.

Confirm which binary the live daemon is running before trusting the feature.
