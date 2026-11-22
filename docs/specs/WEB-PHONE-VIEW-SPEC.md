# Web UI: opt-in phone view with a read-only chat

**Date:** 2026-11-18
**Status:** Draft
**Scope:** `internal/web/static/app` (frontend), `internal/web` (one config key, one flag), `tests/web`
**Discussion:** #3124 (step 2 of 3)

## Problem

On a phone the web UI is the desktop layout squeezed down. The only way into a
session is the terminal, which on a small screen means reading a narrow pane
and typing through the soft keyboard. What a phone user mostly wants is to see
what an agent said and whether it is waiting on them.

Step 1 of #3124 built the read side: `GET /api/sessions/{id}/timeline` (#3157),
Pi rows (#3159, #3162) and `GET /api/remotes/{name}/sessions/{id}/timeline`
(#3163) all answer the same `agent-deck.recall.rows/v2` object. This spec covers
step 2: a phone layout that shows that object as a chat. Sending and approvals
are step 3 and are out of scope here.

## Switch

Off by default. Nothing changes for anyone who does not turn it on.

- Config key `[web] phone_view = true|false` (default `false`), a `UserConfig`
  field documented in `skills/agent-deck/references/config-reference.md`.
- Flag `agent-deck web --phone-view[=false]` overrides the key for one run.
- The server reports the effective value in the existing settings payload. The
  frontend reads it once at load. Names are open to change in review.

With the switch on, the phone layout is used when the viewport matches
`(max-width: 720px), (max-height: 500px) and (pointer: coarse)`, so a phone in
landscape counts too. A "desktop layout" toggle in the phone view lets a user
fall back for that browser (stored in `localStorage`).

## Layout

Two screens instead of the desktop panes:

1. **Session list.** Groups and sessions as in the sidebar, with each
   session's status and whether it is waiting on the user. Tapping a session
   opens screen 2.
2. **Session.** Header with title, status and a back button. Body is the
   **Chat** tab (default) or the **Terminal** tab.

The Terminal tab is the existing xterm view, attached as a watcher
(`ignore-size`, #3158), so opening it on a phone never resizes the shared tmux
window for anyone else.

## Chat

- Data: `GET /api/sessions/{id}/timeline?tail=60` for local sessions and the
  remote route for remote ones. New rows come from `?since=<through_cursor>`.
  Older pages from `?before=<before_cursor>&limit=60` when the user scrolls up.
- Updates: poll with the `since` cursor while the chat is on screen, and stop
  when it is not. A per-session stream that sends the same rows from a cursor
  can replace polling later, as agreed in #3124. No change to `/events/menu`.
- `409 RESYNC_REQUIRED` reloads from the tail. `503 RECALL_DISABLED` shows a
  one-line notice with the config key to set. Any other error keeps the rows
  already shown and retries on the next poll.
- Rows to bubbles: `user` and `assistant` rows are messages. Tool rows (`tool`,
  `bash`, `edit`, `read`, `subagent`, ...) are one collapsed line with the row's
  `title` and `summary`, expanding to `body`. `thinking`, `system` and
  `compaction` rows are dim one-liners. `other` rows are hidden. The mapping
  lives in one pure function with unit tests.
- Assistant text goes through the shared Markdown renderer (tables, nested
  lists, fenced code with highlighting). All text is escaped. No raw HTML.
- This is the frontend renderer in `miniMarkdown.js`, which builds Preact
  nodes from row text in the browser. It is separate from the server-side
  Markdown in `internal/openpages` (#3175, open), which renders a whole file
  that a session opened as a page. Chat rows arrive as JSON text from the
  timeline routes and are rendered where they are shown, like the Command
  Center and Fleet board summaries that already use `miniMarkdown.js`.

## PRs

| # | PR | Contents |
|---|---|---|
| 1 | Markdown renderer for chat | `miniMarkdown.js`, `codeHighlight.js`, unit tests, this spec |
| 2 | Chat tab next to Terminal | rows-to-bubbles mapping, chat view on the timeline routes, polling, paging. Desktop shows the tab too, with Terminal as the default there |
| 3 | Phone mode | the switch, the two-screen layout, the desktop-layout fallback |

Each PR comes with vitest units, and PR 2 and 3 with Playwright e2e and
screenshot baselines regenerated in the pinned Playwright image.

## Out of scope

- Sending from the chat, approval cards, remote send (step 3).
- Harnesses the rows reader does not stream directly (Gemini, OpenCode and the
  rest).
- Push notifications changes.
