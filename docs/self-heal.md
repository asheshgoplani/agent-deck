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
| `audit_full_records` | `false` | Write one audit record per *evaluation* instead of per state change. Debugging only — it restores the ~480 MB/day growth rate. |

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

**Changing `mode` or `audit_full_records` requires restarting the transition
daemon** — both are read once, when the profile's engine and audit sink are
built. The engine is
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

## What gets recorded

Self-heal evaluates **every session on every poll**. It records one NDJSON line
per session **state**, not per evaluation:

- the **first** read of a new state is written immediately — a live `tail` shows
  a session changing state at once;
- identical follow-up reads are suppressed but **counted**;
- when the state changes, a **closing** copy of the run is written first,
  stamped with the last suppressed read's timestamp and `"repeat": N`, so the
  run's exact extent is on record before the next state opens;
- an unchanged session is re-recorded every **15 minutes** (again with
  `repeat`), so "self-heal recorded nothing" can never be confused with
  "self-heal was not running";
- a record where an action actually **ran** is never collapsed.

So the audit still answers *what state was this session in, since when, and how
many reads confirmed it*. It no longer answers *what was the output signature on
read #17,432 of an unchanging run* — the data that cost 480 MB/day.

Why: recording every evaluation measured **2.55 GB / 7.4M records in 5 days**
(~480 MB/day, ~345 bytes/record) on one normal single-user machine
(2026-08-12 → 2026-08-17), nearly all of it a healthy session re-recorded every
2 seconds. The same 24 h of a 34-session fleet collapses to ~3.3k records
(~1 MB/day), a 99.8% drop. Set `audit_full_records = true` to get the raw
per-evaluation stream back while debugging the pass.

## Audit size, rotation and retention

The rate above is what rotation is sized for: the dials below assume the
*uncollapsed* worst case (`audit_full_records = true`, or a fleet flapping
between states on every poll), so they hold regardless of how much the collapse
saves.

| Dial | Value | Why |
|---|---|---|
| Segment threshold | 128 MiB | ≈400k records ≈6.4 h at the measured rate — a roll ~4×/day, and one segment is still `zcat \| jq`-able. |
| Retained segments | 28 | 28 × 128 MiB = 3.5 GB of records ≈ **7.5 days**, so the ≥1-week observe window survives with margin. |
| Compression | gzip on roll | Measured 11× on representative records → ~330 MB for the rolled window, ~460 MB worst case including the live segment. |

Rotation never truncates or rewrites: the live file is **renamed** to a stamped
sibling (`selfheal-audit-<profile>.ndjson.20260817T131901Z.gz`) and gzipped in
the background, and only whole segments older than the retention count are
removed. An interrupted compression leaves the plain rolled segment in place,
which is still part of the window.

## Reading the audit

**The window spans segments — reading only the live path misses every rolled
segment.** Oldest record first:

```sh
P=~/.agent-deck/runtime/selfheal/selfheal-audit-default.ndjson
{ for f in $(ls -1 "$P".*Z "$P".*Z.gz 2>/dev/null | sort); do gzip -cdf "$f"; done
  cat "$P"; } | jq -c 'select(.decision == "act")'
```

In Go, `selfheal.ForEachAuditEvent(livePath, fn)` walks the same window in the
same order (and `selfheal.AuditSegmentPaths` returns the files it would read).

A record with `"repeat": N` stands for itself plus N identical evaluations that
were not written; its `ts` is the last of them. To count *evaluations* rather
than records, sum `1 + (.repeat // 0)`. The outcome field is the fastest way to
answer "why was this session not resumed":

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
