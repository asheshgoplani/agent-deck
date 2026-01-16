# Agent Deck

Terminal session manager for AI coding agents. Built with Go + Bubble Tea. **Version**: 0.8.14

## Agent Deck Skill (for Claude Code)

**Marketplace:** `asheshgoplani/agent-deck`
**Skill:** `agent-deck@agent-deck`

```bash
# Install (one-time)
/plugin marketplace add asheshgoplani/agent-deck
/plugin install agent-deck@agent-deck

# Update
/plugin marketplace update agent-deck
```

**Skill location:** `skills/agent-deck/` in this repo

**Update workflow:**
1. Edit files in `skills/agent-deck/`
2. Commit and push to GitHub
3. Users run `/plugin marketplace update agent-deck`

## CRITICAL: Data Protection Rules

**THIS SECTION MUST NEVER BE DELETED OR IGNORED**

### tmux Session Loss Prevention

**NEVER DO THESE THINGS:**
1. **NEVER run `tmux kill-server`** - Destroys ALL agent-deck sessions instantly
2. **NEVER run `tmux kill-session` with patterns** - `tmux ls | grep agentdeck | xargs tmux kill-session` DESTROYS ALL SESSIONS
3. **NEVER quit Terminal.app/iTerm completely** while sessions running - tmux server may not survive
4. **NEVER restart macOS** without exporting important session outputs first
5. **NEVER run tests that might interfere with production tmux sessions**
6. **NEVER run cleanup commands** targeting "agentdeck" patterns - Claude in dangerous mode may do this autonomously

**Incidents:**
- **2025-12-09**: 37 sessions lost when tmux server restarted (metadata in `sessions.json` intact, tmux sessions destroyed)
- **2025-12-10**: Claude in dangerous mode ran `tmux ls | grep agentdeck | cut -d: -f1 | xargs -I{} tmux kill-session -t {}` - killed ALL 40 sessions

**Recovery:**
```bash
# Session logs preserved in ~/.agent-deck/logs/
tail -500 ~/.agent-deck/logs/agentdeck_<session-name>_<id>.log

# Metadata backups (rolling 3 generations):
~/.agent-deck/profiles/default/sessions.json.bak{,.1,.2}
```

### Test Isolation (CRITICAL)

**2025-12-11 Incident**: Tests with `AGENTDECK_PROFILE=work` overwrote ALL 36 production sessions.

**Fix Applied:**
- `TestMain` in all packages sets `AGENTDECK_PROFILE=_test`
- Storage safeguard forces `_test` profile if production detected during tests
- Test data isolated to `~/.agent-deck/profiles/_test/`

**Files**: `internal/ui/testmain_test.go`, `internal/session/testmain_test.go`, `internal/tmux/testmain_test.go`, `cmd/agent-deck/testmain_test.go`

### GitHub Actions Require Permission (CRITICAL)

**🚨 NEVER post to GitHub without explicit user permission! 🚨**

**NEVER do these without asking first:**
- Post comments on GitHub issues
- Post comments on GitHub PRs
- Create new issues or PRs
- Close/merge/edit any GitHub content
- Any `gh` command that modifies GitHub state

**Always:** Draft the content, show it to the user, and wait for explicit "yes, post it" permission.

**Incident (2026-01-05):** Almost posted a reply to Issue #20 without permission. User stopped it just in time.

### Public Repository - NO Private Data

**🚨 CRITICAL: NEVER push personal documentation to GitHub! 🚨**

**IMPORTANT POLICY:**
- **ALL personal documentation MUST go in `docs/` folder**
- The `docs/` folder is excluded via `.git/info/exclude` (local only, not committed)
- Any file you think is personal and shouldn't be public → put it in `docs/`
- When creating planning docs, marketing materials, or personal notes → create in `docs/`

**NEVER push:**
- API keys, tokens, passwords, secrets
- `~/.agent-deck/config.toml`, `~/.claude-work/.claude.json`
- Session logs/data, personal documents
- **docs/ folder** - ALL personal documentation goes here
- Personal markdown files in root (traction strategy, marketing, research notes)

**Setup (already configured):**
```bash
# docs/ folder is excluded via .git/info/exclude
cat .git/info/exclude | grep docs/
# Output: docs/

# Before ANY commit, check for secrets:
git diff --cached | grep -iE "(api.?key|token|password|secret).*=.*[a-zA-Z0-9]{10,}"

# Before pushing, verify no docs/ files:
git log --name-only origin/main..HEAD | grep "^docs/"
```

**Policy for new files:**
```bash
# ✅ Good - personal docs in docs/ folder (excluded)
echo "# My personal notes" > docs/my-research.md

# ❌ Bad - personal docs in root (tracked by git)
echo "# My personal notes" > my-research.md

# When in doubt, put it in docs/!
```

### Duplicate Instance Prevention (2025-12-22)

Two instances caused 160-240 tmux subprocess spawns/sec, 131%+ CPU.

**Protection:** PID lock at `~/.agent-deck/profiles/{profile}/.lock` with atomic creation, stale cleanup, signal handling.

### Ghost Session Optimization (2025-12-22)

Sessions in `sessions.json` without tmux sessions ("ghosts") only rechecked every 30s instead of 500ms.
**Implementation:** `internal/session/instance.go` - `errorRecheckInterval`, `lastErrorCheck` field

### Runaway Log Prevention (2025-12-22)

Single session generated 1.6GB logs in minutes, crashing tmux server.

**Fix:** Periodic log maintenance every 5 minutes (not just startup). See `[logs]` config below.

### Session ID Overwrite Bug (2025-12-26, CRITICAL FIX)

**Symptom:** Claude/Gemini session ID changes unexpectedly when restoring sessions, causing fork/resume to use wrong conversation.

**Root Cause:** `UpdateClaudeSession()` and `UpdateGeminiSession()` used a 5-minute timestamp check:
```go
// BROKEN: If timestamp was stale (loaded from storage), file scanning would overwrite valid ID
if i.ClaudeSessionID != "" && time.Since(i.ClaudeDetectedAt) < 5*time.Minute {
    return  // Skip only if recent
}
// File scanning here could overwrite a valid stored session ID!
```

When sessions were loaded from storage with old `ClaudeDetectedAt` timestamps, the 5-minute check would fail, triggering file scanning that could overwrite the valid stored session ID with a different one.

**Fix (2025-12-26):** Session IDs are now NEVER overwritten by file scanning once set:
```go
// FIXED: Trust existing session IDs unconditionally
// 1. tmux environment takes priority (authoritative source from capture-resume)
if sessionID := i.GetSessionIDFromTmux(); sessionID != "" {
    i.ClaudeSessionID = sessionID  // Can update from tmux env
    return
}
// 2. If we already have an ID, trust it - just refresh timestamp
if i.ClaudeSessionID != "" {
    i.ClaudeDetectedAt = time.Now()  // Prevent repeated detection
    return  // NO file scanning, NO overwrite
}
// 3. File scanning only for NEW sessions (empty ID)
```

**Key Principle:** Stored session IDs are trusted data. Only tmux environment (set during active session capture) can update an existing ID.

**Files:**
- `internal/session/instance.go` - Fixed `UpdateClaudeSession()` and `UpdateGeminiSession()`
- `internal/session/instance_test.go` - Added regression tests

**Tests:** `TestInstance_UpdateClaudeSession_NeverOverwriteExisting`, `TestInstance_UpdateGeminiSession_NeverOverwriteExisting`

### Cross-Profile Contamination Bug (2025-12-26, CRITICAL FIX)

**Symptom:** All sessions disappear when creating a new session in a different profile (e.g., creating in work profile wipes out default profile sessions).

**Root Cause:** `storage_watcher.go:90` used `filepath.Base()` to match filenames, which only compared "sessions.json" vs "sessions.json" WITHOUT checking the full path. This meant:
1. Work profile TUI saves → `/profiles/work/sessions.json` changes
2. Default profile watcher sees event for "sessions.json"
3. `filepath.Base()` check PASSES (both are "sessions.json")
4. Default TUI reloads from its own file
5. If timing is wrong or file is stale → EMPTY instances replace all sessions

**Fix (2025-12-26):** Changed to compare FULL ABSOLUTE paths instead of just basename:
```go
// Before (BROKEN):
if filepath.Base(event.Name) != filepath.Base(sw.storagePath) {
    continue
}

// After (FIXED):
eventPath, _ := filepath.Abs(event.Name)
watchPath, _ := filepath.Abs(sw.storagePath)
if eventPath != watchPath {
    continue
}
```

**Additional Fixes:**
- Added `isReloading` check in `saveInstances()` to prevent saving during reload window
- Added `isReloading` check in `statusUpdateMsg` handler
- Added debug logging to track reload events

**IMPORTANT:** After updating, you MUST restart ALL TUI instances for the fix to take effect:
```bash
# Kill all agent-deck TUI processes
pkill -f "^agent-deck"  # Or quit each TUI with 'q'

# Restart TUIs
agent-deck           # Default profile
agent-deck -p work   # Work profile
```

**Files:**
- `internal/ui/storage_watcher.go` - Fixed path matching, added debug logging
- `internal/ui/home.go` - Added `isReloading` checks before saves
- `internal/session/storage.go` - Added debug logging for empty loads

### Missing TestMain Files Bug (2025-12-26, CRITICAL FIX)

**Symptom:** Test data (sessions with title "new-name", path "/tmp/project") appears in DEFAULT profile, overwriting production sessions. This happens when running `go test ./...` or any test commands.

**Root Cause:** TestMain files were MISSING from 3 packages:
- ❌ `internal/ui/testmain_test.go` - MISSING
- ❌ `internal/tmux/testmain_test.go` - MISSING
- ❌ `cmd/agent-deck/testmain_test.go` - MISSING
- ✅ `internal/session/testmain_test.go` - existed

Without TestMain, tests in these packages used the DEFAULT profile instead of `_test` profile, causing test data from `internal/ui/home_test.go` (which creates sessions like "original-name" renamed to "new-name" in "/tmp/project") to overwrite production data.

**Fix (2025-12-26):** Created TestMain files for ALL packages that interact with storage:
```go
// internal/ui/testmain_test.go (and others)
func TestMain(m *testing.M) {
    os.Setenv("AGENTDECK_PROFILE", "_test")
    os.Exit(m.Run())
}
```

**NEVER DELETE THESE FILES:**
- `internal/ui/testmain_test.go`
- `internal/tmux/testmain_test.go`
- `cmd/agent-deck/testmain_test.go`
- `internal/session/testmain_test.go`

### NotifySave Timing Race (2025-12-26, FIX)

**Symptom:** Background saves could overwrite external changes because the 500ms ignore window started too early.

**Root Cause:** `NotifySave()` was called 18-25 lines BEFORE `SaveWithGroups()`:
```go
// BROKEN: NotifySave 25 lines before actual save
h.storageWatcher.NotifySave()  // Line 2289
// ... 25 lines of defensive checks, snapshots, etc ...
h.storage.SaveWithGroups(...)  // Line 2315
```

Under load, the 500ms window could expire before the save completed.

**Fix:** Moved `NotifySave()` to immediately before `SaveWithGroups()`:
```go
// FIXED: NotifySave immediately before save
// ... all defensive checks first ...
h.storageWatcher.NotifySave()  // Line 2313
h.storage.SaveWithGroups(...)  // Line 2317
```

**Files:** `internal/ui/home.go` - Lines 2309-2317 (saveInstances), Lines 2506-2511 (attachSession)

### Handler State Corruption Race (2025-12-26, FIX)

**Symptom:** Creating/forking/deleting sessions during reload could corrupt state.

**Root Cause:** Handlers like `sessionCreatedMsg`, `sessionForkedMsg`, `sessionDeletedMsg` modified `h.instances` WITHOUT checking `isReloading` first. If a reload was in progress, the modifications would be overwritten by `loadSessionsMsg`, but the handler had already modified `groupTree` inconsistently.

**Fix:** Added `isReloading` check at START of these handlers:
```go
case sessionCreatedMsg:
    if h.isReloading {
        log.Printf("[RELOAD-DEBUG] sessionCreatedMsg: skipping during reload")
        return h, nil
    }
    // ... rest of handler
```

**Files:** `internal/ui/home.go` - Lines 1059-1066, 1115-1119, 1165-1169

---

## Quick Start

```bash
make build      # Build to ./build/agent-deck (symlink updates /usr/local/bin/agent-deck)
make test       # Run tests
```

**Dependencies:** `brew install tmux jq`

**Dev symlink:** `sudo ln -sf /Users/ashesh/claude-deck/build/agent-deck /usr/local/bin/agent-deck`

---

## Installation

```bash
# Quick install
curl -fsSL https://raw.githubusercontent.com/asheshgoplani/agent-deck/main/install.sh | bash

# Options: --name <name> --dir <path> --version <ver> --skip-tmux-config --non-interactive
```

**Homebrew:** `brew install asheshgoplani/tap/agent-deck`
**Go:** `go install github.com/asheshgoplani/agent-deck/cmd/agent-deck@latest`

**Clipboard by platform:** macOS: `pbcopy` | WSL: `clip.exe` | Linux Wayland: `wl-copy` | Linux X11: `xclip -in -selection clipboard`

---

## CLI Commands

```bash
agent-deck                              # Interactive TUI
agent-deck add /path -t "Title" -g "group" -c claude  # Add session
agent-deck list [--json]                # List sessions
agent-deck remove <id|title>            # Remove session
agent-deck status [-v|-q|--json]        # Status check (no TUI)
agent-deck uninstall [--dry-run|--keep-data]  # Uninstall
agent-deck version / help
```

**Add flags:** `-t/--title`, `-g/--group`, `-c/--cmd` (claude/gemini/opencode/codex/cursor/custom)

### Session Commands

| Command | Description |
|---------|-------------|
| `session start <id>` | Start a stopped session |
| `session stop <id>` | Stop a running session |
| `session restart <id>` | Restart session (Claude/Gemini: resumes with updated MCPs) |
| `session fork <id>` | Fork Claude session (flags: `-t/--title`, `-g/--group`) - Claude only |
| `session attach <id>` | Attach to session (PTY mode) |
| `session show [id]` | Show session details (current session if no id) |
| `session current` | Auto-detect current session and profile (supports `-q`, `--json`) |
| `session set <id> <field> <value>` | Update session property (title, path, command, tool, claude-session-id, gemini-session-id) |
| `session send <id> <msg>` | Send message to running session |
| `session output [id]` | Get last response (Claude: JSONL, Gemini: JSON, others: terminal) |

### MCP Commands (Claude and Gemini)

| Command | Description |
|---------|-------------|
| `mcp list` | List all available MCPs from config |
| `mcp attached [id]` | List MCPs attached to session (current if no id) |
| `mcp attach <id> <mcp>` | Attach MCP to session (Claude: `--global`, `--restart`; Gemini: always global) |
| `mcp detach <id> <mcp>` | Detach MCP from session (Claude: `--global`, `--restart`; Gemini: always global) |

### Group Commands

| Command | Description |
|---------|-------------|
| `group list` | List all groups |
| `group create <name>` | Create group (flag: `--parent` for subgroup) |
| `group delete <name>` | Delete group (flag: `--force` to delete with sessions) |
| `group move <id> <group>` | Move session to group |

### Global Flags

| Flag | Description |
|------|-------------|
| `--json` | JSON output (for automation) |
| `-q/--quiet` | Minimal output |
| `-p/--profile` | Use specific profile |

### Session Resolution

Commands accept flexible session identifiers:
- **Title**: `session start "My Project"` (exact or fuzzy match)
- **ID prefix**: `session start a1b2` (unique prefix match)
- **Path**: `session start /path/to/project`
- **Current session**: Omit ID in tmux to use `AGENTDECK_SESSION_ID` env var

**Implementation:** `cmd/agent-deck/session_cmd.go`, `mcp_cmd.go`, `group_cmd.go`, `cli_utils.go`

### Auto-Detect Current Session

**NEW:** Use `session current` to auto-detect the current session and profile:

```bash
# Get current session and profile
agent-deck session current
# Output: Session: test, Profile: work, ID: c5bfd4b4, Status: running, Path: ~/claude-deck

# For scripting (just session name)
agent-deck session current -q
# Output: test

# For automation (JSON)
agent-deck session current --json
# Output: {"session":"test","profile":"work","id":"c5bfd4b4",...}

# Use in workflows
PARENT=$(agent-deck session current -q)
PROFILE=$(agent-deck session current --json | jq -r '.profile')
agent-deck -p "$PROFILE" add -t "Subtask" --parent "$PARENT" -c claude /tmp/subtask
```

**Profile auto-detection:**
- Priority 1: `AGENTDECK_PROFILE` environment variable
- Priority 2: Parse from `CLAUDE_CONFIG_DIR` (`~/.claude-work` → `work`)
- Priority 3: Fall back to config default or `default`

**Implementation:** `cmd/agent-deck/session_cmd.go` → `handleSessionCurrent()`, `internal/profile/detect.go`

---

## Project Structure

```
cmd/agent-deck/
├── main.go               # Entry point + CLI
├── session_cmd.go        # session start/stop/restart/fork/attach/show/current
├── mcp_cmd.go            # mcp list/attached/attach/detach
├── group_cmd.go          # group list/create/delete/move
└── cli_utils.go          # Session resolution, JSON output helpers
internal/
├── ui/                       # TUI (Bubble Tea)
│   ├── home.go               # Main model, Update(), View()
│   ├── styles.go             # Tokyo Night colors
│   ├── group_dialog.go       # Group create/rename/move
│   ├── newdialog.go          # New session dialog
│   ├── help.go, search.go, menu.go, preview.go, tree.go, list.go
├── platform/                 # Platform detection
│   └── platform.go           # WSL1/WSL2/macOS/Linux detection
├── profile/                  # Profile detection
│   └── detect.go             # Auto-detect current profile from environment
├── session/                  # Data layer
│   ├── instance.go           # Session struct, Status enum
│   ├── groups.go             # GroupTree, hierarchy
│   ├── storage.go            # JSON persistence
│   ├── config.go             # Profile management
│   ├── userconfig.go         # TOML config, getMCPPoolConfigSection()
│   └── discovery.go          # Import tmux sessions
└── tmux/                     # tmux integration
    ├── tmux.go               # Session management, status detection
    ├── detector.go           # Tool/prompt detection
    └── pty.go                # PTY attach/detach
```

---

## Keyboard Shortcuts

### Navigation
| Key | Action |
|-----|--------|
| `j`/`↓`, `k`/`↑` | Move down/up |
| `h`/`←` | Collapse group (or go to parent) |
| `l`/`→`/`Tab` | Toggle expand/collapse |

### Session Actions
| Key | Action |
|-----|--------|
| `Enter` | Attach to session OR toggle group |
| `n` | New session (inherits current group) |
| `r`/`R` | Restart session (Claude/Gemini: resumes with updated MCPs) |
| `M` | MCP Manager (Claude and Gemini) |
| `e` | Rename session/group |
| `m` | Move session to different group |
| `d` | Delete session/group |
| `u` | Mark unread (idle → waiting) |
| `K`/`J` | Move item up/down in order |
| `f` | Fork session (Claude only, quick) |
| `F` | Fork with dialog (Claude only) |

### Group Actions
| Key | Action |
|-----|--------|
| `g` | New group (subgroup if on group) |

### View Actions
| Key | Action |
|-----|--------|
| `/` | Search sessions (fuzzy) |
| `G` | Global Search (all Claude conversations) |
| `?` | Help overlay |
| `i` | Import existing tmux sessions |
| `r` | Refresh sessions |

### Quick Filters
| Key | Filter |
|-----|--------|
| `0` | All sessions |
| `!` | Running only |
| `@` | Waiting only |
| `#` | Idle only |
| `$` | Error only |

### Global
| Key | Action |
|-----|--------|
| `Ctrl+Q` | Detach (tmux keeps running) |
| `q`/`Ctrl+C` | Quit |

---

## MCP Manager (Claude and Gemini)

Press `M` to attach/detach MCP servers.

**Claude scopes:**
| Scope | Writes To | Effect |
|-------|-----------|--------|
| LOCAL | `.mcp.json` in project | Project-only MCPs |
| GLOBAL | Claude config | All projects |

**Gemini:** All MCPs are global (writes to `~/.gemini/settings.json`)

**Controls:** `Tab` switch scope | `←/→` switch columns | `↑/↓` navigate | `Space` toggle | `Enter` apply | `Esc` cancel

**Config (`~/.agent-deck/config.toml`):**
```toml
[mcps.exa]
command = "npx"
args = ["-y", "exa-mcp-server"]
env = { EXA_API_KEY = "your-key" }
description = "Web search via Exa AI"
```

**Preview indicators:** `(l)` LOCAL | `(g)` GLOBAL | `(p)` PROJECT
**Sync:** Normal=active | Yellow `⟳`=pending restart | Dim `✕`=stale

---

## Gemini CLI Integration

Agent-deck supports Google's Gemini CLI with full session management:

- **Session Detection**: Automatic from `~/.gemini/tmp/<hash>/chats/`
- **Session Resume**: Press `r` on Gemini session to resume with `gemini --resume <id>`
- **MCP Management**: Press `M` to configure MCP servers (global scope only)
- **Response Extraction**: `agent-deck session output <id>` extracts last response

**Limitations:** No fork support (use sub-sessions instead), global MCPs only (no project-level)

**Session ID Capture Pattern:**
```bash
session_id=$(gemini --output-format stream-json -i 2>/dev/null | head -1 | jq -r '.session_id') && \
tmux set-environment GEMINI_SESSION_ID "$session_id" && \
gemini --resume "$session_id"
```

**Verified Technical Details:**
- Project hash: SHA256 of absolute path
- Session format: JSON with camelCase (`sessionId`, not `session_id`)
- Message type: `"type": "gemini"` (not `role: "assistant"`)
- MCP config: `~/.gemini/settings.json`

**Key files:** `internal/session/gemini.go`, `internal/session/gemini_mcp.go`

See `GEMINI_INTEGRATION.md` for comprehensive guide.

---

## Fork Session (Claude Only)

**Requirements:** Claude running, valid `lastSessionId` in Claude config

**Config:**
```toml
[claude]
config_dir = "~/.claude-work"  # Custom profile
dangerous_mode = true          # --dangerously-skip-permissions
```

### Session ID Capture (Capture-Resume Pattern)

Agent-deck uses the capture-resume pattern to reliably get session IDs:

```bash
# Capture session ID by running Claude with minimal prompt
session_id=$(claude -p "." --output-format json 2>/dev/null | jq -r '.session_id')
if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
  tmux set-environment CLAUDE_SESSION_ID "$session_id"
  claude --resume "$session_id" --dangerously-skip-permissions
else
  claude --dangerously-skip-permissions  # Fallback: start fresh
fi
```

**Why capture-resume (not `--session-id`):**
- Claude's `--session-id` flag only works for RESUMING existing sessions
- It does NOT work for creating NEW sessions (Claude ignores the passed ID)
- Capture-resume ensures we get the actual session ID Claude creates

**Key functions:** `buildClaudeCommand()` in `internal/session/instance.go`

**Note:** Requires `jq` for JSON parsing. Falls back to starting Claude fresh if capture fails.

---

## Global Search

Press `G` to search ALL Claude conversations in `~/.claude/projects/`.

**Features:** Full content search, auto-scroll to match, keyword highlighting, match count, recency ranking, fuzzy fallback

**Controls:** `↑/↓` navigate | `[/]` scroll preview | `Enter` open | `Tab` local search | `Esc` close

**Config:**
```toml
[global_search]
enabled = true
recent_days = 30
tier = "auto"  # "instant" (<100MB), "balanced", or "auto"
```

---

## Core Concepts

### Session (instance.go)
```go
type Instance struct {
    ID, Title, ProjectPath, GroupPath, Command, Tool string
    Status    Status    // running, waiting, idle, error
    CreatedAt time.Time
}
```

### Status Indicators
| Status | Symbol | Color | Meaning |
|--------|--------|-------|---------|
| Running | `●` | Green #9ece6a | Busy indicator OR content changed <2s |
| Waiting | `◐` | Yellow #e0af68 | Stopped, unacknowledged |
| Idle | `○` | Gray #565f89 | Stopped, acknowledged |
| Error | `✕` | Red #f7768e | Session doesn't exist |

### Groups (groups.go)
Path-based hierarchy: `"parent/child/grandchild"`
- `CreateGroup()`, `CreateSubgroup()`, `DeleteGroup()`, `RenameGroup()`, `Flatten()`
- Empty groups persist until explicitly deleted

### Storage (`~/.agent-deck/sessions.json`)
```json
{
  "instances": [{"id": "...", "title": "...", "project_path": "...", "group_path": "...", "command": "claude", "tool": "claude", "status": "waiting"}],
  "groups": [{"name": "Projects", "path": "projects", "expanded": true, "order": 0}]
}
```

---

## Configuration (`~/.agent-deck/config.toml`)

```toml
[claude]
config_dir = "~/.claude-work"  # Custom Claude profile
dangerous_mode = true          # --dangerously-skip-permissions (default: false)

# Custom CLI tools (Issue #42) - Configure ANY coding assistant
# All fields except 'command' are optional. See "Custom CLI Tools" section below.
[tools.my-ai]
command = "my-ai"
icon = "🧠"
busy_patterns = ["thinking...", "processing..."]  # Shows GREEN when matched
prompt_patterns = ["> ", "Ready:"]                # Shows YELLOW when matched
detect_patterns = ["MyAI v"]                      # Auto-detect tool from content
resume_flag = "--continue"                        # CLI flag for session resume
session_id_env = "MYAI_SESSION"                   # tmux env var for session ID
session_id_json_path = ".id"                      # jq path to extract session ID
output_format_flag = "--json"                     # Flag to get JSON output
dangerous_flag = "--yes"                          # Flag to skip confirmations
dangerous_mode = true                             # Enable dangerous flag by default

[logs]
max_size_mb = 1       # Max before truncation (default: 10)
max_lines = 2000      # Lines to keep (default: 10000)
remove_orphans = true # Clean logs for deleted sessions

[global_search]
enabled = true
recent_days = 30
tier = "auto"

[mcps.example]
command = "npx"
args = ["-y", "package-name"]
env = { API_KEY = "..." }
description = "Description"

[mcp_pool]
enabled = true        # Enable socket pooling (default: false)
pool_all = true       # Pool ALL available MCPs (default: false)
exclude_mcps = []     # MCPs to exclude from pool
fallback_to_stdio = true  # Fallback if pool fails
```

---

## Custom CLI Tools (Issue #42)

Agent Deck supports ANY CLI coding assistant through `config.toml` configuration.

### Configuration Fields

| Field | Type | Description |
|-------|------|-------------|
| `command` | string | Command to run (required) |
| `icon` | string | Icon shown in TUI |
| `busy_patterns` | []string | Strings indicating tool is working (GREEN status) |
| `prompt_patterns` | []string | Strings indicating tool needs input (YELLOW status) |
| `detect_patterns` | []string | Strings to auto-detect tool from terminal content |
| `resume_flag` | string | CLI flag to resume session (e.g., `--resume`) |
| `session_id_env` | string | tmux env var for session ID (e.g., `VIBE_SESSION_ID`) |
| `session_id_json_path` | string | jq path to extract session ID (e.g., `.session_id`) |
| `output_format_flag` | string | Flag to get JSON output (e.g., `--output-format json`) |
| `dangerous_flag` | string | Flag to skip confirmations (e.g., `--auto-approve`) |
| `dangerous_mode` | bool | Enable dangerous flag by default |

### Implementation Files

| File | Purpose |
|------|---------|
| `internal/session/userconfig.go` | `ToolDef` struct, getter functions |
| `internal/session/instance.go` | `buildGenericCommand()`, `GetGenericSessionID()`, `CanRestartGeneric()` |
| `internal/tmux/tmux.go` | `hasBusyIndicator()` custom patterns, `DetectTool()` custom detection |
| `internal/tmux/detector.go` | `HasPrompt()` custom patterns |

### Pattern Matching Priority

1. **Custom patterns** (from config.toml) - checked FIRST
2. **Built-in patterns** (for claude, gemini, opencode, codex) - fallback

### Session Resume Pattern

For tools with `resume_flag`, `session_id_env`, `output_format_flag`, and `session_id_json_path` configured:

```bash
# Capture-resume command (built by buildGenericCommand)
session_id=$(my-ai --output-format json "." 2>/dev/null | jq -r '.session_id' 2>/dev/null) || session_id=""
if [ -n "$session_id" ] && [ "$session_id" != "null" ]; then
  tmux set-environment MYAI_SESSION "$session_id"
  my-ai --resume "$session_id" --yes  # dangerous_flag if enabled
else
  my-ai --yes  # fallback: start fresh
fi
```

### Example: Adding Mistral Vibe

```toml
[tools.vibe]
command = "vibe"
icon = "🎵"
busy_patterns = ["executing", "running tool", "searching"]
prompt_patterns = ["approve?", "[y/n]", "vibe> ", "> "]
detect_patterns = ["mistral vibe", "██████████"]
dangerous_flag = "--auto-approve"
```

Then: `agent-deck add -t "My Project" -c vibe /path/to/project`

---

## MCP Socket Pool

**Purpose:** Reduce memory usage for heavy users running many Claude sessions.

**Problem:** Each Claude session spawns separate MCP processes. 30 sessions × 5 MCPs = 150 node processes.

**Solution:** Share MCP processes across sessions via Unix sockets.

### Platform Support

| Platform | Socket Pool | Detection |
|----------|-------------|-----------|
| macOS | ✅ Full support | `runtime.GOOS == "darwin"` |
| Linux | ✅ Full support | `/proc/version` (no Microsoft) |
| WSL2 | ✅ Full support | `microsoft-standard` in `/proc/version` |
| WSL1 | ❌ Auto-disabled | `Microsoft` (uppercase) in `/proc/version` |
| Windows | ❌ Not supported | `runtime.GOOS == "windows"` |

**Platform detection code:** `internal/platform/platform.go`

On unsupported platforms (WSL1, Windows), pooling is automatically disabled at runtime. MCPs work fine in stdio mode.

### How It Works
```
Startup (pool_all = true):
  1. platform.SupportsUnixSockets() check
  2. If unsupported: skip pool, use stdio mode
  3. If supported: config.toml [mcps.*] → Start socket proxies
                                        → Sockets at /tmp/agentdeck-mcp-{name}.sock

Session restart/MCP attach:
  WriteMCPJsonFromConfig() checks pool status
  If pooled: .mcp.json uses {"command": "nc", "args": ["-U", "/tmp/..."]}
  If not:    .mcp.json uses {"command": "npx", "args": [...]}
```

### Key Files
| File | Purpose |
|------|---------|
| `internal/platform/platform.go` | Platform detection (WSL1/WSL2/macOS/Linux) |
| `internal/mcppool/socket_proxy.go` | Unix socket proxy wrapping stdio MCP |
| `internal/mcppool/pool_simple.go` | Pool manager, ShouldPool() logic |
| `internal/session/pool_manager.go` | Global pool singleton, platform check, initialization |
| `internal/session/mcp_catalog.go` | WriteMCPJsonFromConfig() - socket vs stdio |
| `internal/session/userconfig.go` | `getMCPPoolConfigSection()` - platform-aware config |

### Request Routing
Socket proxy tracks JSON-RPC request IDs to route responses to correct clients:
```go
requestMap map[interface{}]string  // requestID → sessionID
```

### Indicators
- 🔌 in MCP Manager = MCP is pooled and running
- Socket files: `ls /tmp/agentdeck-mcp-*.sock`
- Pool logs: `~/.agent-deck/logs/mcppool/*.log`

---

## Status Detection System

### Architecture
```
UI (500ms tick) → Instance.UpdateStatus() → tmux.GetStatus()
                                          → CapturePane() → hasBusyIndicator() → normalizeContent()
                                          → Compare hash, check cooldown, check acknowledged
                                          → Return: "active" | "waiting" | "idle" | "inactive"
```

### GetStatus() Algorithm
1. Session doesn't exist? → "inactive"
2. Busy indicator? → "active" (catches "esc to interrupt", spinners ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏)
3. New session? → init, "idle"
4. Restored session? → set hash, respect saved acknowledged state
5. Hash changed? → "active"
6. Within 2s cooldown? → "active"
7. Cooldown expired → "idle" if acknowledged, else "waiting"

### StateTracker (per session)
```go
type StateTracker struct {
    lastHash       string    // SHA256 of normalized content
    lastChangeTime time.Time // For 2s cooldown
    acknowledged   bool      // User has "seen" this state
}
```

### Content Normalization
Strips for stable hashing: ANSI codes, control chars, spinners, time counters (`(45s · 1234 tokens)` → `(STATUS)`), trailing whitespace, multiple blank lines.

---

## Tool Detection (detector.go)

**Order:** Command string → Content parsing (regex) → Default "shell"

| Tool | Icon | Color |
|------|------|-------|
| claude | 🤖 | #ff9e64 |
| gemini | ✨ | #bb9af7 |
| opencode | 🌐 | #7dcfff |
| codex | 💻 | #7aa2f7 |
| cursor | 🖱️ | #bb9af7 |
| shell | 🐚 | default |

**30s cache** - use `ForceDetectTool()` to bypass

### Prompt Detection
- **Claude busy:** "esc to interrupt", spinners, "Thinking..."
- **Claude prompts:** "Yes, allow once", "No, and tell Claude...", box dialogs, ">" input
- **Others:** "(Y)es/(N)o", tool-specific prompts, shell prompts ($, #, %, ❯, ➜, >)

---

## Viewport Height Management

**Problem:** Help bar appeared/disappeared inconsistently due to scattered height calculations.

**Solution:** Centralized in `View()` with `ensureExactHeight()`:
```go
helpBarHeight := 2
filterBarHeight := 1
panelTitleLines := 2
contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - filterBarHeight
panelContentHeight := contentHeight - panelTitleLines
```

**Key:** `syncViewport()` MUST match `View()` calculations exactly.

---

## tmux Integration

**Session naming:** `agentdeck_{sanitized-title}_{unique-id}`

**PTY (pty.go):** `Ctrl+Q` detaches, SIGWINCH forwarded, `AcknowledgeWithSnapshot()` on detach

---

## Development

### Add Keyboard Shortcut
1. `home.go` → `Update()`, add `case "key":`
2. Update `renderHelpBar()`

### Add Dialog
1. Create `internal/ui/mydialog.go` with `Show()`, `Hide()`, `IsVisible()`, `Update()`, `View()`
2. Add to `Home` struct, init in `NewHome()`
3. Check `IsVisible()` in `Home.Update()` and `Home.View()`

### Testing
```bash
go test ./...                        # All
go test ./internal/session/... -v    # Session
go test ./internal/ui/... -v         # UI
go test ./internal/tmux/... -v       # tmux
```

---

## Documentation Updates

**CRITICAL:** When adding new features or commands, update ALL relevant documentation:

### Required Documentation Files

1. **README.md** - Main project documentation
   - Update CLI Commands section
   - Update usage examples
   - Add to FAQ if needed

2. **CLAUDE.md** (this file) - Project-specific instructions
   - Update CLI Commands tables
   - Update Project Structure section
   - Add implementation notes

3. **~/.claude-work/skills/agent-deck-cli/SKILL.md** - Agent-deck CLI skill
   - Update Quick Start section
   - Update all Common Workflows
   - Add new command to Core Operations
   - Update examples to use new features

4. **docs/plans/** - Implementation plans
   - Document the feature in a new plan file
   - Follow naming: `YYYY-MM-DD-feature-name.md`

### Documentation Checklist

When adding a new feature:
- [ ] Update README.md with command usage
- [ ] Update CLAUDE.md command tables and structure
- [ ] Update agent-deck-cli skill workflows
- [ ] Add implementation plan to docs/plans/
- [ ] Update CLI reference if exists
- [ ] Test all documentation examples work correctly

### Example: Adding `session current` Command

Files updated:
- ✓ README.md - Added to CLI Commands, Current session detection
- ✓ CLAUDE.md - Added to Session Commands table, Auto-Detect section, Project Structure
- ✓ ~/.claude-work/skills/agent-deck-cli/SKILL.md - Updated Quick Start, Workflow 2, Core Operations
- ✓ internal/profile/detect.go - NEW file for implementation
- ✓ cmd/agent-deck/session_cmd.go - Added handleSessionCurrent()

---

## Release

```bash
# 1. Update version in cmd/agent-deck/main.go
# 2. Commit and push
git commit -m "chore: bump version to vX.Y.Z" && git push origin main
# 3. Tag (triggers release)
git tag vX.Y.Z && git push origin vX.Y.Z
```

**CI:** `.github/workflows/ci.yml` (test + lint on push/PR)
**Release:** `.github/workflows/release.yml` (GoReleaser on tags)
**Platforms:** macOS/Linux amd64/arm64 | Windows via WSL

**Secrets:** `GITHUB_TOKEN` (auto), `HOMEBREW_TAP_GITHUB_TOKEN`

---

## Performance Optimizations

### Session Cache (2025-12-23)
**Problem:** 30 subprocesses/tick (15 `has-session` + 15 `display-message`)

**Solution:** Single `tmux list-sessions` call per tick, cached 2s:
```go
// tmux.go - RefreshSessionCache()
tmux list-sessions -F '#{session_name}\t#{session_activity}'
```

**Result:** 97% subprocess reduction, CPU 15% → 0.5% for idle sessions

### Already Optimized
| Component | Method |
|-----------|--------|
| Session existence/activity | Batched cache (`RefreshSessionCache()`) |
| Preview content | 2s TTL cache |
| Status updates | Background worker goroutine |
| View() builder | Reused 32KB buffer |
| Ghost sessions | 30s recheck interval |

---

## Colors (Tokyo Night)

| Color | Hex | Use |
|-------|-----|-----|
| Accent | #7aa2f7 | Selection |
| Green | #9ece6a | Running |
| Yellow | #e0af68 | Waiting |
| Red | #f7768e | Error |
| Cyan | #7dcfff | Groups |
| Purple | #bb9af7 | Tool tags |
| Background | #1a1b26 | Dark bg |
| Surface | #24283b | Panels |

---

## Debugging

```bash
AGENTDECK_DEBUG=1 agent-deck  # Logs status transitions to stderr
```

### Common Issues
| Symptom | Cause | Fix |
|---------|-------|-----|
| Yellow when should be green | Busy indicator not detected | Check `hasBusyIndicator()` |
| Yellow flash on startup | Init returning "waiting" | Should return "idle" |
| Status not persisting | `acknowledged` not saved | Check `ReconnectSessionWithStatus()` |
| Flickering green/yellow | Cooldown too short | Increase `activityCooldown` |
| Help bar appears/disappears | Height mismatch | `syncViewport()` must match `View()` |
| High CPU with many sessions | Subprocess not batched | Check `RefreshSessionCache()` |
| High CPU with active session | Expected (live preview) | Not a bug |
