# Session status detection

How agent-deck decides whether a session is `running` (green), `waiting`
(yellow), `idle` (grey), `error` (red) or `stopped`, for every harness, local
and remote. Line numbers refer to the tree at the time of the 2026-09-23
status-detection audit.

## Two layers

| Layer | Where | Output |
|---|---|---|
| Pane frame | `internal/tmux/tmux.go` `Session.GetStatus` | `active` / `waiting` / `idle` / `starting` / `error` / `inactive` + a substate |
| Instance | `internal/session/instance.go` `Instance.updateStatus` | `running` / `waiting` / `idle` / `error` / `stopped`, after hooks, debounce and acknowledgement |

The pure oracle for the frame layer is `tmux.ClassifyPaneFrame(tool, frame)`
(`internal/tmux/frame_classify.go`); it is scored against the golden pane
corpus in `internal/tmux/testdata/status_corpus` by `pane_corpus_test.go`.

## Inputs per harness

| Harness | Live signal (wins while fresh) | Frame cues for running | Frame cues for waiting | Frame cues for error |
|---|---|---|---|---|
| claude | Hooks: UserPromptSubmit → running, Stop / PermissionRequest / Notification(permission) → waiting, SessionEnd → dead. Fresh for 2 min. No tool-use hooks, so a long turn falls back to the pane after 2 min. | Spinner line `^[✳✽✶✻✢·] Word… (…)`, `esc to interrupt`, Braille spinner in pane title, `Waiting for N background agent to finish` | Bare `❯`, `❯ draft` between the two input-box rules, menu footers (`Enter to select`, `Enter to confirm`, `Allow once`, feedback picker), trust prompt | `API Error: 401`, `Please run /login`, `socket connection closed`, `Crunched for 0s` model-unavailable no-op |
| codex | `codex-notify` hook: turn start → running (fresh 20 s), turn end → waiting (fresh 5 s). Absent unless `codex-hooks install` ran. Pane title Braille spinner. | `^• Word (9m 41s • esc to interrupt)` status line above the composer, `esc to interrupt` within the last 3 lines, Braille spinner | `› ` composer (`Ask Codex to do anything`), `Press enter to confirm or esc to go back` | Column-0 `■` banners: usage limit, not logged in |
| gemini | Hooks BeforeAgent / AfterAgent (2 min) | `esc to cancel` | `gemini>`, `Type your message`, line ending in `>` | none |
| opencode | SSE `/event` stream, TUI only (30 s) | `thinking...`, `generating...`, pulse glyphs `█▓▒░` | `Ask anything`, `enter submit` | none |
| pi | Hooks turn_start / turn_end (2 min), excluded from the flip debounce | `── ⠹ Working ──` banner, `[subagent]`, `[running]`, `delegate_task` | `pi>`, the `↑… ↓… R…` status line under the composer | none |
| hermes | Hooks around every LLM/tool call (2 min); gateway health probe every 30 s | Braille spinner anywhere | shell prompt | gateway unreachable |
| copilot | none | `Thinking`, `Running`, Braille | `copilot>`, `›`, `>` | none |
| shell / custom | none | opt-in `[status] shell_running_indicator` | `$ `, `# `, `% `, `❯ `, `(y/N)` | none |

## Precedence inside one frame (GetStatus)

1. Pane missing or dead → `inactive` → instance `error` / `stopped`.
2. Pane title carries a Braille spinner → `active` (every tool, no capture).
3. Capture the visible pane (no scrollback). Claude frames drop the agent
   roster and artifact rows drawn under the footer (`prepareFrame`), so a
   session with many sub-agents keeps its spinner inside the detector windows.
4. Model-unavailable no-op → `error`; tool error banner → `error`.
5. Open Claude menu at the tail with no live busy cue near it → not busy.
6. Busy indicator (tool patterns over the last 25 lines, spinner scan, 6 s
   spinner grace) → `active`.
7. Claude awaiting a background agent → `active`.
8. Prompt indicator → `waiting` (or `idle` once the operator attached).
9. Otherwise history: `starting` inside the 2 min startup window, else the
   previous stable status / `waiting`.

Instance layer on top: a fresh hook verdict short-circuits the pane; the
hook-lag rule flips a stale `running` hook to `waiting` after two completed
turn samples; a purely pane-derived flip away from running is held for one
sample (`debounceFlipFromRunning`) when this process saw running itself;
Codex completion evidence bypasses that hold.

## Substates (additive, never change the colour except model-unavailable)

`running`, `idle-at-empty-prompt`, `interactive-menu`, `background-work`
(Claude at the prompt with `N shells still running` / `· N shells ·`),
`auth-401`, `usage-limit`, `model-unavailable`, `unknown-exit`, `hook-lag`.

## Who computes, how often, what persists

| Surface | Source | Cadence | Writes |
|---|---|---|---|
| TUI | long-lived Instances + hook file watcher | 2 s, backing off to 10 s | `status` column on change |
| notify daemon, TUI alive | reads the TUI's `status` column | 1–3 s | — |
| notify daemon, no TUI | reloads Instances per pass, probes tmux; carries the previous pass's verdict and pending flip (`livePrior`) so the one-sample hold works | 1–3 s | `status` column on change |
| `list --json`, `session show`, remote agent probe | one fresh pass, hooks cold-loaded, pane read; a persisted `running` is not treated as an observation | per call | nothing |

## Remote sessions

`agent-deck remote sessions` runs `agent-deck list --json` over SSH on each
remote, so a remote row is the remote host's own detector result at poll time
(same code, same frame rules); the controller does no re-detection and no
status mapping. The controller polls every 15 s (config `remote_session_refresh_secs`),
keeps the last good rows when a poll fails, and stamps `remoteFetchedAt`.

Staleness rules on the controller (classic row, embedded card, preview pane
and header counts all follow them):

| Condition | Glyph | Text |
|---|---|---|
| poll ok, fetched < 30 s ago | live colour | — |
| poll ok, fetched ≥ 30 s ago | dimmed | `· status 47s old` |
| poll failed / timed out / auth failed, or rows from the on-disk startup cache | grey `?` | `· last known`, header `unreachable: reason` |

## Known blind spots

- Claude hooks give no signal between UserPromptSubmit and Stop; after 2 min
  the pane decides. A Claude turn that redraws without its spinner for more
  than the 6 s grace plus one debounce sample reads as waiting.
- Codex without `codex-hooks install` is pane-only; its title spinner and the
  `• Working (…)` line are the only running cues.
- OpenCode is pane-only outside the TUI (the SSE watcher is TUI-owned).
- Shell prompts that are not a prompt glyph (a `Password:` line) carry no cue.
- A remote whose `list --json` prints non-JSON (very old build) shows as zero
  sessions with a healthy poll.
- The remote snapshot age counts from arrival at the controller, not from the
  remote's own capture time.
