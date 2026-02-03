# Changelog

All notable changes to Agent Deck will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.10.4] - 2026-02-03

### Added

- **Prevent nested agent-deck sessions (#127)**: Running `agent-deck` inside a managed tmux session now shows a clear error instead of causing infinite `...` output. Read-only commands (`version`, `help`, `status`, `list`, `session current/show/output`, `mcp list/attached`) still work for debugging

## [0.10.3] - 2026-02-03

### Fixed

- **Global search unusable with large datasets (#125)**: Multiple performance fixes make global search work with multi-GB session data:
  - Remove rate limiter from initial load (was causing 42+ minute "Loading..." on large datasets)
  - Read only first 32KB of files for metadata in balanced tier (was reading entire files, some 800MB+)
  - Early exit from parsing once metadata found (SessionID/CWD/Summary)
  - Parallelize disk search with 8-worker pool (was sequential)
  - Debounced async search on UI thread (250ms debounce + background goroutine)
  - Default `recent_days` to 30 when not set (was 0 = all time)
- **G key didn't open Global Search**: Help bar showed `G Global` but the key actually jumped to the bottom of the list. `G` now opens Global Search (falls back to local search if global search is disabled)

## [0.10.2] - 2026-02-03

### Fixed

- **Global search freezes when typing with many sessions (#125)**: Search ran synchronously on the UI thread, blocking all input while scanning files from disk. Now uses debounced async search (250ms debounce + background goroutine) so the UI stays responsive regardless of data size
- **G key didn't open Global Search**: Help bar showed `G Global` but the key actually jumped to the bottom of the list. `G` now opens Global Search (falls back to local search if global search is disabled)

## [0.10.1] - 2026-02-02

### Fixed

- **GREEN status not detecting Claude 2.1.25+ spinners**: Prompt detector only checked braille spinner chars (`⠋⠙⠹...`) as busy guards, missing the asterisk spinners (`✳✽✶✢`) used since Claude 2.1.25. This caused sessions to show YELLOW instead of GREEN while Claude was actively working
- **Prompt detector missing whimsical word timing patterns**: Only "thinking" and "connecting" were recognized as active processing. Now detects all 90+ whimsical words (e.g., "Hullaballooing", "Clauding") via the universal `…` + `tokens` pattern
- **Spinner check range too narrow**: Only checked last 3 lines for spinner chars, but Claude's UI can push the spinner line 6+ lines from the bottom (tip lines, borders, status bar). Expanded to last 10 lines
- **Acknowledge override on attach**: Attaching to a waiting (yellow) session would briefly acknowledge it, but the background poller immediately reset it back to waiting because the prompt was still visible. Prompt detection now respects the acknowledged state

## [0.10.0] - 2026-02-02

### Changed

- **Group dialog defaults to root mode on grouped sessions**: Pressing `g` while the cursor is on a session inside a group now opens the "Create New Group" dialog in root mode instead of subgroup mode. Tab toggle still switches to subgroup. Group headers still default to subgroup mode. This makes it easier for users with all sessions in groups to create new root-level groups

### Added

- **MCP socket pool resilience docs**: README updated to mention automatic ~3s crash recovery via reconnecting proxy
- **Pattern override documentation**: `config.toml init` now includes documentation for `busy_patterns_extra`, `prompt_patterns_extra`, and `spinner_chars_extra` fields for extending built-in tool detection patterns

## [0.9.2] - 2026-01-31

### Fixed

- **492% CPU usage**: Main TUI process was consuming 5 CPU cores due to reading 100-841MB JSONL files every 2 seconds per Claude session. Now uses tail-read (last 32KB only) with file-size caching to skip unchanged files entirely
- **Duplicate notification sync**: Both foreground TUI tick and background worker were running identical notification sync every 2 seconds, spawning duplicate tmux subprocesses. Removed foreground sync since background worker handles everything
- **Excessive tmux subprocess spawns**: `GetEnvironment()` spawned `tmux show-environment` every 2 seconds per Claude session for session ID lookup. Added 30-second cache since session IDs rarely change
- **Unnecessary idle session polling**: Claude/Gemini/Codex session tracking updates now skip idle sessions where nothing changes

### Added

- Configurable pattern detection system: `ResolvedPatterns` with compiled regexes replaces hardcoded busy/prompt detection, enabling pattern overrides via `config.toml`

## [0.9.1] - 2026-01-31

### Fixed

- **MCP socket proxy 64KB crash**: `bufio.Scanner` default 64KB limit caused socket proxy to crash when MCPs like context7 or firecrawl returned large responses. Increased buffer to 10MB, preventing orphaned MCP processes and permanent "failed" status
- **Faster MCP failure recovery**: Health monitor interval reduced from 10s to 3s for quicker detection and restart of failed proxies
- **Active client disconnect on proxy failure**: When socket proxy dies, all connected clients are now actively closed so reconnecting proxies detect failure immediately instead of hanging

### Added

- **Reconnecting MCP proxy** (`agent-deck mcp-proxy`): New subcommand replaces `nc -U` as the stdio bridge to MCP sockets. Automatically reconnects with exponential backoff when sockets drop, making MCP pool restarts invisible to Claude sessions (~3s recovery)

## [0.9.0] - 2026-01-31

### Added

- **Fork worktree isolation**: Fork dialog (`F` key) now includes an opt-in worktree toggle for git repos. When enabled, the forked session gets its own git worktree directory, isolating Claude Code project state (plan, memory, attachments) between parent and fork (#123)
- Auto-suggested branch name (`fork/<session-name>`) in fork dialog when worktree is enabled
- CLI `session fork` command gains `-w/--worktree <branch>` and `-b/--new-branch` flags for worktree-based forks
- Branch validation in fork dialog using existing git helpers

## [0.8.99] - 2026-01-31

### Fixed

- **Session reorder persistence**: Reordering sessions with Shift+K/J now persists across reloads. Added `Order` field to session instances, normalized on every move, and sorted by Order on load. Legacy sessions (no Order field) preserve their original order via stable sort (#119)

## [0.8.98] - 2026-01-30

### Fixed

- **Claude Code 2.1.25+ busy detection**: Claude Code 2.1.25 removed `"ctrl+c to interrupt"` from the status line, causing all sessions to appear YELLOW/GRAY instead of GREEN while working. Detection now uses the unicode ellipsis (`…`) pattern: active state shows `"✳ Gusting… (35s · ↑ 673 tokens)"`, done state shows `"✻ Worked for 54s"` (no ellipsis)
- Status line token format detection updated to match new `↑`/`↓` arrow format (`(35s · ↑ 673 tokens)`)
- Content normalization updated for asterisk spinner characters (`·✳✽✶✻✢`) to prevent false hash changes

### Changed

- Analytics preview panel now defaults to OFF (opt-in via `show_analytics = true` in config.toml)

### Added

- 6 new whimsical thinking words: `billowing`, `gusting`, `metamorphosing`, `sublimating`, `recombobulating`, `sautéing`
- Word-list-independent spinner detection regex for future-proofing against new Claude Code words

## [0.8.97] - 2026-01-29

### Fixed

- **CLI session ID capture**: `session start`, `session restart`, `session fork`, and `try` now persist Claude session IDs to JSON immediately, enabling fork and resume from CLI-only workflows without the TUI
- Fork pre-check recovery: `session fork` attempts to recover missing session IDs from tmux before failing, fixing sessions started before this fix
- Stale comment in `loadSessionData` corrected to reflect lazy loading behavior

### Added

- `PostStartSync()` method on Instance for synchronous session ID capture after Start/Restart (CLI-only; TUI uses its existing background worker)

## [0.8.96] - 2026-01-28

### Added

- **HTTP Transport Support for MCP Servers**: Native support for HTTP/SSE MCP servers with auto-start capability
- Add `[mcps.X.server]` config block for auto-starting HTTP MCP servers (command, args, env, startup_timeout, health_check)
- Add `mcp server` CLI commands: `start`, `stop`, `status` for managing HTTP MCP servers
- Add transport type indicators in `mcp list`: `[S]`=stdio, `[H]`=http, `[E]`=sse
- Add TUI MCP dialog transport indicators with status: `●`=running, `○`=external, `✗`=stopped
- Add HTTP server pool with health monitoring and automatic restart of failed servers
- External server detection: if URL is already reachable, use it without spawning a new process

### Changed

- MCP dialog now shows transport type and server status for each MCP
- `mcp list` output now includes transport type column

## [0.8.95] - 2026-01-28

### Changed

- **Performance: TUI startup ~3x faster** (6s → 2s for 44 sessions)
- Batch tmux operations: ConfigureStatusBar (5→1 call), EnableMouseMode (6→2 calls) using command chaining
- Lazy loading: defer non-essential tmux configuration until first attach or background tick
- Skip UpdateStatus and session ID sync at load time (use cached status from JSON)

### Added

- Add `ReconnectSessionLazy()` for deferred session configuration
- Add `EnsureConfigured()` method for on-demand tmux setup
- Add `SyncSessionIDsToTmux()` method for on-demand session ID sync
- Background worker gradually configures unconfigured sessions (one per 2s tick)

## [0.8.94] - 2026-01-28

### Added

- Add undo delete (Ctrl+Z) for sessions: press Ctrl+Z after deleting a session to restore it including AI conversation resume. Supports multiple undos in reverse order (stack of up to 10)
- Show ^Z Undo hint in help bar (compact and full modes) when undo stack is non-empty
- Add Ctrl+Z entry to help overlay (? screen)

### Changed

- Update delete confirmation dialog: "This cannot be undone" → "Press Ctrl+Z after deletion to undo"

## [0.8.93] - 2026-01-28

### Fixed

- Fix `g` key unable to create root-level groups when any group exists (#111). Add Tab toggle in the create-group dialog to switch between Root and Subgroup modes
- Fix `n` key handler using display name constant instead of path constant for default group

### Added

- Group DefaultPath tracking: groups now track the most recently accessed session's project path via `updateGroupDefaultPath`

## [0.8.92] - 2026-01-28

### Fixed

- Fix CI test failure in `TestBindUnbindKey` by making default key restore best-effort in `UnbindKey`

## [0.8.91] - 2026-01-28

### Fixed

- Fix TUI cursor not following notification bar session switch after detach (Ctrl+b N during attach now moves cursor to the switched-to session on Ctrl+Q)

## [0.8.90] - 2026-01-28

### Fixed

- Fix quit dialog ("Keep running" / "Shut down") hidden behind splash screen, causing infinite hang on quit with MCP pool
- Fix `isQuitting` flag not reset when canceling quit dialog with Esc
- Add 5s safety timeouts to status worker and log worker waits during shutdown

## [0.8.89] - 2026-01-28

### Fixed

- Fix shutdown hang when quitting with "shut down" MCP pool option (process `Wait()` blocked forever on child-held pipes)
- Set `cmd.Cancel` (SIGTERM) and `cmd.WaitDelay` (3s) on MCP processes for graceful shutdown with escalation
- Add 5s safety timeout to individual proxy `Stop()` and 10s overall timeout to pool `Shutdown()`

## [0.8.88] - 2026-01-28

### Fixed

- Fix stale expanded group state during reload causing cursor jumps when CLI adds a session while TUI is running
- Fix new groups added via CLI appearing collapsed instead of expanded
- Eliminate redundant tree rebuild and viewport sync during reload (performance)

## [0.8.87] - 2026-01-28

### Added

- Add `env` field to custom tool definitions for inline environment variables (closes #101)
- Custom tools from config.toml now appear in the TUI command picker with icons
- CLI `agent-deck add -c <custom-tool>` resolves tool to actual command automatically

### Fixed

- Fix `[worktree] default_location = "subdirectory"` config not being applied (fixes #110)
- Add `--location` CLI flag to override worktree placement per session (`sibling` or `subdirectory`)
- Worktree location now respects config in both CLI and TUI new session dialog

## [0.8.86] - 2026-01-28

### Fixed

- Fix changelog display dropping unrecognized lines (plain text paragraphs now preserved)
- Fix trailing-slash path completion returning directory name instead of listing contents
- Reset path autocomplete state when reopening new session dialog
- Fix double-close on LogWatcher and StorageWatcher (move watcher.Close inside sync.Once)
- Fix log worker shutdown race (replace unused channel with sync.WaitGroup)
- Fix CapturePane TOCTOU race with singleflight deduplication

### Added

- Comprehensive test suite for update package (CompareVersions, ParseChangelog, GetChangesBetweenVersions, FormatChangelogForDisplay)

## [0.8.85] - 2026-01-27

### Fixed

- Clear MCP cache before regeneration to prevent stale reads
- Cursor jump during navigation and view duplication bugs

## [0.8.83] - 2026-01-26

### Fixed

- Resume with empty session ID opens picker instead of random UUID
- Subgroup creation under selected group

### Added

- Fast text copy (`c`) and inter-session transfer (`x`)

## [0.8.79] - 2026-01-26

### Added

- Gemini model selection dialog (`Ctrl+G`)
- Configurable maintenance system with TUI feedback
- Improved status detection accuracy and Gemini prompt caching
- `.env` file sourcing support for sessions (`[shell] env_files`)
- Default dangerous mode for power users

### Fixed

- Sync session IDs to tmux env for cross-project search
- Write headers to Claude config for HTTP MCPs
- OpenCode session detection persistence and "Detecting session..." bug
- Preserve parent path when renaming subgroups

## [0.8.69] - 2026-01-20

### Added

- MCP Manager user scope: attach MCPs to `~/.claude.json` (affects all sessions)
- Three-scope MCP system: LOCAL, GLOBAL, USER
- Session sharing skill (export/import sessions between developers)
- Scrolling support for help overlay on small screens

### Fixed

- Prevent orphaned test sessions
- MCP pool quit confirmation

## [0.8.67] - 2026-01-20

### Added

- Notification bar enabled by default
- Thread-safe key bindings for background sync
- Background worker self-ticking for status updates during `tea.Exec`
- `ctrl+c to interrupt` as primary busy indicator detection
- Debug logging for status transitions

### Changed

- Reduced grace period from 5s to 1.5s for faster startup detection
- Removed 6-second animation minimum; uses status-based detection
- Hook-based polling replaces frequent tick-based detection

## [0.8.65] - 2026-01-19

### Improved

- Notification bar performance and active session detection
- Increased busy indicator check depth from 10 to 20 lines

## [0.6.1] - 2025-12-24

### Changed

- **Replaced Aider with OpenCode** - Full integration of OpenCode (open-source AI coding agent)
  - OpenCode replaces Aider as the default alternative to Claude Code
  - New icon: 🌐 representing OpenCode's open and universal approach
  - Detection patterns for OpenCode's TUI (input box, mode indicators, logo)
  - Updated all documentation, examples, and tests

## [0.1.0] - 2025-12-03

### Added

- **Terminal UI** - Full-featured TUI built with Bubble Tea
  - Session list with hierarchical group organization
  - Live preview pane showing terminal output
  - Fuzzy search with `/` key
  - Keyboard-driven navigation (vim-style `hjkl`)

- **Session Management**
  - Create, rename, delete sessions
  - Attach/detach with `Ctrl+Q`
  - Import existing tmux sessions
  - Reorder sessions within groups

- **Group Organization**
  - Hierarchical folder structure
  - Create nested groups
  - Move sessions between groups
  - Collapsible groups with persistence

- **Intelligent Status Detection**
  - 3-state model: Running (green), Waiting (yellow), Idle (gray)
  - Tool-specific busy indicator detection
  - Prompt detection for Claude Code, Gemini CLI, OpenCode, Codex
  - Content hashing with 2-second activity cooldown
  - Status persistence across restarts

- **CLI Commands**
  - `agent-deck` - Launch TUI
  - `agent-deck add <path>` - Add session from CLI
  - `agent-deck list` - List sessions (table or JSON)
  - `agent-deck remove <id|title>` - Remove session

- **Tool Support**
  - Claude Code - Full status detection
  - Gemini CLI - Activity and prompt detection
  - OpenCode - TUI element detection
  - Codex - Prompt detection
  - Generic shell support

- **tmux Integration**
  - Automatic session creation with unique names
  - Mouse mode enabled by default
  - 50,000 line scrollback buffer
  - PTY attachment with `Ctrl+Q` detach

### Technical

- Built with Go 1.24+
- Bubble Tea TUI framework
- Lip Gloss styling
- Tokyo Night color theme
- Atomic JSON persistence
- Cross-platform: macOS, Linux

[0.1.0]: https://github.com/asheshgoplani/agent-deck/releases/tag/v0.1.0
