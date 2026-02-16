# Changelog

All notable changes to Agent Deck will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.18.0] - 2026-02-17

### Added

- Add Codex notify hook integration for instant session status updates
- Add notification show_all mode to display all notifications at once
- Add automatic bridge.py updates when running `agent-deck update` (#178)

### Fixed

- Fix: handle error returns in test cleanup functions
- Fix: bridge.py not updating with agent-deck binary updates (#178)

## [0.17.0] - 2026-02-16

### Added

- Add top-level rename command with validation (#176, contributed by @nlenepveu)
- Add Slack user ID authorization for conductors (#170, contributed by @mtparet)
- Custom CLAUDE.md paths via symlinks for conductors (#173, contributed by @mtparet)

### Fixed

- Fix: remove thread context fetching from Slack handler (#175, contributed by @mtparet)
- Fix: prevent worktree nesting when creating from within worktrees (#177)

## [0.16.0] - 2026-02-14

### Added

- Add `--teammate-mode` tmux option to Claude session launcher for shared terminal pairing (#168, contributed by @jonnocraig)
- Add Slack integration and cross-platform daemon support (#169, contributed by @mtparet)
- Add Claude Code lifecycle hooks for real-time status detection (instant green/yellow/gray transitions without tmux polling)
- Add first-launch prompt asking users to install hooks (preserves existing Claude settings.json)
- Add `agent-deck hooks install/uninstall/status` CLI subcommands for manual hook management
- Add `hooks_enabled` config option under `[claude]` to opt out of hook-based detection
- Add StatusFileWatcher (fsnotify) for instant hook status file processing
- Add `AGENTDECK_INSTANCE_ID` env var export for Claude hook subprocess identification
- Add acknowledgment awareness to hook fast path (attach turns session gray, `u` key turns it orange)
- Add `llms.txt` for LLM discoverability, fix schema version, add FAQ entries (#167)

### Fixed

- Fix middot `·` spinner character not detected as busy indicator when followed by ellipsis (BusyPatterns regex now includes `·`)

### Changed

- Sessions with active hooks skip tmux content polling entirely (2-minute timeout as crash safety net only)
- Existing sessions without hooks continue using polling (seamless hybrid mode)

## [0.15.0] - 2026-02-13

### Added

- Add `inject_status_line` config option under `[tmux]` to disable tmux statusline injection, allowing users to keep their own tmux status bar (#157)
- Add system theme option: sync TUI theme with OS dark/light mode (#162)
- Improve quick session creation: inherit path, tool, and options from hovered session (#165)

### Fixed

- Fix Claude session ID not updating after `/clear`, `/fork`, or `/compact` by syncing from disk (#166)
- Restore delay between paste and Enter in `SendKeysAndEnter` to prevent swallowed input in tmux (#168)

## [0.14.0] - 2026-02-12

### Added

- Add title-based status detection fast-path: reads tmux pane titles (Braille spinner / done markers) to determine Claude session state without expensive content scanning
- Add `RefreshPaneInfoCache()` for zero-subprocess pane title fetching via PipeManager
- Add worktree finish dialog (`W` key): merge branch, remove worktree, delete branch, and clean up session in one step
- Add worktree branch badge `[branch]` in session list for worktree sessions
- Add worktree info section in preview pane (branch, repo, path, dirty status)
- Add worktree dirty status cache with lazy 10s TTL checks
- Add repository worktree summary in group preview when sessions share a repo
- Add `esc to interrupt` fallback to Claude busy patterns for older Claude Code versions
- Add worktree section to help overlay

### Fixed

- Fix busy indicator false negatives for `·` and `✻` spinner chars with ellipsis (BusyRegexp now correctly catches all spinner frames with active context)
- Remove unused `matchesDetectPatterns` function (lint warning)
- Fix `starting` and `inactive` status mapping in instance status update

## [0.13.0] - 2026-02-11

### Added

- Add quick session creation with `Shift+N` hotkey: instant session with auto-generated name and smart defaults (#161)
- Add Docker-style name generator (adjective-noun) with ~10,000 unique combinations
- Add `--quick` / `-Q` flag to `agent-deck add` CLI for auto-named sessions
- Smart defaults: inherits tool, options, and path from most recent session in the group

## [0.12.3] - 2026-02-11

### Fixed

- Fix busy detection window reduced from 25 to 10 lines for faster status transitions
- Fix conductor group permanently pinned to top of group list
- Optimize status detection pipeline for faster green/yellow transitions
- Add spinner movement detection tests for stuck spinner validation

## [0.12.2] - 2026-02-10

### Fixed

- Fix `session send` intermittently dropping Enter key (and sometimes text) due to tmux race condition between two separate `send-keys` process invocations (tmux#1185, tmux#1517, tmux#1778)
- Fix all 6 send-keys + Enter code paths to use atomic tmux command chaining (`;`) in a single subprocess
- Add retry with verification to CLI `session send` for resilience under heavy load or SSH latency

## [0.12.1] - 2026-02-10

### Fixed

- Fix Shift+R restart race condition with animation guard on restart and fork hotkeys (#147)
- Fix settings menu viewport cropping in small terminals with scroll windowing (#149)
- Fix .mcp.json clobber by preserving existing entries when managing MCP sessions (#146)
- Fix --resume-session arg parsing by registering it in the arg reorder map (#145)

### Added

- Add tmux option overrides via `[tmux]` config section in config.toml (#150)
- Add opencode fork infrastructure with OpenCodeOptions for model/agent/fork support (#148)

## [0.12.0] - 2026-02-10

### Added

- Multiple conductors per profile: create N named conductors in a single profile
  - `agent-deck conductor setup <name>` with `--heartbeat`, `--no-heartbeat`, `--description` flags
  - `agent-deck conductor teardown <name>` or `--all` to remove conductors
  - `agent-deck conductor list` with `--json` and `--profile` filters
  - `agent-deck conductor status [name]` shows all or specific conductor health
- Two-tier CLAUDE.md for conductors: shared knowledge base + per-conductor identity
  - Shared `CLAUDE.md` at conductor root with CLI reference, protocols, and rules
  - Per-conductor `CLAUDE.md` with name and profile substitution
- Conductor metadata via `meta.json` files for name, profile, heartbeat settings, and description
- Auto-migration of legacy single-conductor directories to new multi-conductor format
- Bridge (Telegram) updated for dynamic conductor discovery via `meta.json` scanning
- `normalizeArgs` utility for consistent flag parsing across all CLI commands
- Status field added to `agent-deck list --json` output

## [0.11.4] - 2026-02-09

### Added

- Add `allow_dangerous_mode` option to `[claude]` config section
  - Passes `--allow-dangerously-skip-permissions` to Claude (opt-in bypass mode)
  - `dangerous_mode = true` takes precedence when both are set
  - Based on contribution by @daniel-shimon (#152), with architectural fixes (#153)
- New permission flag persists per-session across fork and restart operations

## [0.11.3] - 2026-02-09

### Fixed

- Fix deleted sessions reappearing after reload or app restart
  - `SaveInstances()` now deletes stale rows from SQLite within the same transaction
  - Added explicit `DeleteInstance()` call in the delete handler as a safeguard
  - Root cause: `INSERT OR REPLACE` never removed deleted session rows from the database
- Update profile detection to check for `state.db` (SQLite) in addition to legacy `sessions.json`
- Update uninstall script to count sessions from SQLite instead of JSON

### Added

- Persist UI state (cursor position, preview mode, status filter) across restarts via SQLite metadata
- Save group expanded/collapsed state immediately on toggle
- Discord badge and link in README

### Changed

- Simplify multi-instance coordination: remove periodic primary re-election from background worker
- Create new profiles with SQLite directly instead of empty `sessions.json`
- Update troubleshooting docs for SQLite-based recovery

## [0.11.2] - 2026-02-06

### Fixed

- Enable notification bar on all instances, not just the primary
  - Previously secondary instances had notifications disabled entirely
  - All instances share the same SQLite state, so they produce identical bar content

## [0.11.1] - 2026-02-06

### Changed

- Replace file-based lock with SQLite heartbeat-based primary election for multi-instance coordination
  - Dynamic failover: if the primary instance crashes, a secondary takes over the notification bar within ~12 seconds
  - Eliminates stale `.lock` files that required manual cleanup after crashes
  - `ElectPrimary()` uses atomic SQLite transactions to prevent split-brain

### Removed

- Remove `acquireLock`, `releaseLock`, `getLockFilePath`, `isProcessRunning` (replaced by SQLite election)

## [0.11.0] - 2026-02-06

### Changed

- Replace `sessions.json` with SQLite (`state.db`) as the single source of truth
  - WAL mode for concurrent multi-instance reads/writes without corruption
  - Auto-migrates existing `sessions.json` on first run (renamed to `.migrated` as backup)
  - Removes fragile full-file JSON rewrites, backup rotation, and fsnotify dependency
  - Tool-specific data stored as JSON blob in `tool_data` column for schema flexibility
- Replace fsnotify-based storage watcher with SQLite metadata polling
  - Simpler, works reliably on all filesystems (9p, NFS, WSL)
  - 2-second poll interval using `metadata.last_modified` timestamp
- Replace tmux rate limiter and watcher with control mode pipes (PipeManager)
  - Event-driven status detection via `tmux -C` control mode
  - Zero-subprocess architecture: no more `tmux capture-pane` for idle sessions

### Added

- Add `internal/statedb` package: SQLite wrapper with CRUD, heartbeat, status sync, and change detection
- Add cross-instance acknowledgment sync via SQLite (ack in instance A visible in instance B)
- Add instance heartbeat table for tracking alive TUI processes
- Add `StatusSettings` in user config (reserved for future status detection settings)

## [0.10.20] - 2026-02-06

### Added

- Add `worktree finish` command to merge branch, remove worktree, and delete session in one step (#140)
  - Flags: `--into`, `--no-merge`, `--keep-branch`, `--force`, `--json`
  - Abort-safe: merge conflicts trigger `git merge --abort`, leaving everything intact
- Auto-cleanup worktree directories when deleting worktree sessions (CLI `remove` and TUI `d` key)

### Fixed

- Fix orphaned MCP server processes (Playwright CPU leak) by killing entire process group
  - Set `Setpgid=true` so grandchild processes (npx/uvx spawned) share a process group
  - Shutdown now sends SIGTERM/SIGKILL to `-pid` (group) instead of just the parent
- Fix test cleanup killing user sessions with "test" in their title
- Fix session rename lost during reload race condition

## [0.10.19] - 2026-02-05

### Fixed

- Fix session rename not persisting (#141)
  - `lastLoadMtime` was not updated after saves, causing mtime check to incorrectly abort subsequent saves
  - Renames, reorders, and other non-force saves now persist correctly

## [0.10.18] - 2026-02-05

### Added

- Add Codex CLI `--yolo` flag support (#142)
  - Global config: `[codex] yolo_mode = true` in config.toml
  - Per-session override in New Session dialog (checkbox)
  - Flag preserved across session restarts
  - Settings panel toggle for global default
- Add unified `OptionsPanel` interface for tool-specific options (#143)
  - New tools can add options by implementing interface + 1 case in `updateToolOptions()`
  - Shared `renderCheckboxLine()` helper ensures visual consistency across panels

### Fixed

- Fix `ClaudeOptionsPanel.Blur()` not resetting focus state
  - `IsFocused()` now correctly returns false after blur

## [0.10.17] - 2026-02-05

### Fixed

- Fix sessions disappearing after creation in TUI
  - Critical saves (create, fork, delete, restore) now bypass mtime check that was incorrectly aborting saves
  - Sessions created during reload are now properly persisted to JSON before triggering reload
- Fix import function to recover orphaned agent-deck sessions
  - Press `i` to import sessions that exist in tmux but are missing from sessions.json
  - Recovered sessions are placed in a "Recovered" group for easy identification

## [0.10.16] - 2026-02-05

### Fixed

- Fix garbled input at update confirmation prompt
  - Add `drainStdin()` to flush terminal input buffer before prompting
  - Use `TCFLSH` ioctl to discard pending escape sequences and accidental keypresses
  - Switch from `fmt.Scanln` to `bufio.NewReader` for more robust input handling

## [0.10.15] - 2026-02-05

### Fixed

- Fix TUI overwriting CLI changes to sessions.json (#139)
  - Add mtime check before save: compares file mtime against when we last loaded, aborts save and triggers reload if external changes detected
  - Fix TOCTOU race condition: `isReloading` flag now protected by mutex in all 6 read locations
  - Add filesystem detection for WSL2/NFS: warns users when on 9p/NFS/CIFS/SSHFS mounts where fsnotify is unreliable

## [0.10.14] - 2026-02-04

### Fixed

- Fix critical OOM crash: Global Search was loading 4.4 GB of JSONL content into memory and opening 884 fsnotify directory watchers (7,900+ file descriptors), causing agent-deck to balloon to 6+ GB RSS until macOS killed it
  - Temporarily disable Global Search at startup until memory-safe implementation is complete
  - Optimize directory traversal to skip `tool-results/` and `subagents/` subdirectories (never contain JSONL files)
  - Limit fsnotify watchers to project-level directories only (was recursively watching ALL subdirectories)
- Add max client cap (100) per MCP socket proxy to prevent unbounded goroutine growth from reconnect loops
  - Broken MCPs (e.g., `reddit-yilin` with 72 connects/30s) could spawn unlimited goroutines and scanner buffers

### Changed

- Global Search (`G` key) is temporarily disabled pending a memory-safe reimplementation
  - Will be re-enabled once balanced tier is enforced for large datasets and memory limits are properly applied

## [0.10.13] - 2026-02-04

### Added

- Migrate all logging to structured JSONL via `log/slog` with automatic rotation
  - JSONL output to `~/.agent-deck/debug.log` with component-based filtering (`jq 'select(.component=="pool")'`)
  - Automatic log rotation via lumberjack (configurable size, backups, retention in `[logs]` config)
  - Event aggregation for high-frequency MCP socket events (1 summary per 30s instead of 40 lines/sec)
  - In-memory ring buffer with crash dump support (`kill -USR1 <pid>`)
  - Optional pprof profiling on `localhost:6060`
  - 9 log components: status, mcp, notif, perf, ui, session, storage, pool, http
  - New `[logs]` config options: `debug_level`, `debug_format`, `debug_max_mb`, `debug_backups`, `debug_retention_days`, `debug_compress`, `ring_buffer_mb`, `pprof_enabled`, `aggregate_interval_secs`

### Fixed

- Fix MCP pool infinite restart loop causing 45 GB memory leak over 15 hours
  - Add `StatusPermanentlyFailed` status: broken MCPs are disabled after 10 consecutive failures
  - Fix leaked proxy context/goroutines when `Start()` fails during restart
  - Reset failure counters after proxy is healthy for 5+ minutes (allows transient failure recovery)
  - Skip permanently failed proxies in health monitor for both socket and HTTP pools
- Fix inconsistent debug flag check in tmux.go (`== "1"` changed to `!= ""` to match rest of codebase)

## [0.10.12] - 2026-02-04

### Fixed

- Fix tmux pane showing stale conversation history after session restart (#138)
  - Clear scrollback buffer before respawn to remove old content
  - Invalidate preview cache on restart for immediate refresh
  - Kill old tmux session in fallback restart path to prevent orphans

## [0.10.11] - 2026-02-04

### Added

- Add `mcp_default_scope` config option to control where MCPs are written (#137)
  - Set to `"global"` or `"user"` to stop agent-deck from overwriting `.mcp.json` on restart
  - Affects MCP Manager default tab, CLI attach/detach defaults, and session restart regeneration
  - Defaults to `"local"` (no breaking change)

## [0.10.10] - 2026-02-04

### Added

- Add configurable worktree path templates via `path_template` config option (#135, contributed by @peteski22)
  - Template variables: `{repo-name}`, `{repo-root}`, `{branch}`, `{session-id}`
  - Overrides `default_location` when set; falls back to existing behavior when unset
  - Integrated at all 4 worktree creation points (CLI add, CLI fork, TUI new session, TUI fork)
  - Backported from [njbrake/agent-of-empires](https://github.com/njbrake/agent-of-empires)

## [0.10.9] - 2026-02-03

### Removed

- Remove dead GoReleaser ldflags targeting non-existent `main.version/commit/date` vars
- Remove redundant `make release` target (superseded by `release-local`)
- Remove unused deprecated wrappers `NewStorage()` and `GetStoragePath()`
- Remove unused test helpers file (`internal/ui/test_helpers.go`)
- Remove stale `home.go.bak` backup file

## [0.10.8] - 2026-02-03

### Fixed

- Fix shell dying after tool exit by removing `exec` prefix from all tool commands (#133, contributed by @kurochenko)
  - When Claude, Gemini, OpenCode, Codex, or generic tools exit, users now return to their shell prompt instead of a dead tmux pane
  - Enables workflows where tools run inside wrappers (e.g., nvim) that should survive tool exit

## [0.10.7] - 2026-02-03

### Added

- Add `make release-local` target for local GoReleaser releases (no GitHub Actions dependency)

## [0.10.6] - 2026-02-03

### Fixed

- **TUI freezes with 40+ sessions**: Parallel status polling replaces sequential loop that couldn't complete within 2s tick
  - 10-worker pool via errgroup for concurrent tmux status checks
  - Instance-level RWMutex prevents data races between background worker and TUI rendering
  - Tiered polling skips idle sessions with no activity (10s recheck gate)
  - 3-second timeout on CapturePane/GetWindowActivity prevents hung tmux calls from blocking workers
  - Timeout preserves previous status instead of flashing RED
  - Race detector (`-race`) enabled in tests and CI

## [0.10.5] - 2026-02-03

### Fixed

- **Fix intermittent `zsh: killed` due to memory exhaustion (#128)**: Four memory leaks causing macOS OOM killer (Jetsam) to SIGKILL agent-deck after prolonged use with many sessions:
  - Cap global search content buffer memory at 100MB (configurable via `memory_limit_mb`), evict oldest 25% of entries when exceeded
  - Release all content memory and clear file trackers on index Close()
  - Stop debounce timers on watcher shutdown to prevent goroutine leaks
  - Prune stale analytics/activity caches every 20 seconds (were never cleaned up)
  - Clean up analytics caches on session delete
  - Clear orphaned MCP socket proxy request map entries on client disconnect and MCP failure
  - Prune LogWatcher rate limiters for removed sessions every 20 seconds

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
