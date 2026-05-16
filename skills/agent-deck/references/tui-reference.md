# TUI Reference

Complete reference for agent-deck Terminal UI features.

## Keyboard Shortcuts

### Navigation

| Key | Action |
|-----|--------|
| `j` / `↓` | Move down |
| `k` / `↑` | Move up |
| `h` / `←` | Collapse group / go to parent |
| `l` / `→` / `Tab` | Toggle expand/collapse group |
| `1-9` | Jump to Nth root group |

### Session Actions

| Key | Action |
|-----|--------|
| `Enter` | Attach to session OR toggle group |
| `n` | New session (inherits current group) |
| `r` | Rename session or group |
| `R` | Restart session (reloads MCPs) |
| `K` / `Shift+↑` | Move item up (auto-promotes a sub-session to top-level when at the parent's first child) |
| `J` / `Shift+↓` | Move item down (auto-promotes a sub-session to top-level when at the parent's last child) |
| `Shift+→` / `Shift+←` | Indent / outdent within current group (single-level nesting) |
| `M` | Move session to different group |
| `m` | Open MCP Manager (Claude/Gemini) |
| `s` | Open Skills Manager |
| `d` | Delete session or group |
| `u` | Mark unread (idle -> waiting) |
| `f` | Quick fork (Claude only) |
| `F` | Fork with options (Claude only) |

### Group Actions

| Key | Action |
|-----|--------|
| `g` | Create group (subgroup if on group) |
| `r` | Rename group |

### Search & Filter

| Key | Action |
|-----|--------|
| `/` | Local search (fuzzy) |
| `G` | Global search (all Claude conversations) |
| `Tab` | Switch between local/global search |
| `0` | Clear filter (show all) |
| `!` | Filter: running only (toggle) |
| `@` | Filter: waiting only (toggle) |
| `#` | Filter: idle only (toggle) |
| `$` | Filter: error only (toggle) |

### Global

| Key | Action |
|-----|--------|
| `?` | Help overlay |
| `i` | Import existing tmux sessions |
| `Ctrl+R` | Manual refresh |
| `Ctrl+Q` | Detach (keep tmux running) |
| `q` / `Ctrl+C` | Quit |

## Status Indicators

| Symbol | Status | Color | Meaning |
|--------|--------|-------|---------|
| `●` | Running | Green | Active, content changed in last 2s |
| `◐` | Waiting | Yellow | Stopped, unacknowledged |
| `○` | Idle | Gray | Stopped, acknowledged |
| `✕` | Error | Red | tmux session doesn't exist |
| `⟳` | Starting | Yellow | Session launching |

## Dialogs

### New Session (`n`)

**Fields:**
- Session name (required)
- Project path (required, supports `~/`)
- Command (claude/gemini/opencode/codex/custom)
- Parent group (auto-selected)
- Claude options (when Claude is selected): permission mode, Chrome, teammate mode, extra args, and start query

**Controls:** `Tab` move fields | `Enter` create | `Esc` cancel

Claude New Session defaults are remembered in `~/.agent-deck/config.toml` under `[claude]`, except start query and resume IDs, which are per-launch values.

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
