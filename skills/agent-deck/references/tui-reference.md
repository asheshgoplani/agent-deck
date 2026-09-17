# TUI Reference

Complete reference for agent-deck Terminal UI features.

## Keyboard Shortcuts

Reconciled against the in-app help overlay (`?`), which is the source of truth
(`internal/ui/help.go`). Keys marked **rebindable** can be remapped under
`[hotkeys]` in `config.toml`; the rest are fixed.

### Navigation

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `Ctrl+u` / `Ctrl+d` | Half page up / down |
| `PgUp` / `PgDn` | Half page up / down |
| `Ctrl+f` / `Ctrl+b` | Full page up / down |
| `Home` / `End` | Jump to first / last item |
| `gg` | Jump to top |
| `G` | Global search |
| `h` / `←` | Collapse group / go to parent |
| `l` / `→` / `Tab` | Toggle expand/collapse group |
| `1-9` | Jump to Nth root group |
| `Space` | Jump mode |
| `Enter` | Attach to session OR toggle group |
| `Shift+Enter` | Open session in new iTerm window (macOS) |

### Group Navigation

| Key | Action |
|-----|--------|
| `Alt+j` / `Alt+k` | Next / previous session in group |
| `Alt+1` - `Alt+9` | Jump to Nth session in group |
| `Alt+g` / `Alt+G` | First / last session in group |
| `Alt+/` | Filter search within group |

### Session Actions

| Key | Action |
|-----|--------|
| `Enter` | Attach to session OR toggle group |
| `n` / `N` | New session / quick create (**rebindable**) |
| `r` | Rename session or group (**rebindable**) |
| `R` | Restart session, reloads MCPs (**rebindable**) |
| `T` | Restart with a new session ID (**rebindable**) |
| `d` | Delete session or group (**rebindable**) |
| `D` | Close session process (**rebindable**) |
| `Ctrl+Z` | Undo delete (**rebindable**) |
| `A` | Archive session — stops tmux, hides from default list; conversations/metadata untouched (**rebindable**) |
| `Shift+U` | Unarchive session; does NOT auto-start tmux (**rebindable**) |
| `^` | Toggle archived view (**rebindable**) |
| `M` | Move session to a different group (**rebindable**) |
| `m` | MCP Manager (Claude/Gemini/Cursor) (**rebindable**) |
| `L` | Plugin Manager (Claude) (**rebindable**) |
| `s` | Skills Manager (**rebindable**) |
| `$` | Cost Dashboard |
| `v` | Cycle preview mode: output / stats / both (**rebindable**) |
| `O` | Toggle preview orientation (right / below — portrait monitors) |
| `<` / `>` | Shrink / grow preview pane by 5% (or drag the divider with the mouse) |
| `u` | Mark unread, idle -> waiting (**rebindable**) |
| `a` | Quick approve — sends `1` to Claude (**rebindable**) |
| `o` | Prompt session — send a one-line prompt without attaching (**rebindable**) |
| `y` | Toggle YOLO mode (**rebindable**) |
| `+` / `K` / `Shift+↑` | Move item up (auto-promotes a sub-session to top-level at the parent's first child) |
| `-` / `J` / `Shift+↓` | Move item down (auto-promotes a sub-session to top-level at the parent's last child) |
| `Shift+→` / `Shift+←` | Indent / outdent within current group (single-level nesting) |
| `,` | Pin (cycles off -> top -> bottom -> off) |
| `f` / `F` | Quick fork / fork with options (Claude/OpenCode/Pi/Codex) (**rebindable**) |
| `x` | Send output to another session (**rebindable**) |
| `E` | Exec shell in sandbox container (**rebindable**) |
| `H` | Open shell in session's worktree, split pane / window (**rebindable**) |
| `p` | Edit multi-repo paths (**rebindable**) |
| `P` | Edit session settings — title / color / ... (**rebindable**) |
| `e` | Edit notes; hidden when notes are disabled (**rebindable**) |
| `b` | Re-run worktree setup script `.agent-deck/worktree-setup.sh` (**rebindable**) |
| `W` | Finish worktree — merge + cleanup (**rebindable**) |
| `w` | Watcher panel (**rebindable**) |

For remote group headers, `Enter`/`Tab` toggles collapse and `h`/Left collapses or moves to the parent. A remote host header shows `v1.15.0 ↑` after its count when the remote runs an older agent-deck than this controller (the version is asked once per hour per remote on the session poll); `u` on that header opens "Update remote <name> from v<old> to v<new>?" and runs the same verified deploy as `agent-deck remote update <name>`. Remote-session reorder keys move only within the current remote group; the order is saved on the viewing machine, while remote group headers remain name-sorted.

### Copy & Text Selection

Dragging with the mouse does **not** select text: the TUI puts the terminal in
mouse reporting mode (`tea.WithMouseCellMotion`) so that click-to-select,
wheel scrolling and the divider drag work, which means the terminal never sees
your drag as a selection gesture.

| Key | Action |
|-----|--------|
| `c` | Copy last AI response (**rebindable**) |
| `C` | Copy session info — repo / path / branch |
| `V` | Copy visible terminal text, links included (**rebindable**) |
| `Y` | Copy a fenced code block from output; opens a picker when there are several |
| `Shift+drag` | Native terminal selection — bypasses mouse reporting |
| `Option+drag` | Native terminal selection in iTerm2 |

Note the family is only half-rebindable: `c` and `V` are `copy_output` and
`copy_pane` under `[hotkeys]`, while `C` and `Y` are fixed.

All four copy paths use the same clipboard chain, falling back to OSC 52 so they
work over SSH. If your terminal offers no selection bypass at all, you can turn
off tmux mouse mode for attached sessions — at the cost of tmux scrolling, pane
resize and mouse copy mode:

```toml
[tmux]
mouse = false
```

That setting affects **attached sessions only**; the agent-deck list view keeps
its own mouse capture regardless.

### Group Actions

| Key | Action |
|-----|--------|
| `g` | Create group (subgroup if on group) (**rebindable**) |
| `r` | Rename group (**rebindable**) |
| `Tab` | Toggle expand |

### Search & Filter

| Key | Action |
|-----|--------|
| `/` | Local search, fuzzy (**rebindable**) |
| `G` | Global search (all Claude conversations) |
| `Tab` | Switch between local/global search |
| `0` | Clear filter (show all) |
| `!` / `Shift+1` | Filter: running only (toggle) |
| `@` / `Shift+2` | Filter: waiting only (toggle) |
| `#` / `Shift+3` | Filter: idle only (toggle) |
| `&` | Filter: errors only (toggle) |
| `%` | Filter: open only, hides errors (toggle) |
| `^` | Filter: view archived sessions (toggle) |
| `t` | Cycle group view: active-on-top / populated-on-top (**rebindable**) |
| `*` | Cycle time filter: today / 3 days / 7 days / all (**rebindable**) |

Inside the search prompt, `/waiting`, `/running` and `/idle` filter by status.

### Global

| Key | Action |
|-----|--------|
| `?` | Help overlay (**rebindable**) |
| `S` | Settings (**rebindable**) |
| `i` | Import existing tmux sessions (**rebindable**) |
| `Ctrl+R` | Manual refresh / reload from disk (**rebindable**) |
| `Ctrl+Q` | Detach, keeps tmux running (**rebindable**) |
| `Ctrl+S` | Switch session, here or attached — unbound by default (**rebindable**) |
| `PageUp` | Scrollback pager, while attached |
| `Alt+A` | Agents panel; appears once an agent is adopted (**rebindable**) |
| `$` | Cost Dashboard |
| `Ctrl+Y` | Install the available update now (`install_update`; runs `agent-deck update` on the terminal, see [Updates](#updates)) |
| `Ctrl+T` | Restart agent-deck in place now (`restart_deck`; the new build starts with the same args, env and selection) |
| `q` / `Ctrl+C` | Quit (**rebindable**) |

### Worktree Shortcuts

| Key | Action |
|-----|--------|
| `n` -> `w` | Create session in a worktree |
| `F` -> `w` | Fork session into a worktree |

### Startup Flags

| Flag | Action |
|------|--------|
| `--group <name>` | Launch scoped to a group |
| `--profile <name>` | Use a specific profile |

## Local Status Indicators

| Symbol | Status | Color | Meaning |
|--------|--------|-------|---------|
| `●` | Running | Green | Active, content changed in last 2s |
| `◐` | Waiting | Yellow | Stopped, unacknowledged |
| `○` | Idle | Gray | Stopped, acknowledged |
| `✕` | Error | Red | tmux session doesn't exist |
| `⟳` | Starting | Yellow | Session launching |

Federated remote rows currently carry coarse running/waiting/idle/error status; local Honest Status substates are not included in the remote payload.

## Dialogs

### New Session (`n`)

**Fields (order: Name → Tool → Model → Reasoning effort → Path):**
- Session name (required)
- Command (claude/gemini/opencode/codex/custom) — the dialog remembers the last-used tool (persisted per profile, never written to config.toml; an explicit `default_tool` in config wins)
- Model ID (claude/codex/gemini/opencode): empty means the tool default; `↓` or `Space` opens the list of known IDs, or type any ID (CLI: `--model`)
- Reasoning effort (claude/codex): `←`/`→` or `Space` cycles the levels (CLI: `--effort`)
- Project path (required, supports `~/`)
- Parent group (auto-selected)
- Claude options (when Claude is selected): permission mode, Chrome, teammate mode, extra args, start query, and the account row (CLI: `--account`; hidden when no named accounts are configured)
- `[ Create session ]` button (last row)

**Controls:** `Enter` next field on every row (inside the Claude options it steps row by row) | `Tab`/`Shift+Tab` and `↓`/`↑` move fields the same way | `Enter` on `[ Create session ]` or `Ctrl+S` anywhere creates | `Esc` back out of a list, then cancel

Enter-advances is the default (`[ui].new_session_enter_advances = true`): Enter never creates the session until you reach the Create button, so typing a name and pressing Enter through the form no longer launches a session before you have chosen the model, path, or options. The footer on every row says what Enter does there. Set `[ui].new_session_enter_advances = false` to restore the legacy behavior where Enter creates from any row; `Ctrl+S` creates in both modes.

Pressing `n` on a remote group/session opens a remote-aware dialog (remote paths and group pre-filled); the session is created over SSH on the remote, never on localhost.

Claude New Session defaults are remembered in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`) under `[claude]`, except start query and resume IDs, which are per-launch values.

### Edit Session (`Shift+P`)

Edits the fields a session iterates on at runtime: Title, Harness (tool), Pin position, the account row (shown when `[profiles.<name>.*].config_dir` slots are configured for a supported harness), and for claude sessions Skip permissions, Auto mode, Extra args, Plugins.

**Controls:** `Tab`/`↓` `Shift+Tab`/`↑` move rows | `←`/`→` choose (pills) | `Space` toggle (checkboxes) | `Enter` save | `Esc` cancel. The footer says what the keys do on the focused row; on a changed harness or account row it reads "Enter switch (asks first)".

A harness or account change is saved on its own (not together with other edits) and always asks first: a same-harness account change shows "Switch Account?" (from → to, what happens), a different harness shows the transfer disclosure. `y`/Switch runs it, `n`/Esc returns to the row with nothing written; the outcome is reported in a notice. CLI equivalents: `agent-deck session switch-account <session> <account>` (same flow), `agent-deck session set <session> account <name>`, `agent-deck session switch <session> --to-harness <tool> [--to-account <account>]` (preview with `session switch-preview`); `agent-deck accounts` lists the slots.

### MCP Manager (`m`)

**Layout:**
- Two columns: Attached | Available
- Two scopes: LOCAL | GLOBAL

**Controls:**
- `Tab` - Switch scope
- `←/→` - Switch columns
- `↑/↓` - Navigate
- `Type letters/digits` - Jump to MCP name prefix
- `Space` - Toggle MCP
- `Enter` - Apply changes
- `Esc` - Cancel

**Indicators:**
- `(l)` LOCAL scope
- `(g)` GLOBAL scope
- `(p)` PROJECT scope
- `🔌` MCP is pooled
- `⟳` Pending restart

### Skills Manager (`s`)

**Layout:**
- Two columns: Attached | Available
- Available is pool-only (`source=pool`)
- Column headers include counts (for example: `Attached (3)`, `Available (28)`)

**Controls:**
- `←/→` - Switch columns
- `↑/↓` - Navigate (scrolls long lists)
- `Type letters/digits` - Jump to skill name prefix
- `Space` - Move skill between columns
- `Enter` - Apply changes
- `Esc` - Cancel

**Persistence:**
- Writes attachment state to `<project>/.agent-deck/skills.toml`
- Claude-compatible sessions materialize selected entries in `<project>/.claude/skills`
- Gemini, Codex, and Pi sessions materialize selected entries in `<project>/.agents/skills`
- If no pool entries exist, dialog shows guidance for `~/.agent-deck/skills/pool`

**Runtime notes:**
- Skills Manager is available for Claude, Gemini, Codex, and Pi sessions
- Pressing `Enter` reconciles managed attachments to the active runtime root even if the attached list did not change
- Auto-restart after apply is supported for Claude, Gemini, and Codex; Pi requires manual reload/restart

### Fork Dialog (`F`)

**Fields:**
- Session title (pre-filled)
- Group (auto-selected)

**Controls:** `Enter` fork | `Esc` cancel

### Delete Confirmation (`d`)

**For sessions:** Warning about tmux kill, process termination

**For groups:** Sessions move to default (not deleted)

**Controls:** `y` confirm | `n`/`Esc` cancel

## Search

### Local Search (`/`)

- Fuzzy search session titles and groups
- Max 10 results
- `↑/↓` or `Ctrl+K/J` navigate
- `Enter` select | `Tab` switch to global | `Esc` close

### Global Search (`G`)

- Full content search across `~/.claude/projects/`
- Regex + fuzzy matching
- Recency ranking
- Split view: results + preview
- `[/]` scroll preview
- `Enter` create/jump to session

**Config:**
```toml
[global_search]
enabled = true
recent_days = 30
```

## Preview Pane

- Shows last ~500 lines of session's tmux pane
- Auto-updates every 2 seconds
- Launch animation: 6-15s for Claude/Gemini

## Updates

The TUI checks for a new release on startup and every 5 minutes, and watches its own binary on disk once per tick (one `stat`; a `version` probe only when the file changed). What happens next depends on `[updates]` in config.toml (both default to `true`, both also in the Settings panel under UPDATES):

| Setting | On (default) | Off |
|---------|--------------|-----|
| `auto_install` | When a release is installable the TUI runs `agent-deck update --unattended --trigger tui` in the background (no prompt, the deck stays usable). One attempt per version per hour; a failure shows one footer line ("auto-update to vX failed: ...; run agent-deck update"). Skipped for Homebrew-managed installs, while a release is still publishing, and under `AGENTDECK_SKIP_UPDATE_CHECK`. | Banner: `⬆ Update available: vA → vB (... press ctrl+y to install (agent-deck update) · Esc to dismiss)` for 6+ releases behind; `Ctrl+Y` installs interactively. |
| `auto_restart` | Once a newer build is on disk the banner reads `⬆ vX installed, restarting when idle (ctrl+t now)` and the TUI restarts itself at the first tick with no dialog open, no insert mode and no session action in flight (attached sessions are never interrupted: the restart only happens from the home screen). After the restart the footer says `restarted into vNEW (was vOLD)` and the cursor is back on the session it was on. | Banner: `⬆ vX installed, press ctrl+t to restart agent-deck`; `Ctrl+T` restarts when you choose. |

Before the restart is armed (by the key or on its own) the TUI checks the file it is about to exec: it must be a regular, non-empty, executable file that answers `agent-deck version`; otherwise the restart is refused with a footer message (`restart blocked: new binary ... ; still running vOLD`) and the old build keeps running. The restart replaces the process in place (same executable path, args and environment), so tmux sessions, MCP pools and the web server are untouched. `web --no-tui` restarts itself the same way when no request is in flight; a remote agent exits cleanly instead and the controller reconnects. None of this happens on its own when the TUI is driven by a test, CI or a script: under `go test`, with `AGENTDECK_SKIP_UPDATE_CHECK` set, with `CI` truthy, with an `AGENTDECK_TEST_*` marker, or without a terminal on stdin and stdout, the banner reverts to `press ctrl+t to restart agent-deck` and only the keys act (issue #2251).

## Layout

- **< 50 cols:** List only
- **50-79 cols:** Stacked (list above preview)
- **80+ cols:** Side-by-side (default)

## Tool Icons

| Tool | Icon | Color |
|------|------|-------|
| Claude | 🤖 | Orange |
| Gemini | ✨ | Purple |
| OpenCode | 🌐 | Cyan |
| Codex | 💻 | Cyan |
| Cursor | 📝 | Blue |
| Shell | 🐚 | Default |

## Color Scheme (Tokyo Night)

| Element | Color |
|---------|-------|
| Accent (selection) | #7aa2f7 |
| Running | #9ece6a |
| Waiting | #e0af68 |
| Error | #f7768e |
| Groups | #7dcfff |
| Background | #1a1b26 |
| Surface | #24283b |

## Hidden Features

- **`Ctrl+K/J`:** Vim-style navigation in search
- **Numbers 1-9:** Jump to root groups instantly
- **Status filters are toggles:** Press again to turn off
