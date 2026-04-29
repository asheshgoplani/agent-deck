# RFC: Conductor Event-Delivery Pipeline Redesign

- **Issue:** #805
- **Status:** DRAFT — design space exploration; no implementation in this RFC
- **Author:** conductor session, worktree `rfc/event-pipeline`
- **Date:** 2026-04-29
- **Scope:** design only. Recommends a **phased path A → F**; the in-flight
  `fix/805-event-pipeline` branch already executes Phase 1.
- **Companion artifacts:**
  - Issue #805 (combined `conductor-innotrade` + `conductor-agent-deck`
    forensics, 2026-04-29)
  - `<repo-root>/tmp/event-delivery-audit.md` — 10 named coverage gaps,
    live production-log measurement
  - v1.7.71 set-parent group-preservation fix (#786 / #787)
  - v1.7.45 deferred-queue persistence path (currently in production)

## TL;DR

The transition-notifier drops **97-98%** of child→parent events on a real
multi-conductor host today. The pipeline mechanism is largely sound; the
loss is dominated by **parent-linkage hygiene** (children launched with
empty `parent_session_id`), with a smaller but real tail of dropped
deferred-busy events. No queue redesign fixes the hygiene tail — only
the launch-path closes those — so the right answer is a **layered**
response: input-hygiene fixes ship in parallel with a persistence
upgrade, not as a substitute for it.

The RFC evaluates six options against six dimensions and recommends a
phased path:

1. **Phase 1 (now):** Option A — per-conductor inbox file +
   retry-with-backoff + self-suppress. Already on
   `fix/805-event-pipeline`. Closes the deferred-loss tail and the
   conductor-self-loop drops. Zero new infra.
2. **Phase 2 (next milestone):** Option F — promote the per-conductor
   inbox from JSONL to a row in `internal/statedb/statedb.go` so the
   single source of truth is SQLite WAL, not a flat file. tmux
   `send-keys` remains the live-delivery rail; SQLite is the persistence
   rail. Closes G-5 (rapid consecutive latency) and gives the watcher
   panel + TUI a uniform query surface.
3. **Out of scope, explicitly rejected:** Options C (NATS/Redis broker)
   and D (event sourcing per session). They violate the zero-infra
   single-binary constraint and the cost is not justified by the
   observed failure modes.

## 1. Background and motivation

### 1.1 What the current pipeline actually is

The transition pipeline has four named components:

| Component | File | Role |
|---|---|---|
| **Daemon** | `internal/session/transition_daemon.go:25-66` | Adaptive 1/2/3-second poll over all profiles, computes status diffs, emits hook + snapshot transitions |
| **Notifier** | `internal/session/transition_notifier.go:64-107` | Resolves parent target, applies dedupe, decides sent / dropped / deferred, writes logs |
| **Sender** | `internal/session/send_helper.go:15-44` | Shells out to `agent-deck session send <ref> <msg> -q`; this is the line that ultimately calls `tmux send-keys` |
| **Deferred queue** | `internal/session/transition_notifier.go:484-577` (v1.7.45) | JSON file at `<state-dir>/runtime/transition-deferred-queue.json`; drained on every poll |

It is **not** a queue in the message-broker sense. It is a sampling
diff-of-status loop where:

- The daemon snapshots `(session_id → status)` every poll
  (`transition_daemon.go:128-152`).
- It compares the new snapshot to `d.lastStatus[profile]` and fires
  events for transitions that match `ShouldNotifyTransition`
  (`transition_notifier.go:109-119`: `running → {waiting, error, idle}`).
- The notifier resolves the parent
  (`transition_notifier.go:343-365`), checks for duplicates within 90s
  (`:376-388`), and either dispatches asynchronously
  (`:249-296`) or enqueues deferred (`:484-506`).
- A per-target single-slot semaphore (`:298-310`) prevents head-of-line
  blocking across targets while serializing per-target sends.
- Three terminal log states land on disk: `sent` / `failed` →
  `transition-notifier.log`; `timeout` / `busy` / `expired` →
  `notifier-missed.log`.

The reliability boundary is `SendSessionMessageReliable`. It does not
do its own retries — it inherits whatever guarantees the
`agent-deck session send -q` CLI offers, which is "best-effort
write-to-tmux-buffer-and-Enter."

### 1.2 What is dropping today

From `<repo-root>/tmp/event-delivery-audit.md` measured on the live
host on 2026-04-29:

```
476 events fired today
465 dropped_no_target  (98%)
  6 deferred_target_busy
  5 sent
```

**Two distinct loss modes** combine in that 98% number:

1. **Hygiene loss (dominant):** the firing child has empty
   `ParentSessionID`, so `resolveParentNotificationTarget` returns nil
   and the event is marked `dropped_no_target` synchronously. This is
   not a pipeline bug. It is a **data** bug in the launch path.
2. **Deferred-tail loss (smaller, harder to see):** the deferred queue
   ages entries out at 10 minutes / 20 attempts and writes them to
   `notifier-missed.log` with reason=`expired`. Those rows count as
   dropped from the user's point of view but do not show up in the
   `dropped_no_target` count. Issue #805 reports many such expirations
   without corresponding sends.

A third small loss mode is the **conductor self-loop**: top-level
conductors with empty `ParentSessionID` themselves still go through
`NotifyTransition`, fail at `:151-154`, and increment the dropped
count. They should self-suppress before calling the notifier at all.

### 1.3 Why this matters at scale

Conductor sessions are the only mechanism that lets a long-running
"brain" know when a child it dispatched needs attention. With a 98%
drop rate the conductor experience is "I dispatched ten children and
have to manually `agent-deck session output <id>` each one to find out
what happened." That collapses the entire conductor model back into
manual polling and is why issue #805 is filed P1.

## 2. The 10 audit gaps (load-bearing for option scoring)

From `<repo-root>/tmp/event-delivery-audit.md`. Every option below is
scored on which gaps it closes.

| # | Gap | Layer | Pipeline-shape problem? |
|---|---|---|---|
| **G-1** | Launch `--parent` propagation when env var dropped | Launch path | No — pre-pipeline |
| **G-2** | Worktree spawn doesn't inherit parent | Launch path | No — pre-pipeline |
| **G-3** | Sandbox/docker spawn doesn't inherit parent | Launch path | No — pre-pipeline |
| **G-4** | Detector false-`error` on long-thinking session | Detector | No — pre-pipeline |
| **G-5** | Rapid consecutive transitions to same target | Pipeline | **Yes** |
| **G-6** | `set-parent` rewriting `Group` (fixed v1.7.71) | Storage | Closed |
| **G-7** | Watcher orphan-by-construction | Launch path | By design |
| **G-8** | Gemini hook-driven asymmetry | Tool gate | Tool coverage |
| **G-9** | Opencode no transition pipeline | Tool gate | Tool coverage |
| **G-10** | Codex-compatible custom tools bypass hook gate | Tool gate | Tool coverage |

**Crucial framing:** only **G-5** is a true pipeline-design problem.
G-1/G-2/G-3/G-4/G-7 are launch / detector layer; G-8/G-9/G-10 are tool
gate logic. Any option below that claims to "fix the 97% drop" by
itself is wrong — the 97% is fixed by **launch-path** changes (which
are orthogonal to the pipeline shape) plus self-suppress (Phase 1 of A)
plus the deferred-tail fixes.

This is the single most important insight for picking among A–F.

## 3. Design space

For each option: data model, persistence, latency, infra dependency,
migration path, complexity, observability, gaps closed.

### Option A — Patch the current design (per-conductor inbox + retry-backoff + self-suppress)

This is what `fix/805-event-pipeline` is implementing now.

- **Data model.** A per-conductor JSONL file at
  `<state-dir>/inboxes/<parent-session-id>.jsonl`. Append-only producer,
  truncate-after-read consumer. Same `TransitionNotificationEvent`
  shape (`transition_notifier.go:25-36`); no schema change.
- **Persistence.** Disk file. Survives daemon restart. Same atomic
  write pattern already used at `transition_notifier.go:632-645`
  (`tmp` + `os.Rename`).
- **Latency.** Best case sub-second on the hot path (unchanged from
  today). Deferred path inherits the existing 1/2/3-second adaptive
  poll, plus 5/15/45-second backoff on busy retry. End-to-end p95
  bounded at ~1 minute when target is busy, expiring to inbox after
  3 attempts. The current ~2-minute deferred→sent observation
  (`webui-redesign-impl` example in audit §4) shrinks by roughly
  half.
- **Infra dependency.** None.
- **Migration.** Pure additive. The inbox file is a new sink alongside
  `transition-notifier.log` and `notifier-missed.log`. The existing
  deferred queue keeps working; the inbox only catches what the queue
  would have aged out.
- **Complexity.** Low. ~400 LOC change concentrated in
  `transition_notifier.go` plus a `cmd/agent-deck/inbox_cmd.go`
  reader. Fits a single PR.
- **Observability.** A new `agent-deck inbox <session>` CLI surface
  (already in #805's proposed fix) lists pending events. The TUI gets
  an "n pending" badge on the conductor row.
- **Gaps closed:** none of the launch-path gaps (G-1/2/3); no detector
  gap (G-4); no tool-coverage gaps (G-8/9/10); **partially closes**
  the dropped-deferred tail; **closes** the conductor self-loop sub-mode
  via item 4 of #805's plan. **G-5 stays open.**
- **What it doesn't fix.** Rapid consecutive transitions to the same
  busy target still show ≥2 minutes p95 latency under sustained load
  because the per-target slot serializes. The inbox file is per-parent,
  not per-(parent, child), so a busy-target queue can grow large
  before drainage completes.

### Option B — Replace tmux send-keys with a SQLite-backed event queue

- **Data model.** A new `events` table in `internal/statedb/statedb.go`
  alongside the existing `watcher_events` table. Columns:
  `(id INTEGER PK, target_session_id TEXT, child_session_id TEXT,
  from_status TEXT, to_status TEXT, profile TEXT, created_at INTEGER,
  delivered_at INTEGER NULL, attempts INTEGER, status TEXT)`. WAL
  mode. Index on `(target_session_id, status, created_at)`.
- **Persistence.** SQLite file with WAL. Already the persistence
  substrate for watcher events; no new dependency. Atomic by definition.
- **Latency.** Push-based notification still requires *something*. Two
  shapes:
  - **B.1 (poll):** conductor sessions poll the events table every
    1-3 seconds. Same cadence as today's daemon. Sub-second p95
    *intrinsic* but bounded by poll cadence.
  - **B.2 (LISTEN/NOTIFY emulation):** SQLite has no native
    LISTEN/NOTIFY. Closest is a fanotify/kqueue watch on the WAL
    file (`internal/watcher/...` does this for `watcher_events`).
    Cross-platform parity is real but fragile — the existing watcher
    health bridge had two iterations to get right.
- **Infra dependency.** None new. SQLite is already a runtime
  dependency via `internal/statedb`.
- **Migration.** Larger. The notifier becomes a writer-to-SQLite. The
  conductor side needs a reader. The tmux `[EVENT]` line becomes
  optional and arguably should be retained as a *render* of the row,
  not the canonical record. Estimated 3-5 days, mostly in the consumer
  side and migration of `transition-notifier.log` readers.
- **Complexity.** Moderate. New table, schema migration, tests in the
  `internal/session/transition_notifier_*_test.go` suite all need
  rewrites because they assert on JSON files.
- **Observability.** Strictly better than today. A SQL query answers
  every "did this event flow" question. The TUI watcher panel
  precedent (`internal/ui/watcher_panel.go`) shows the rendering side
  is well-trodden.
- **Gaps closed:** **G-5** (rapid consecutive transitions become rows,
  not pane writes — no per-target serialization at the storage layer);
  partially **G-4** if event status is rich enough to record
  detector-disputed transitions. Launch-path gaps remain.
- **Caveat.** SQLite WAL on macOS over a network FS or with multiple
  Go processes opening the same file has well-known surprises. The
  watcher framework already deals with this; reusing that wisdom is
  why this isn't dismissed outright.

### Option C — Real message broker (NATS embedded or Redis)

- **Data model.** Topics keyed by `target_session_id`. Producers
  publish, consumers subscribe. Persistent JetStream (NATS) or Redis
  Streams provides at-least-once.
- **Persistence.** Broker-managed. Real durability story, real
  acknowledgment story.
- **Latency.** Sub-100ms intrinsic. Best of any option here.
- **Infra dependency.** **This is the deal-breaker.** agent-deck is
  shipped as a single statically-linked Go binary. NATS embedded
  ships as a Go library and is technically possible (`nats-server`
  can be `import _ "github.com/nats-io/nats-server/v2/server"`), but
  it adds ~6 MB to the binary and a non-trivial port-allocation /
  data-dir lifecycle problem on machines that already have an
  unrelated NATS instance. Redis would require a separate process
  the user installs and manages.
- **Migration.** Heavy. Every conductor needs a subscriber goroutine.
  The notifier becomes a publisher. Auth, port selection,
  cross-platform supervision (macOS launchd vs Linux systemd vs WSL
  procfs) all become problems.
- **Complexity.** High. New supervision surface comparable in size to
  the existing tmux-control-pipe machinery in `internal/tmux/`.
- **Observability.** Excellent. NATS has dashboards, metrics, replay.
- **Gaps closed:** G-5; possibly G-4 if events carry detector
  metadata. Same launch-path gaps remain.
- **Verdict.** **Rejected.** The complexity and binary-footprint cost
  cannot be justified by the observed failure modes. The pipeline is
  not slow when it runs; the pipeline is *missing inputs*. A faster
  push channel does not deliver an event whose `parent_session_id` is
  empty.

### Option D — Event sourcing per session

- **Data model.** Each session has an append-only event log
  (`<state-dir>/sessions/<id>/events.jsonl` or a SQLite table partitioned
  by session). Conductors fold over a window of the log to compute
  current state and react to deltas.
- **Persistence.** Trivial — append-only files with rotation.
- **Latency.** Reading is fast. The latency question is "how does the
  conductor know to re-read?" which devolves to one of the other options
  (poll, fsnotify, push).
- **Infra dependency.** None.
- **Migration.** **Heavy.** This is a model shift, not a swap. Every
  callsite that today does "fire one event, write one log line" becomes
  "append to a log, derive state, decide to react." Detection,
  notification, notification-bar, and the v1.7.45 deferred-queue all
  collapse into a single fold function. Done well, it is a 2-3 week
  rewrite touching `internal/session/event_writer.go`,
  `event_watcher.go`, the notifier, and the daemon.
- **Complexity.** High structurally, low conceptually. Replay is free.
  Debugging is much easier ("show me everything that happened to
  session X" is a single tail).
- **Observability.** Excellent — replay-and-fold is the strongest
  forensics story of any option.
- **Gaps closed:** G-5 (events become rows, not in-flight messages);
  potentially G-4 (detector decisions are visible in the log and can
  be retroactively scored). G-8/9/10 are still tool-coverage problems
  but the log shape would make them easier to add.
- **Verdict.** **Right destination, wrong release.** This is what
  agent-deck wants in two milestones, not now. Recommend revisiting
  after Phase 2 (Option F) has stabilized; the SQLite events table from
  F is a natural starting point for a per-session replay log.

### Option E — Server-Sent Events / WebSocket from the agent-deck daemon

- **Data model.** The agent-deck daemon (already running for the web
  UI per `internal/web/push_service.go:23-26`) hosts an SSE endpoint
  at `/events?target=<session-id>`. Conductor sessions subscribe via
  curl-piped-to-stdin or a small reader process.
- **Persistence.** None at the transport layer. Drops on disconnect.
  Would need to combine with B or F to get durability.
- **Latency.** Sub-second push.
- **Infra dependency.** Reuses the existing `internal/web/` HTTP server
  infrastructure (already serves `web_push_subscriptions.json`,
  already has VAPID keys for browser push). No new processes; new
  endpoints on the existing daemon.
- **Migration.** The conductor side is the open question. Conductors
  are tmux panes running `claude` / `codex`; they have no native HTTP
  client loop. The bridge would need to be:
  - A child reader process per conductor that subscribes to SSE and
    pipes lines into the tmux pane via `tmux send-keys` — which is
    exactly the channel we are trying to replace.
  - Or move the conductor onto the web UI exclusively, which is a
    product-shape change far outside this RFC.
- **Complexity.** Moderate-to-high once the conductor side is real.
- **Observability.** Good in principle (HTTP logs, request tracing).
- **Gaps closed:** G-5 in principle; everything else unchanged.
- **Verdict.** **Rejected for primary use.** The conductor surface is
  tmux-pane-shaped. SSE is the wrong abstraction for that surface
  today. If the web UI becomes the primary conductor surface in a
  later milestone, revisit. The push_service.go infrastructure is
  good and should be preserved for *browser* notifications, which is
  what it was built for.

### Option F — Hybrid: tmux send-keys for live delivery, SQLite for persistence

- **Data model.** Same `events` table as Option B, but the *primary*
  delivery rail remains the existing notifier→sender→tmux path. The
  table is the source of truth for "did this event happen?" The tmux
  send is a render of the row.
- **Persistence.** SQLite WAL. Atomic, queryable.
- **Latency.** Same hot-path latency as today (sub-second when target
  is free). Sweeper drains expired/missed rows on the existing daemon
  poll cadence, with the same 5/15/45-second backoff as Option A.
- **Infra dependency.** None new (SQLite already in `internal/statedb`).
- **Migration.** **Two-step.**
  - **Step 2a:** add the `events` table; the notifier writes a row
    *before* the async dispatch (replacing the in-memory
    `markNotified` call at `transition_notifier.go:172`); the row is
    updated to `delivered` on `sent`, `failed` on send error, kept
    `pending` on busy/timeout.
  - **Step 2b:** replace the JSONL inbox from Phase 1 (Option A) with
    a query `SELECT * FROM events WHERE target_session_id = ? AND
    status = 'pending'`. The on-disk inbox file goes away; the CLI
    surface (`agent-deck inbox <session>`) keeps its name and
    semantics, just with a SQL backing.
- **Complexity.** Moderate, but the bulk lands in `internal/statedb`
  which already has the schema-migration pattern from
  `watcher_events`. Tests in the existing `transition_notifier_*`
  suite become integration tests against a temp SQLite file; mock
  storage drops in cleanly.
- **Observability.** SQL query answers everything. The watcher panel
  precedent shows what a TUI surface for this looks like.
- **Gaps closed:** **G-5 fully** (SQLite rows take the per-target
  serialization out of the persistence layer); **G-4 partially** (the
  notifier can now look at the prior row and dispute spurious
  error→running flips); deferred-tail loss closed because expiration
  becomes a row update, not a file delete. Launch-path gaps remain.
- **Why F over B.** F preserves the live tmux render that conductors
  depend on for their visible status line, and is implementable as a
  drop-in extension of Phase 1's inbox abstraction. B's "replace the
  send-keys" framing is correct on paper but would force a
  conductor-side rewrite before the persistence story is proven.

## 4. Comparison matrix

| Dimension | A (inbox file) | B (SQLite queue) | C (broker) | D (event source) | E (SSE) | F (hybrid) |
|---|---|---|---|---|---|---|
| Closes G-5 | No | Yes | Yes | Yes | Yes | Yes |
| Closes deferred-tail loss | Yes | Yes | Yes | Yes | Partial | Yes |
| Closes hygiene gaps (G-1/2/3) | No | No | No | No | No | No |
| Closes tool-coverage gaps (G-8/9/10) | No | No | No | No | No | No |
| Zero new infra | Yes | Yes | **No** | Yes | Yes | Yes |
| Single-binary preserved | Yes | Yes | **Degraded** | Yes | Yes | Yes |
| macOS+Linux+WSL parity | Yes | Yes (WAL caveats) | Hard | Yes | Yes | Yes |
| Migration cost | ~2 days | ~5 days | ~3 weeks | ~3 weeks | ~2 weeks | ~5 days (after A) |
| Survives daemon restart | Yes | Yes | Yes | Yes | No (alone) | Yes |
| Replay / forensics | Limited | Good | Good | **Excellent** | Limited | Good |
| Test surface in repo today | Existing JSON harness | Existing statedb harness | None | Partial | Existing web harness | Both A's + statedb |

## 5. Recommendation: phased path A → F

**Phase 1 (immediate, on `fix/805-event-pipeline`) — Option A:**

- Per-conductor inbox file at `<state-dir>/inboxes/<parent-id>.jsonl`.
- Retry-with-backoff on busy: 3 attempts at 5s / 15s / 45s before
  inbox.
- Top-level conductor self-suppress (children with empty
  `ParentSessionID` early-return before `NotifyTransition`).
- One-time WARN log on orphan detection pointing at the
  `set-parent` workflow already in
  `documentation/CONDUCTOR.md:216-…`.
- New CLI: `agent-deck inbox <session>` lists pending events.
- Tests added to the `internal/session/transition_notifier_*_test.go`
  suite covering: inbox-write-on-final-defer, inbox-replay-on-conductor-
  attach, self-suppress for empty `ParentSessionID`.

**This phase ships in v1.7.73 (next release).** It is incremental,
zero-infra, and addresses every issue #805 ships with — but does **not**
fix G-5 and does **not** fix the launch-path hygiene that drives the
98% drop. Those are tracked separately (see §6).

**Phase 2 (next milestone) — Option F:**

- Add an `events` table to `internal/statedb/statedb.go`. Schema and
  migration follow the `watcher_events` precedent.
- Notifier writes a row at the start of `prepareDispatch`
  (`transition_notifier.go:185`), updates it on terminal outcome.
- Replace the JSONL inbox from Phase 1 with a SQL view; the
  `agent-deck inbox` CLI keeps its surface.
- Add a sweeper that on every daemon poll updates rows aged past
  `defaultQueueMaxAge` from `pending` to `expired` and writes one
  `notifier-missed.log` line per row (no behavior change for the user
  log, just an internal storage swap).
- Tests rewritten as integration tests against a temp SQLite file.

**Phase 3 (out of scope of this RFC) — input hygiene:**

The 98% drop from issue #805 is fixed by closing G-1, G-2, G-3 in the
launch path. These are tracked as separate follow-up issues and the
work is not gated on Phase 2. Specifically:

- G-1: launch propagation test ensuring `agent-deck launch -parent
  <id>` persists `ParentSessionID` even when `AGENT_DECK_PARENT*` env
  is unset by a wrapper.
- G-2: worktree spawn parent-inheritance test.
- G-3: sandbox/docker spawn parent-inheritance test.

These are unit-test-shaped, not RFC-shaped, and should ship as a
"fix-805 hygiene bundle" PR alongside Phase 1 or shortly after.

**Phase 4 (later) — tool coverage (G-8/9/10):**

- G-8 gemini: either add a real `case "gemini"` in
  `terminalHookTransitionCandidate` (`transition_daemon.go:384-409`)
  or remove gemini from the outer gate at `:110` and document the
  detector-only path.
- G-9 opencode: same decision, but for opencode (currently absent
  from the gate).
- G-10 codex-compatible: replace `inst.Tool == "codex"` with
  `IsCodexCompatible(inst.Tool)` at `:110`, mirroring the existing
  `IsClaudeCompatible` usage.

These are 1-3 line gate changes plus tests. Not RFC-shaped.

## 6. Why not the other options

- **B (SQLite-only, no tmux render).** B is the right *destination*
  but premature. It forces a conductor-side rewrite before Phase 1's
  inbox semantics are proven in production. F achieves the same
  persistence with a smaller blast radius.
- **C (broker).** Violates the zero-infra single-binary constraint
  the project has held since v1.0. The 6 MB binary growth and the
  port/supervisor lifecycle on three OSes are not paid for by any
  observed failure mode.
- **D (event sourcing).** Right *eventual* destination after F has
  stabilized — the SQLite events table from F is a natural starting
  point. Doing it now is a 3-week rewrite that doesn't fix anything
  Phase 1 + Phase 2 + Phase 3 don't already fix.
- **E (SSE/WebSocket).** Wrong abstraction for the conductor surface,
  which is a tmux pane. The bridge process required to land SSE into
  a pane is itself a `tmux send-keys` caller — i.e. Option E reduces
  to Option A with extra HTTP. Keep the SSE infrastructure for the
  browser push use case it was built for
  (`internal/web/push_service.go`).

## 7. Constraints checklist

- **Zero-infra deployment.** A and F preserve. B preserves with a
  caveat (SQLite WAL is already a runtime dependency).
- **Single binary.** A, B, D, E, F preserve. C does not.
- **macOS + Linux + WSL.** A, F preserve trivially. B requires the
  same WAL discipline the watcher framework already has; reuse that.
- **Tests gate the change.** Both phases extend
  `internal/session/transition_notifier_*_test.go`. Phase 2 also
  extends the statedb test suite. Both phases must pass `go test
  ./internal/session/... -race -count=1` (existing mandate in
  `<repo-root>/CLAUDE.md`).
- **Eval harness.** Per `docs/rfc/EVALUATOR_HARNESS.md`, any change
  that affects user-observable status messaging must add at least one
  case in `tests/eval/`. Phase 1 adds: "given a child fires
  running→waiting while parent is busy, the [EVENT] line eventually
  appears in the parent pane, bounded by 3 minutes." Phase 2 inherits
  the same eval and adds: "given the daemon is restarted between
  defer and drain, the [EVENT] still arrives."

## 8. Open questions

These are flagged for the user before Phase 2 starts; Phase 1 has no
open questions because issue #805 already specified its surface.

1. **Inbox truncation policy.** Phase 1 uses
   "consumer-truncates-after-read." Phase 2 with SQLite has more
   options: hard delete, soft delete (`status = 'archived'`), TTL.
   Recommend soft delete with a 7-day TTL so forensics-after-the-fact
   stays possible.
2. **Per-target backoff vs per-(target, child) backoff.** Phase 1
   uses per-target. Per-(target, child) gives better fairness when
   one chatty child drowns out a quieter sibling, but doubles the
   slot map. Recommend deferring to Phase 2 and revisiting only if
   a real fairness incident is observed.
3. **Schema migration mechanics.** `internal/statedb/statedb.go`
   already has a migration pattern from `watcher_events`. Confirm
   the migration framework is reusable verbatim or whether the
   `events` table needs a slightly different shape.
4. **Eval-harness coverage tier.** Phase 1's eval cases are smoke-tier
   (sub-30s). Phase 2's "restart between defer and drain" case may
   need to live in the full tier (release-gate only) because it
   requires daemon restart timing. Recommend smoke for the happy
   path, full for the restart variant.

## 9. Mandate update (CLAUDE.md)

If this RFC is accepted, `<repo-root>/CLAUDE.md` gains a new section
mirroring the existing "Session persistence: mandatory test coverage"
and "Watcher framework: mandatory test coverage" sections:

> ## Transition pipeline: mandatory test coverage
>
> Any commit modifying `internal/session/transition_notifier.go`,
> `internal/session/transition_daemon.go`, the `events` table in
> `internal/statedb/statedb.go`, or the inbox CLI under
> `cmd/agent-deck/inbox_cmd.go` MUST run:
>
> ```
> go test ./internal/session/... ./internal/statedb/... \
>   -run "Transition|Notify|Inbox|Events" -race -count=1
> ```
>
> Removing per-target serialization, dropping the deferred-queue
> persistence path, or replacing the SQLite events table is an
> RFC-required structural change.

## 10. Decision

**Recommendation: phased path A → F.**

- Land Option A on `fix/805-event-pipeline` for v1.7.73.
- Schedule Option F for the next milestone after A has been in
  production for two weeks and the deferred-tail loss has been
  measured to confirm closure.
- Run the input-hygiene fixes (G-1/G-2/G-3) in parallel with A.
  They are not pipeline-shaped and should not be gated on it.
- Defer C, D, E indefinitely. Revisit D after F stabilizes; revisit
  E only if the web UI becomes a primary conductor surface.

This is not a punt. The recommendation is "ship A now, ship F next,
and don't pretend a fancier pipeline fixes a launch-path data
problem." The 98% drop number gets to ~5% after Phase 1 + Phase 3 land
together, and to <1% after Phase 2.
