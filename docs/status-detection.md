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
| claude | Hooks: UserPromptSubmit / PreToolUse → running, Stop / PermissionRequest / Notification(permission) → waiting, SessionEnd → dead. Fresh for 2 min. PreToolUse (synchronous, status only) also covers turns that start without a typed prompt; PostToolUse is not subscribed, so a long tool or a long thinking phase falls back to the pane 2 min after its last hook. See [Claude status hooks](claude-status-hooks.md). | Spinner line `^[✳✽✶✻✢·] Word… (…)`, `esc to interrupt`, Braille spinner in pane title, `Waiting for N background agent to finish` | Bare `❯`, `❯ draft` between the two input-box rules, menu footers (`Enter to select`, `Enter to confirm`, `Allow once`, feedback picker), trust prompt | `API Error: 401`, `Please run /login`, `socket connection closed`, `Crunched for 0s` model-unavailable no-op |
| codex | `codex-notify` hook: turn start → running (fresh 20 s), turn end → waiting (fresh 5 s). Absent unless `codex-hooks install` ran. Pane title Braille spinner. | `• Word (9m 41s • esc to interrupt)` status line in the live slot (the last `•` block above the `› ` composer, only blank and `  └ …` lines between; `codexLiveStatusLine`), `esc to interrupt` within the last 3 lines, Braille spinner | `› ` composer (`Ask Codex to do anything`), `Press enter to confirm or esc to go back` | Column-0 `■` banners: usage limit, not logged in |
| gemini | Hooks BeforeAgent / AfterAgent (2 min) | `esc to cancel` | `gemini>`, `Type your message`, line ending in `>` | none |
| opencode | SSE `/event` stream, TUI only (30 s) | `thinking...`, `generating...`, pulse glyphs `█▓▒░` | `Ask anything`, `enter submit` | none |
| pi | Hooks turn_start / turn_end (2 min), excluded from the flip debounce | `── ⠹ Working ──` banner, `[subagent]`, `[running]` (a plain `delegate_task` in prose is not a cue) | `pi>`, the `↑… ↓… ` token/cost status line under the composer | none |
| hermes | Hooks around every LLM/tool call (2 min); gateway health probe every 30 s | Braille spinner anywhere | shell prompt | gateway unreachable |
| copilot | none | `Thinking`, `Running`, Braille | `copilot>`, `›`, `>` | none |
| shell / custom | none | opt-in `[status] shell_running_indicator` | `$ `, `# `, `% `, `❯ `, `(y/N)` | none |

## Precedence inside one frame (GetStatus)

1. Pane missing or dead → `inactive` → instance `error` / `stopped`.
2. Pane title carries a Braille spinner → `active` (every tool, no capture).
3. Capture the visible pane (no scrollback). Claude frames drop the agent
   roster and artifact rows drawn under the footer (`prepareFrame`, also in
   `GetSubstate` and the Stop-hook `BackgroundWorkSince`), so the prompt (8)
   and menu (15) windows see the input box. Before the trim, `prepareFrame`
   records the frame's background work (`tmux.ParseClaudeBackgroundWork`):
   the workflow row under the footer is drawn with a roster glyph and the
   trim would remove it.
4. Model-unavailable no-op → `error`; tool error banner → `error`.
5. Open Claude menu at the tail with no live busy cue near it → not busy.
6. Busy indicator (tool patterns over the last 25 lines, spinner scan, 6 s
   spinner grace) → `active`.
7. Claude background work in flight → `active` (issue #2473): a workflow row
   short of its last step (`○ name ▰▰▱ 3/5 · 18m32s`; a finished row stays at
   n/n), `Waiting for N background agents / dynamic workflows to finish` as
   the last turn line above the input box (an older one further up is
   history), or the live shell count on the footer. Monitor-only counters
   are passive watchers and do not promote the session.
   Never over an open menu, an error banner or the model-unavailable no-op
   (`backgroundWorkOutrankedLocked`): a menu blocks the turn on the operator
   and an error means no progress, so such a frame keeps its waiting / error
   verdict while a workflow runs under it. An open menu means menu chrome
   (`Enter to select`, `Esc to cancel`, `Allow once`, `No, and tell Claude
   what to do differently`, `Enter to confirm`, `0: Dismiss`, ...); a dialog
   question (`Do you want`, `Would you like`) counts only with a selected
   numbered option (`❯ 1.`) after it, because Claude often ends a reply with
   the same words in prose and that opens no menu.
8. Prompt indicator → `waiting` (or `idle` once the operator attached).
9. Otherwise history: `starting` inside the 2 min startup window, else the
   previous stable status / `waiting`.

Instance layer on top: a fresh hook verdict short-circuits the pane, except
that a `waiting` hook (the Stop that ends every foreground turn) never
overrides background work in flight: the pane (captured for the hook path,
never reusing a probe older than the hook event) OR the transcript (pending
Workflow / background Agent / background Bash launch with no
terminal `<task-notification>` yet, or Claude Code's `pendingWorkflowCount`
/ `pendingBackgroundAgentCount` on the last `turn_duration`) keeps the
session `running` with substate `background-work`. Transcript-only evidence
holds for 3 minutes after its newest sighting (redraws, resizes, a capture
that missed the footer); a workflow row whose task already reported back is
vetoed. The same merge runs on the tmux path, and the notify daemon's hook
candidate path skips a Stop that handed off to background work, so no
`running -> waiting` record is written until the work ends (session/
background_work.go). A menu or an error outranks background work on every
path: a frame showing an open menu, an error banner or the
model-unavailable no-op is never promoted to `running`, and a
`PermissionRequest` / `Notification(permission_prompt|elicitation_dialog)`
hook holds `waiting` without looking at the pane for its first 5 s (the
dialog is drawn just after the hook fires); after that the frame decides,
because a dialog dismissed with Esc fires no further hook. The daemon still
emits that event's `waiting` record (the child is blocked on input). The
record written when the work ends keeps its tier under the urgent rule: a
plain summary is `info`; only a sentinel, an error or a question to the
parent is `urgent`. The whole lifecycle (launch, running / background-work
with task and n/m on every poll, the notification turn, `waiting` on the
first poll after it, `idle` once acknowledged) is pinned end to end by
`TestAcceptance2473_SmallWorkflowLifecycle` (cmd/agent-deck) through the
`session show --json` and `list --json` code paths. The hook-lag rule flips
a stale `running` hook to `waiting` after two completed turn samples; a
purely pane-derived flip away from running is held for one sample
(`debounceFlipFromRunning`) when this process saw running itself; Codex
completion evidence bypasses that hold.

## Substates (additive, never change the colour except model-unavailable)

`running`, `idle-at-empty-prompt`, `interactive-menu`, `background-work`
(Claude at the prompt with a workflow, background agents or finite shells
still in flight; pairs with `running`, detail in `substate_detail` and the
`background_work` JSON object),
`watching` (armed Monitor or proven sleep-only until loop; waiting/idle,
watcher count in `background_work.count`),
`auth-401`, `usage-limit`, `model-unavailable`, `unknown-exit`, `hook-lag`.
`process-exited` (a custom command started with `add -cmd` / `launch -cmd`
has exited: status `idle` for exit 0, `error` for a non-zero exit, and the
code is reported as `exit_code` in `list --json`, `status -v --json`,
`session show --json`, remote rows, the status event detail and the
transition notification; a deliberate stop clears it and reads `stopped`; `session start` of such a command that finishes at once
still reports a successful start, whatever its exit code).

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

Staleness rules on the controller (classic row, embedded card, preview pane,
remote host/sub-group header counts and the filter-bar pill all follow them;
the header renders a stale remote's `● N ◐ N` dimmed, the pill leaves its
running rows out):

| Condition | Glyph | Text |
|---|---|---|
| poll ok, fetched < 30 s ago | live colour | — |
| poll ok, fetched ≥ 30 s ago | dimmed | `· status 47s old` |
| poll failed / timed out / auth failed, or rows from the on-disk startup cache | grey `?` | `· last known`, header `unreachable: reason` |

## Known blind spots

- Claude hooks give no signal between the last PreToolUse (or
  UserPromptSubmit) and Stop; after 2 min the pane decides. A self-started
  turn that thinks before its first tool keeps the previous Stop's waiting
  until the pane shows a live spinner above the composer or the hook ages out. A Claude turn that redraws without its spinner for more
  than the 6 s grace plus one debounce sample reads as waiting.
- Codex without `codex-hooks install` is pane-only; its title spinner and the
  `• Working (…)` line are the only running cues.
- OpenCode is pane-only outside the TUI (the SSE watcher is TUI-owned).
- Shell prompts that are not a prompt glyph (a `Password:` line) carry no cue.
- A remote whose `list --json` prints non-JSON (very old build) shows as zero
  sessions with a healthy poll.
- The remote snapshot age counts from arrival at the controller, not from the
  remote's own capture time.
- A remote row is that remote's own verdict: a remote still on 1.16.16 or
  older keeps reporting Claude background shells at an idle prompt as
  running whatever the controller runs, until the remote is updated.
- A Codex live status line pushed out of the live slot (a popup drawn between
  it and the composer) is not seen; the pane-title spinner still is.

### Passive watchers

Monitor tool receipts and the harness's live monitor-kind footer counter establish
an armed watcher. They do not keep a completed turn open or reset acknowledgment.
Finite transcript work wins over a monitor-only footer. A new foreground spinner
or running hook still means running. Menus and errors keep their existing priority.

A background Bash receipt is passive only when its correlated `tool_use.input.command`
is exactly `until [ -e|-f|-d PATH ]; do sleep SECONDS; done` (literal simple path,
no substitutions or trailing commands). Every shell in the footer must be accounted
for by such a pending receipt. Missing receipts, unmatched counts, arbitrary polling
commands and work after the loop remain running. This deliberately covers the
provable sleep-only form; it does not guess the purpose of general shell scripts.
The pane and transcript evidence source is retained in `background_work.source`.
Watcher-only transcript evidence uses the existing three-minute freshness bound.
Until-loop reclassification also requires receipts within that bound even when a
shell footer remains visible. Long-lived until loops revert to running unless fresh
identity evidence is available; Monitor-kind footer evidence does not expire.
