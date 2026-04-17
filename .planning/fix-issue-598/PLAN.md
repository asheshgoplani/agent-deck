# fix-issue-598 — cross-session `x` transfers unpredictable content

## Problem

Pressing `x` in the TUI to transfer output from session A → session B sometimes delivers stale content — an assistant response from before the current work, or content that "never matches what's on screen." Reporter observed it for every Claude ↔ {Claude, Codex, Gemini} combination on Windows 11 / WSL2.

## Reproducer (headless)

Unit-testable at the `internal/ui.getSessionContent` boundary: after a Claude session resume, `Instance.ClaudeSessionID` holds the *previous* session's UUID, pointing to a frozen JSONL. `GetLastResponse()` reads from that JSONL and returns "OLD" content while the live tmux env has a new `CLAUDE_SESSION_ID` for the current conversation.

## Data-flow trace

| # | File:func | What happens |
|---|-----------|--------------|
| 1 | `internal/ui/hotkeys.go` — `hotkeySendOutput="x"` | Key binding |
| 2 | `internal/ui/home.go` — `Update` handler around L9440 | Opens session_picker_dialog |
| 3 | `internal/ui/home.go:12445` `handleSessionPickerDialogKey` | On `enter`, dispatches `sendOutputToSession(source, target)` |
| 4 | `internal/ui/home.go:12400` `sendOutputToSession` | Calls `getSessionContent(source)` → wraps → `tmuxSession.SendKeysChunked` |
| 5 | `internal/ui/home.go:12574` **`getSessionContent`** | **BUG: calls `inst.GetLastResponse()` with stale `ClaudeSessionID`** |
| 6 | `internal/session/instance.go:3311` `GetLastResponse` | For Claude: `getClaudeLastResponse()` uses `i.ClaudeSessionID` directly, no refresh |
| 7 | `internal/session/instance.go:3430` `getClaudeLastResponse` | Reads `<CLAUDE_CONFIG_DIR>/projects/<dir>/<ClaudeSessionID>.jsonl` → returns last assistant message from that file |

Parallel paths that DO refresh correctly (shipped in prior fixes — confirms the pattern):
- `GetLastResponseBestEffort` (L3337) refreshes only *after* a failed primary lookup
- `session output` CLI (cmd/agent-deck/session_cmd.go:1867) uses BestEffort
- Status poller uses `GetSessionIDFromTmux` via `syncClaudeSessionFromTmux`

The TUI `x` path is the only live-user path that doesn't refresh before reading.

## Fix

1. **Add** `Instance.RefreshLiveSessionIDs()` in `internal/session/instance.go` — reads fresh `CLAUDE_SESSION_ID` (and Gemini equivalent) from tmux env, updates stored IDs only when a newer non-empty value is available. No-op when `tmuxSession == nil` or tool isn't agentic.
2. **Change** `internal/ui/home.go:getSessionContent` to:
   - Call `inst.RefreshLiveSessionIDs()` first
   - Use `GetLastResponseBestEffort()` (has richer fallback than `GetLastResponse`)
   - Keep the tmux scrollback fallback unchanged

Out of scope: the `session output` CLI path (already uses BestEffort), GetLastResponse itself (not changing its contract), tmux capture semantics.

## Failing tests (committed before fix)

File: `internal/ui/send_output_content_test.go` (new)

- `TestGetSessionContentWithLive_PrefersFreshIDOverStoredStaleID` — proves fix mechanism. Creates two JSONL files (`stale.jsonl`→"OLD", `fresh.jsonl`→"FRESH") under a fake `CLAUDE_CONFIG_DIR`. Stored `ClaudeSessionID = "stale"`. Passes a live `fresh` ID into the extracted `getSessionContentWithLive` helper. Expects "FRESH" content. **RED** on main — helper doesn't exist; current code would read "OLD".
- `TestGetSessionContentWithLive_KeepsStoredIDWhenLiveEmpty` — no live ID available → still returns stored JSONL content (back-compat).
- `TestGetSessionContentWithLive_NoOpForNonClaudeTool` — tool="shell" → live ID ignored, falls through to tmux capture path (here: returns error since tmuxSession is nil).

File: `internal/session/instance_test.go` (append)

- `TestInstance_RefreshLiveSessionIDs_NoOpWhenTmuxSessionNil` — constructs Instance with tmuxSession nil → method exists → no panic → no field change. **RED** on main (method doesn't exist — compile fail).
- `TestInstance_RefreshLiveSessionIDs_NoOpForNonAgenticTool` — Tool="shell" → no ClaudeSessionID update.

## Scope boundaries

Allowed to modify:
- `internal/session/instance.go` (add `RefreshLiveSessionIDs`)
- `internal/ui/home.go` (modify `getSessionContent`, add `getSessionContentWithLive`)
- `internal/ui/send_output_content_test.go` (new)
- `internal/session/instance_test.go` (append tests)
- `CHANGELOG.md`, `cmd/agent-deck/main.go` (version bump to v1.7.11)
- `.claude/release-tests.yaml` (append regression entries)

NOT modifying:
- tmux.go, watcher/, costs/, statedb/, feedback/, fork/, worktree code — unrelated
- CLI `session output` path — already correct via BestEffort
- GetLastResponse / GetLastResponseBestEffort signatures — risk surface too wide

## Live-boundary verify plan (Phase 7)

Build the fix binary. Launch two real agent-deck sessions on a Claude tool. In session A run a prompt, wait for response. Press `x`, select B. Repeat 5× — each time B must receive the response from A's *current* turn, not an earlier one. Compare against v1.7.10 to confirm regression existed.
