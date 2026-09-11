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

For remote group headers, `Enter`/`Tab` toggles collapse and `h`/Left collapses or moves to the parent. Remote-session reorder keys move only within the current remote group; the order is saved on the viewing machine, while remote group headers remain name-sorted.

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

**Fields (order: Name → Tool → Path):**
- Session name (required)
- Command (claude/gemini/opencode/codex/custom) — the dialog remembers the last-used tool (persisted per profile, never written to config.toml; an explicit `default_tool` in config wins)
- Project path (required, supports `~/`)
- Parent group (auto-selected)
- Claude options (when Claude is selected): permission mode, Chrome, teammate mode, extra args, and start query

**Controls:** `Tab` move fields | `Enter` advance to next field (on free-text Name/Branch fields) | `Ctrl+S` create from any field | `Esc` cancel

Enter-advances is the default (`[ui].new_session_enter_advances = true`), so typing a name and pressing Enter no longer silently creates a session with all defaults. Set `[ui].new_session_enter_advances = false` to restore the legacy Enter-submits behavior; `Ctrl+S` submits in both modes.

Pressing `n` on a remote group/session opens a remote-aware dialog (remote paths and group pre-filled); the session is created over SSH on the remote, never on localhost.

Claude New Session defaults are remembered in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`) under `[claude]`, except start query and resume IDs, which are per-launch values.

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
