# Refresh architecture v2 — event-driven, adaptive, remote-ready

Status: design + first slice landed
Issue: #1753 (perf: laggy switching at ~55 sessions), follow-up to #1756
Scope: `internal/ui` refresh loop, `internal/session` status derivation, `internal/tmux` caches

---

## 1. The problem, stated precisely

agent-deck's TUI does work proportional to **all live sessions** on every refresh
tick. On a 40–70 session deck that is the difference between a snappy tool and a
sluggish one, and it gets worse linearly as fleets grow.

There are three separate loops, each O(sessions):

| Loop | Cadence | Cost per session |
|---|---|---|
| `Home.backgroundStatusUpdate` (status worker goroutine) | `baseStatusInterval` = 2s, backing off to 10s when a sweep overruns | `Instance.UpdateStatus()` — cache reads, and for many states a `capture-pane` |
| `Home.processStatusUpdate` (tick-triggered, user-active only) | 2s | visible rows + 2 round-robin rows |
| `Home.View` | every keystroke + every tick | row render |

`View` was the loudest offender and is fixed: #1756 replaced an unconditional
full-frame `MaxWidth` rebuild with a measure-then-rebuild guard, taking process
CPU during a switching workload from 4.84% to 2.04%. What remains is the
**status sweep**, and it is the structurally wrong shape: it re-derives state for
sessions that provably did not change, using the most expensive mechanism
available.

### 1.1 Why the sweep is expensive, mechanically

Three facts compose badly.

1. **Only a handful of sessions have a control pipe.** `livePipeLRUCapacity = 3`,
   plus attached sessions. That bound is deliberate — pipes cost a tmux client
   each, and N agent-deck instances must stay cheap. But it means the sweep's
   existing "PipeManager says no output for 5s, skip" fast path applies to ~4
   sessions out of 70. Everyone else falls through.

2. **Without a pipe, `CapturePane` is a subprocess.** `tmux capture-pane -p -e`,
   3s timeout, serialized by the tmux server. Cached for only 500ms.

3. **The most common steady state on a large deck captures the pane.** A Claude
   session parked in hook `waiting` (finished its turn, needs you) takes the
   hook fast path in `Instance.UpdateStatus`, which calls
   `tmux.Session.BackgroundWorkPending()` to decide whether `run_in_background`
   work is still in flight. That captures the pane, cached for
   `bgWorkCacheTTL = 3s`. With a 2s sweep that is a capture roughly every other
   sweep, **per waiting session**.

At ~60 quiescent sessions that is tens of `capture-pane` subprocesses per sweep,
all queued behind one tmux server, to re-derive statuses that are identical to
the ones already on screen. When the sweep overruns 2s, `nextStatusInterval`
backs the cadence off — so the system degrades by getting *less* fresh, not by
getting cheaper.

### 1.2 The other half: the event source we already have and ignore

Claude, Codex, Gemini, Hermes and Cursor **already push** lifecycle events into
`~/.agent-deck/hooks/<id>.json` (Stop, Notification, PermissionRequest…), and
`session.StatusFileWatcher` already watches that directory with fsnotify,
debounces, and maintains a live `map[instanceID]*HookStatus`. OpenCode does the
same over SSE (`OpenCodeSSEWatcher`, #1614).

But the watcher is constructed as `session.NewStatusFileWatcher(nil)` — **the
`onChange` callback is nil**. Nothing wakes the TUI when a hook fires. The sweep
*polls the watcher's map* on its own 2s cadence, and the freshly-delivered event
sits in memory waiting to be noticed. We pay for a push pipeline and then
consume it by polling.

That is the core architectural insight behind v2: **the events are already
there. The refresh loop just isn't listening.**

---

## 2. Target model

Three layers, in priority order. Layer 1 decides *when* to do work, layer 2
decides *how much*, layer 3 decides *where*.

### 2.1 Event-driven first

Updates should be **caused** by evidence, not by a clock.

- **Hook events wake the TUI.** Give `StatusFileWatcher` a real `onChange(id)`
  that pushes a `sessionEventMsg{id}` into the Bubble Tea program (via
  `p.Send`), and route it to a single-session status refresh. Latency for a
  turn ending drops from "up to one sweep" to "as fast as fsnotify delivers",
  and it costs exactly one session's work rather than a fleet sweep.
  The watcher's existing debounce is the coalescing layer; an events-per-second
  cap per session guards against a pathological hook loop.
- **tmux control-mode notifications wake the TUI.** The `PipeManager` already
  receives `%output`, `%window-pane-changed`, `%session-closed` on the sessions
  it pipes, and `logUpdateChan` already turns `%output` into a per-session
  `UpdateStatus`. This is the right mechanism; it is simply capacity-bound to 3
  sessions. Widening it is a separate, measured decision (a single control-mode
  client on the *server* can report `%sessions-changed` / `%unlinked-window-…`
  for all sessions — a "fleet notification channel" that is O(1) clients rather
  than O(sessions)).
- **The periodic sweep becomes a reconciliation safety net, not the primary
  path.** Its job is to catch what events cannot report: a session whose hooks
  are not installed, a tool with no event protocol (`shell`, `pi`), a pane that
  died without writing anything, a dropped fsnotify watch, an event lost to
  overflow (`fsnotify.ErrEventOverflow` is already handled). Once events drive
  the common case, this sweep can run at 10–30s instead of 2s.

The degradation model is the one the codebase already uses for hooks: an event
source is trusted while fresh, and control falls through to polling when it goes
stale. v2 applies the same shape one level up — to the *scheduler*, not just to
the status verdict.

### 2.2 Adaptive second

Not all sessions deserve the same attention, and unchanged sessions deserve
none.

- **Visible rows are first-class.** Rows on screen (plus the selected row and
  anything attached) refresh at the fast cadence. This is what the user is
  looking at; freshness here is the product.
- **Off-screen rows are event-driven or slow.** They do not need a 2s poll to
  render a glyph nobody is looking at. They need to be correct *by the time they
  scroll into view*, and events already guarantee that for every tool that emits
  them.
- **Generation counters skip unchanged work.** Before spending a poll, ask the
  caches we already refreshed whether anything observable changed. If not, skip.
  This is the mechanism detailed in §3 and it is the slice that landed.
- **Cadence follows load, in both directions.** `nextStatusInterval` already
  backs off when sweeps overrun. It should also *tighten* when the deck is busy
  and *relax* when it is quiescent, and the "user is active" window that gates
  `processStatusUpdate` should extend to the whole scheduler.
- **Work per tick is budgeted, not unbounded.** The attach-return catch-up burst
  reported on #1753 (Ctrl+Q back to the list feels slow on a ~69-session deck)
  is the same defect in a different costume: everything the suspended TUI
  missed lands in one sweep. The fix is the same shape — visible rows first,
  the rest amortized across following ticks, and no work at all for sessions
  whose generation is unchanged.

### 2.3 Remote-ready third

The roadmap is that **fleets live on cloud machines and the Mac is a thin
control surface**. A refresh layer designed around "spawn a subprocess per
session per tick" is catastrophic across a network: 70 sessions × one SSH
round-trip each is not a slower version of the local case, it is a different
order of magnitude. The layering above is what makes remote cheap, because both
of its principles carry over cleanly: an *event* crossing an SSH connection
costs one multiplexed message rather than a round-trip, and a *generation
counter* lets the remote answer "nothing changed" in a single reply for the
whole host instead of one probe per session. Concretely, the refresh layer
should treat a host as the unit of cost, not a session: one persistent
connection per remote (`ControlMaster` / a resident `agent-deck` agent), one
bulk status query per tick returning the whole host's fingerprints, and remote
hook events forwarded over that same connection rather than rediscovered by
polling — so the Mac's cost is O(remote hosts), independent of session count.
The existing `--ssh` support (remote session listing at
`[ui] remote_session_refresh_secs`, remote previews at a longer TTL, remote
latency probes) is the seed: it already batches per host and already treats
remote data as something to fetch on a slow, separate cadence. v2's job is to
make that the *general* model and let local tmux be one implementation of a
"session host" behind it, instead of the other way round.

---

## 3. The landed slice: adaptive tick + generation skip

Smallest change with the biggest win, deliberately conservative. Two parts, both
additive and both individually disable-able.

### 3.1 Per-session generation ("fingerprint")

`internal/ui/refresh_policy.go` introduces `sessionFingerprint` — a comparable
struct built **entirely from caches the sweep already refreshed once per tick**:

| field | source | spawns |
|---|---|---|
| `activity` | `Session.GetCachedWindowActivity()` — the `list-sessions` snapshot | nothing (map read) |
| `title`, `command`, `dead` | `tmux.GetCachedPaneInfo()` — the `list-panes` snapshot | nothing (map read) |
| `hookAt` | `StatusFileWatcher.GetHookStatus().UpdatedAt` | nothing (in-memory map) |
| `sseAt` | `OpenCodeSSEWatcher.GetStatus().UpdatedAt` | nothing (in-memory map) |

Building it for the whole deck is a handful of map lookups. `ok = false`
whenever the evidence is incomplete (no tmux session, stale pane cache, session
absent from the activity snapshot) and an unusable fingerprint **always forces a
poll** — so a session that dies or vanishes from tmux is never held.

`refreshLedger` stores, per session, the fingerprint of the last sweep that
actually polled it plus how many sweeps have been skipped since.

### 3.2 The gate

In `backgroundStatusUpdate`, immediately after the existing PipeManager
idle-skip, `UpdateStatus` is skipped only when **every** one of these holds:

1. the row is **not** visible (viewport snapshot published by the TUI tick);
2. the status is one of `running` / `waiting` / `idle` — `error` and `stopped`
   carry their own recheck timers, `starting` must converge fast;
3. the current fingerprint is usable **and** identical to the last polled one;
4. fewer than `maxSkips` consecutive skips have already happened.

Any veto polls. There is no path that skips on absent information.

**Why unchanged `window_activity` is a sound proof of "no new pane output".**
This is not a new assumption. `tmux.Session.GetStatus` already gates its
`CapturePane` on exactly this comparison (`needsBusyCheck`), and
`Instance.UpdateStatus` already gates its idle tier on it. The gate reuses the
established invariant and is **strictly more conservative than the existing
uses**, because those skip for as long as activity is unchanged while the gate
is bounded by rule 4.

**Why the ceiling matters.** `UpdateStatus` contains time-based transitions with
no external evidence: `acknowledged → idle`, hook-freshness expiry falling
through to tmux polling, `debounceFlipFromRunning` confirmation, the Hermes
gateway recheck. Rule 4 bounds their delay to `maxSkips` sweeps rather than
suppressing them. `window_activity` also has one-second resolution, so a
pathological "output within the same second as the previous sample, after our
capture" case is possible — rule 4 bounds that too, where the existing
`needsBusyCheck` does not.

**Why real transitions are not delayed in practice.** Anything that actually
happens moves the fingerprint on the very next sweep:

| event | fingerprint field that moves |
|---|---|
| turn ends (Stop hook) | `hookAt` |
| permission prompt | `hookAt` |
| any pane output | `activity` |
| spinner starts/stops | `title` (Claude's OSC marker) |
| process exits under remain-on-exit | `dead` |
| session disappears from tmux | `ok = false` → forced poll |
| OpenCode status over SSE | `sseAt` |

So the ≤`maxSkips` sweep delay applies only to transitions with *zero*
observable evidence, on rows that are *off screen*. Conductor completion
notifications, which are produced from `status_changed` in this same sweep, are
driven by the Stop hook and therefore not delayed at all.

### 3.3 Viewport publication

`publishVisibleSessions` runs on every TUI tick and is **O(visible rows)** — it
walks only the viewport slice of `flatItems`, never the whole deck — recording
the on-screen session IDs, the selected row, and window rows' parent sessions.
`visibleSessionsForSweep` **fails open**: with no snapshot, or one older than
`visibleSessionsMaxAge` (10s — the TUI republishes every 2s, so this means the
event loop is suspended by `tea.Exec` or wedged), the gate is bypassed entirely
and every session is polled, exactly as before the policy existed.

### 3.4 Render-snapshot generation skip

`refreshSessionRenderSnapshot` is called from six sweep/tick paths and used to
allocate and publish a fresh N-entry map every time. It now compares first: when
every computed state matches the published snapshot and membership is unchanged,
it returns without allocating or storing. The per-state derivation was extracted
into `computeSessionRenderState` so the compare pass and the build pass cannot
drift apart.

### 3.5 Kill switch

`[ui] adaptive_refresh_max_skips`:

- `0` / unset — default `2`: a quiescent off-screen session is polled at least
  every 3rd sweep (~6s).
- `>0` — custom ceiling; higher trades off-screen transition latency for less
  tmux load on very large decks.
- `<0` — **disables the policy**: every sweep polls every session, byte-identical
  to pre-policy behaviour.

### 3.6 Expected effect

At 60 quiescent sessions with ~10 rows visible, the gate removes roughly two
thirds of the sweep's `UpdateStatus` calls, and with them the
`BackgroundWorkPending` captures that dominate the current sweep — while every
row the user is looking at keeps polling at exactly today's cadence. Measure
with the sandboxed repro in #1753 (isolated `HOME`, own profile, own tmux
socket, 60 synthetic sessions) and the `idle_sessions_skipped` debug line, which
now carries an `adaptive_skipped` count.

---

## 4. Roadmap after this slice

Ordered by value per unit of risk. Each is independently shippable.

1. **Wire `onChange` on `StatusFileWatcher`** → single-session refresh on hook
   delivery. This is the first genuinely event-driven step and it is small: the
   callback plumbing and a `p.Send` hook already exist in shape elsewhere.
   It also directly serves the TUI-truth `waiting = done` vs `needs-you` split,
   which reads the same `HookStatus.Event` field (Stop vs Notification /
   PermissionRequest) that the callback delivers — one event ingestion path
   feeding both correctness and freshness.
2. **Relax the sweep cadence** once (1) lands: the safety net does not need 2s.
3. **Stagger the attach-return catch-up** (#1753 comment): visible rows first,
   remainder amortized, generation-skipped where unchanged.
4. **Fleet notification channel** — one control-mode client per tmux *server*
   for `%sessions-changed`-class notifications, instead of one pipe per session.
5. **Host-oriented remote refresh** — one persistent connection per remote, one
   bulk fingerprint query per tick, remote hook events forwarded over it.

---

## 5. Invariants any future change here must preserve

1. **Never skip on absent information.** Missing or stale evidence forces a poll.
2. **Never skip without a bound.** Every skip path has a ceiling, so a
   time-based transition can be delayed but never suppressed.
3. **Never skip a visible row.** What is on screen refreshes at the fast cadence.
4. **Fail open.** Any doubt about the scheduler's own state (no viewport
   snapshot, wedged event loop, nil ledger from an alternate constructor)
   degrades to polling everything.
5. **Death is never inferred from silence.** A session that leaves tmux produces
   an unusable fingerprint, which forces a poll, which detects the death.
6. **Keep a kill switch.** Every scheduling change ships with a config value
   that restores the previous behaviour exactly.
