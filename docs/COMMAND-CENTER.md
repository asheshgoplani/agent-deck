# Command Center

The Command Center is the embedded, live, two-way **fleet god-view** inside
`agent-deck web`. It productizes the hand-made conductor status HTMLs into a real
feature: a first-class tab that renders cross-project status live off an SSE feed,
surfaces the decisions waiting on you, and routes typed instructions to Maestro or
a chosen conductor through the supported `session send` path.

It is private by construction — loopback by default, token-gated for any
non-loopback bind, never public — inheriting the web server's existing security
model (`auth.go`, `bind.go`, `csrf.go`, the mutation gate + rate limiter). It adds
no new server, no new daemon, and no new auth surface.

## v1 (shipped)

A "Command Center" tab (positioned first) in the existing Preact SPA, backed by
three additive endpoints registered on the existing mux behind the existing gates.

### Endpoints (`internal/web/handlers_command_center.go`)

| Endpoint | Method | Purpose |
|---|---|---|
| `/api/command-center/status` | GET | One-shot synthesized cross-project snapshot |
| `/events/command-center` | GET (SSE) | Live push of the snapshot, fingerprint-diffed + heartbeat (same pattern as `/events/menu`) |
| `/api/command-center/ask` | POST | Two-way input: route an instruction to Maestro / a conductor via `session send`; mutation-gated, CSRF fail-closed, target-allowlisted, argv-safe |

### What it does

- **Live, no polling.** `/events/command-center` subscribes to the same menu
  change channel the menu SSE uses, so a session transition pushes a new snapshot
  instantly. The page never polls; it re-renders off a Preact signal.
- **Completion notifications.** The stream diffs each session's status between
  consecutive snapshots server-side; a `running → done/waiting` transition is
  emitted in `recentlyCompleted`, and the panel fires a "✅ X just finished"
  toast (de-duplicated by completion id so reconnects don't re-fire).
- **See-everything, noise filtered.** Per-conductor rows show each conductor's
  health plus its active child sessions and what each is working on (from the
  session's latest activity). **Error and stopped sessions are filtered out** by
  construction. Honest-status v2 substates (`model-unavailable`, `auth-401`,
  `idle-at-empty-prompt`) are surfaced distinctly rather than hidden as "running".
- **Decisions waiting on you.** Parsed live from the agent-deck conductor's
  `OPEN-ITEMS.md` §D, each with a 💬 comment affordance that prefills the input.
- **Two-way input.** An input box (with a Maestro/conductor target picker) POSTs
  to `/api/command-center/ask`, which delivers via `agent-deck -p <profile>
  session send <target> "[command-center] <text>" --no-wait`. The text is passed
  as a single argv element (never interpolated into a shell string), the target
  is validated against a server-authoritative allowlist (Maestro + live
  `conductor-*` sessions), and every ask is journaled (correlation id + target +
  text-hash, never the raw text). Replies reflect back through the SSE feed as the
  fleet moves.

### Frontend (`internal/web/static/app/`)

- `panes/CommandCenterPane.js` — the panel.
- `Topbar.js` — the "Command Center" tab (first) with a decisions-waiting badge.
- `main.js` — `startCommandCenterSSE()` subscribes to `/events/command-center`,
  updates `commandCenterSignal`, and fires completion toasts.
- `state.js` — `commandCenterSignal`.
- `app.css` — the `.cc-*` styles (reuse the existing design tokens).

### Snapshot shape

```jsonc
{
  "profile": "personal",
  "generatedAt": "2026-06-15T...",
  "conductors": [
    { "name": "agent-deck", "target": "conductor-agent-deck",
      "status": "running", "substate": "",
      "currentlyWorkingOn": "v1.9.67 release wave",
      "sessions": [ { "id": "...", "title": "fix-1431", "status": "running",
                      "substate": "", "workingOn": "investigating spawn race" } ],
      "counts": { "running": 1, "waiting": 0, "idle": 0 } }
  ],
  "totals": { "running": 1, "waiting": 0, "idle": 0 },
  "decisionsWaiting": [ { "id": "#1361", "question": "...", "route": "conductor-agent-deck" } ],
  "recentlyCompleted": [ { "id": "...", "title": "X", "status": "waiting", "at": "..." } ],
  "askTargets": [ "maestro", "conductor-agent-deck" ]
}
```

No secret-shaped field (token/key/credential) is ever in the payload — only which
account/conductor, never the credential. (Asserted in
`handlers_command_center_test.go`.)

### Tests

- `internal/web/handlers_command_center_test.go` — synthesis (grouping, noise
  filtering, substate surfacing, completion diff), `OPEN-ITEMS.md` §D parsing, the
  status endpoint, SSE initial+change, `/ask` target allowlist + mutation gate +
  empty-text rejection, target resolution.
- `tests/web/e2e/command-center.spec.js` — Playwright: live render, conductor
  rows + input bar, **live SSE update without reload**, **error/stopped
  filtering**, two-way input routing (the web-UI TDD gate).

## v2 (planned — follow-on PR)

v2 deepens interactivity and per-project depth. It is additive to the v1 surface
and ships as its own review-gated PR.

1. **Connected per-project detail pages.** Navigable, in-app explain/detail views
   per project (the ryan/poster/opengraphdb-style pages from the prototype):
   click a conductor row → a detail panel with its roadmap, recent releases, what
   each session is doing in depth, and a back affordance. Rendered from the same
   snapshot plus the conductor's richer status artifacts.
2. **Comment-on-anything.** A 💬 on every entity (conductor, session, decision,
   recently-completed) that prefills the input with that entity's context
   (`re <id>: …`) and, on send, attaches a `context` object to `/ask` so the
   receiving conductor knows exactly what is being commented on. (v1 already
   prefills for decisions; v2 generalizes it to all rows and wires `context`.)
3. **Correlated reply read-back.** `/ask` records the correlation id + target; a
   follow-up SSE event reads the target's latest response via the existing
   `session output` path and shows it inline under the input — the chat-style
   "you asked X, conductor replied Y" (design §5.3 fidelity tier 2). v1 reflects
   replies via the live fleet-state SSE (tier 1); v2 adds the explicit read-back.
4. **Keyboard navigation.** Arrows move row focus, Enter opens/expands, Esc backs
   out / blurs the input, digit-jump (1–9) to a row, `/` to focus the input —
   matching the prototype's nav model.
5. **Compact plain-language layout.** A density toggle + a one-paragraph
   plain-language fleet summary (an optional, debounced, Maestro-routed LLM
   synthesis — never on the hot path), for the "tell me what's going on" glance.
6. **Structured per-conductor `status.json` contract.** v1 already reads an
   optional `conductor/<name>/status.json` `{headline}` when present (falling back
   to the live latest-prompt). v2 formalizes the full contract
   (`{headline, inProgress[], recentlyDone[], decisionsWaiting[], updatedAt}`) and
   has conductors write it on their heartbeat, replacing free-text parsing with
   structured signal.

## v3 (gated on OpenGraphDB — not in scope here)

Swap the interim local synthesis for an OGDB operational-knowledge-graph backend
behind a single `StatusSource` interface (the renderer is unchanged), adding a
free-form "ask anything about everything" query. Gated on OGDB R5/R6/R7/R10 + an
agreed operational-KG schema co-designed with conductor-opengraphdb. See the full
design doc (`conductor/agent-deck/COMMAND-CENTER-DESIGN.md` §7) for the seam.
