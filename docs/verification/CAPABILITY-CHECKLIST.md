# agent-deck Capability Verification Checklist

Derived from `01-CAPABILITY-SPEC.md` — 229 capabilities, 403 capability×surface rows.

**Status legend:** `verified` · `broken` · `partial` · `untestable-locally` · `deferred` · `pending`.

**Totals:** broken=1 · partial=43 · pending=115 · untestable-locally=29 · verified=215

## CAP-SESS (13 caps, 32 rows) — partial:2, pending:11, untestable-locally:1, verified:18

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-SESS-001 | CLI | Session data model (Instance) and identity generation | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-001-CLI.txt | ad add/list --json shows Instance identity model. ID format matches randomString(8hex)-unixseconds (e.g. 0d8070e1-178119 |
| CAP-SESS-001 | TUI | Session data model (Instance) and identity generation | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-TUI-home.txt | TUI session list renders title+tool+group tree. New Session modal shows full tool enum: shell/claude/gemini/opencode/cod |
| CAP-SESS-001 | WEB | Session data model (Instance) and identity generation | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-WEB.txt | /api/sessions exposes id,title,tool,status,groupPath,projectPath,tmuxSession,tmuxSocketName,createdAt,command,noTransiti |
| CAP-SESS-002 | CLI | Lifecycle states and transitions | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-002-CLI.txt | Lifecycle states observed: started shell session->idle; never-started sessions->error (tmuxSession.Exists()==false per c |
| CAP-SESS-002 | TUI | Lifecycle states and transitions | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-TUI-home.txt | TUI status icons rendered per-session and in header legend: running(filled), waiting, idle(circle), stopped(square), err |
| CAP-SESS-002 | WEB | Lifecycle states and transitions | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-WEB.txt | /api/sessions reports per-session status strings (error/idle/stopped) consistent with CLI/TUI. |
| CAP-SESS-003 | CLI | Status derivation pipeline (UpdateStatus) | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-003-CLI.txt | ad status -v / --json drives the CLI cold-load poll (RefreshInstancesForCLIStatus): statuses are derived live from tmux  |
| CAP-SESS-003 | TUI | Status derivation pipeline (UpdateStatus) | pending |  |  |  |
| CAP-SESS-003 | WEB | Status derivation pipeline (UpdateStatus) | pending |  |  |  |
| CAP-SESS-004 | CLI | Spawn paths: Start / StartWithMessage and the capture-resume | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-004-CLI.txt | Start() spawns tmux pane; AGENTDECK_INSTANCE_ID propagated into tmux session env (=51bd32c7-1781198354, equals the insta |
| CAP-SESS-004 | TUI | Spawn paths: Start / StartWithMessage and the capture-resume | pending |  |  |  |
| CAP-SESS-005 | SESS | Claude conversation binding: CLAUDE_SESSION_ID lifecycle and | pending |  |  |  |
| CAP-SESS-006 | SESS | Duplicate-session killer (killing_duplicate_session) | pending |  |  |  |
| CAP-SESS-007 | CLI | Resume and restart logic (claude --resume) | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-005-007-CLI.txt | ad session restart beta succeeds, returns to idle (fast-path for live tmux). Restart-guard observed: ad session start on |
| CAP-SESS-007 | TUI | Resume and restart logic (claude --resume) | pending |  |  |  |
| CAP-SESS-008 | CLI | Session registry: storage Load/Save semantics (state.db) | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-008-CLI.txt | state.db (SQLite WAL: state.db + -wal + -shm) created at <data>/agent-deck/profiles/personal/. Sessions persist across s |
| CAP-SESS-008 | TUI | Session registry: storage Load/Save semantics (state.db) | pending |  |  |  |
| CAP-SESS-008 | WEB | Session registry: storage Load/Save semantics (state.db) | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-WEB.txt | Headless web server (ad web --no-tui) reads the same state.db as source of truth and serves all sessions via /api/sessio |
| CAP-SESS-009 | TUI | Hook-based status pipeline (Claude Code hooks to status file | pending |  |  |  |
| CAP-SESS-010 | DAEMON/INTERNAL | Parent-child relationships, transition notification, and com | pending |  |  |  |
| CAP-SESS-011 | DAEMON/INTERNAL | Death detection and the parent-signal gap (tmux pane / claud | pending |  |  |  |
| CAP-SESS-012 | CLI | Group membership and per-group concurrency | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-012-CLI.txt | Explicit -g mygroup assigns group; auto-derivation puts path-tail-named group. group list shows per-group session+status |
| CAP-SESS-012 | TUI | Group membership and per-group concurrency | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-TUI-home.txt | TUI renders the group tree with per-group session counts and status rollups (ad-sbx-sess(3), mygroup(1), serialgrp(2)),  |
| CAP-SESS-013 | SESS | Child environment propagation (childenv) and spawn-env hygie | pending |  |  |  |
| CAP-SESS-005 | CLI |  | partial | 2026-06-11 | evidence/sess/ev-CAP-SESS-005-007-CLI.txt | Front door 'session set claude-session-id' sets and clears the binding (shown ''->'test-uuid-1234'->'' incl #923 anchor- |
| CAP-SESS-006 | CLI |  | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-006-CLI.txt | Created a rogue 'agentdeck_rogue_deadbeef' tmux session carrying the SAME AGENTDECK_INSTANCE_ID as alpha; ad session res |
| CAP-SESS-009 | CLI |  | partial | 2026-06-11 | evidence/sess/ev-CAP-SESS-009-handler.txt | hook-handler subcommand exists; hooks dir created at <data>/agent-deck/hooks. Silent-skip guard verified: invoking hook- |
| CAP-SESS-010 | CLI |  | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-010-CLI.txt | session set-parent beta alpha links beta.parent_session_id to alpha's exact ID (0d8070e1-1781198354). unset-parent clear |
| CAP-SESS-010 | WEB |  | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-WEB.txt | noTransitionNotify=true surfaced on beta in /api/sessions, confirming the transition-notify toggle persisted to state.db |
| CAP-SESS-010 | internal/daemon |  | untestable-locally | 2026-06-11 | evidence/sess/ev-CAP-SESS-010-CLI.txt | TransitionDaemon completion delivery (inbox/outbox PULL, Stop-hook drain, task-worker exit edge) requires the notify-dae |
| CAP-SESS-011 | CLI |  | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-011-CLI.txt | Hard-killing the tmux session (simulating pane/agent death) flips status from idle to error on the next UpdateStatus pol |
| CAP-SESS-013 | CLI |  | verified | 2026-06-11 | evidence/sess/ev-CAP-SESS-013-CLI.txt | Started grpsess1 with TELEGRAM_BOT_TOKEN=SECRET in parent env. tmux session env contains NO TELEGRAM var; child shell ex |

## CAP-CLI (49 caps, 57 rows) — partial:9, pending:1, untestable-locally:5, verified:42

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-CLI-001 | CLI | TUI launch (bare `agent-deck`) + global flags | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-001-CLI.txt | Global flags + guards work as a user sees them: -g unknown-group exits 2 ('group not found'); outer-tmux guard prints th |
| CAP-CLI-001 | TUI | TUI launch (bare `agent-deck`) + global flags | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-001-TUI.txt | Bare `agent-deck` boots the bubbletea TUI in a tmux pane: header shows version v1.9.56-verify + status counts + system s |
| CAP-CLI-002 | CLI | add (create session) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-002-CLI.txt | add registers session without starting tmux. Verified: basic add (default group=folder name), --json (keys command/group |
| CAP-CLI-003 | CLI | launch (add+start+send in one step) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-003-CLI.txt | launch = add+start(+send). Session created AND tmux session running. JSON includes BOTH `id` and `session_id` keys (comp |
| CAP-CLI-004 | CLI | list / ls | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-004-CLI.txt | list/ls table (TITLE/GROUP/PATH/ID truncated). --json carries id,title,path,group,tool,command,status,tmux_session,profi |
| CAP-CLI-005 | CLI | remove / rm (top-level) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-005-CLI.txt | remove resolves by exact title, by ID prefix (8 chars >=6), persists (gone from list). not-found exit 2. rm alias works. |
| CAP-CLI-006 | CLI | session remove (registry-scoped remove) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-006-CLI.txt | session remove is registry-scoped and status-gated: refused on a non-stopped/error session ('in state idle; only stopped |
| CAP-CLI-007 | CLI | rename / mv | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-007-CLI.txt | rename persists new title; mv alias works; not-found exit 2; rename sets TitleLocked=true (session show --json title_loc |
| CAP-CLI-008 | CLI | status | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-008-CLI.txt | status default prints '<w> waiting • <r> running • <i> idle'; -q prints only waiting count (script-friendly); -v grouped |
| CAP-CLI-009 | CLI | session start | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-009-CLI.txt | session start creates real tmux session (verified on isolated socket), status flips to idle, already-running error exit  |
| CAP-CLI-010 | CLI | session stop + queue drain | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-010-CLI.txt | session stop verified: stops a running session (status->stopped, tmux killed), stop-not-running error exit 1. Queue DRAI |
| CAP-CLI-011 | CLI | session restart | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-011-CLI.txt | restart core verified: single restart, --force, --all (per-session JSON restarted/failed/total/sessions), not-found exit |
| CAP-CLI-012 | CLI | session revive | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-012-CLI.txt | session revive --all prints the exact summary contract 'revived=N errored=N alive=N dead=N'; --json emits {revived,error |
| CAP-CLI-013 | CLI | session fork | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-013-CLI.txt | Front-door verified: fork rejects non-forkable shell sessions ('not a forkable session (tool: shell)', exit 1), not-foun |
| CAP-CLI-014 | CLI | session attach | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-014-CLI.txt | session attach: not-running error exit 1, not-found exit 2. Interactive attach driven inside a wrapper tmux pane LANDED  |
| CAP-CLI-015 | CLI | session show / session current | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-015-CLI.txt | session show outputs refreshed status, path, group, tool, command, title_locked, tmux name, timestamps, parent link fiel |
| CAP-CLI-016 | CLI | session set (generic field mutation) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-016-CLI.txt | session set: color #RRGGBB / ANSI 0-255 / '' clear all accepted, invalid color rejected with the expected message (exit  |
| CAP-CLI-017 | CLI | session set-parent / unset-parent / set-transition-notify /  | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-017-CLI.txt | set-parent links sub-session (parent_session_id persisted), rejects self ('cannot set as own parent' exit1), rejects par |
| CAP-CLI-018 | CLI | session send (message delivery) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-018-019-020-CLI.txt | session send delivered 'echo SENDMARKER_018' which ran in pane (verified via raw tmux capture). 'Sent message' exit0. No |
| CAP-CLI-019 | CLI | session send-keys (low-level keystroke primitive) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-018-019-020-CLI.txt | send-keys --text + --enter delivered KEYSMARKER (ran). --stream stdin line protocol (T <hex>, E) delivered STREAMMARKER  |
| CAP-CLI-020 | CLI | session output | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-018-019-020-CLI.txt | --pane --json returns ResponseOutput-shaped JSON (content/role/session_id/session_title/success/tool). Default transcrip |
| CAP-CLI-021 | CLI | session search | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-021-022-CLI.txt | Command front door verified: parses query + --limit/--days/--tier, builds GlobalSearchIndex over Claude config dir, emit |
| CAP-CLI-022 | CLI | session move (path move + cross-profile migration) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-021-022-CLI.txt | Form1 move to new path: ProjectPath updated+persisted, --no-restart honored. Form2 --to-profile missing profile -> NOT_F |
| CAP-CLI-023 | CLI | mcp list/attached/attach/detach + mcp server start/stop/stat | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-023-CLI.txt | mcp list (table with [S]/[H] transport tags + -q names). attached (LOCAL split, empty state). attach: unknown -> exit2 + |
| CAP-CLI-024 | CLI | plugin list/attached/attach/detach | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-024-CLI.txt | plugin list shows emits_channel marker + id@source (requires name+source fields in [plugins.*]). attach persists + print |
| CAP-CLI-025 | CLI | skill list/attached/attach/detach/source add\|remove\|list | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-024-025-CLI.txt | source add/duplicate(ALREADY_EXISTS exit1)/list(shows claude-global,pool,custom)/remove(not-found exit2, valid ok). skil |
| CAP-CLI-026 | CLI | group list/create/update/delete/move/change/reorder | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-026-CLI.txt | create (+idempotent, bad-parent exit2, child); list tree + --json nesting; update (default-path, require-one-flag, mutua |
| CAP-CLI-027 | CLI | worktree list/info/cleanup/finish | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-027-CLI.txt | add -w -b creates git worktree on disk. worktree list shows main+worktree with session assoc. info shows branch/path/mai |
| CAP-CLI-028 | CLI | conductor setup/teardown/status/list/move | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-028-CLI.txt | setup: saves config.toml, installs shared CLAUDE.md/POLICY.md/LEARNINGS.md, creates conductor/<name>/{meta.json,CLAUDE.m |
| CAP-CLI-029 | CLI | telegram-doctor | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-029-CLI.txt | Empty profile: 'no telegram channel-owning sessions' exit0 + JSON {channel_owners:0,reports:[]} + quiet silent. With a t |
| CAP-CLI-030 | CLI | watcher create/start/stop/list/status/test/routes/import/ins | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-030-CLI.txt | create webhook(--port required)/ntfy(--topic)/github(--secret-file). inline --secret REFUSED (M2 /proc leak, with explan |
| CAP-CLI-031 | CLI | profile list/create/delete/default | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-031-CLI.txt | bare profile lists (empty msg, marks default with *). create; duplicate ALREADY_EXISTS exit1. default show + set. delete |
| CAP-CLI-032 | CLI | update (self-update) + remote propagation | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-032-CLI.txt | Flag surface verified via --help (--check, --version). --check performs a real GitHub releases API call (version banner  |
| CAP-CLI-033 | CLI | uninstall | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-033-CLI.txt | --help flag surface (--dry-run/--keep-data/--keep-tmux-config/-y). --dry-run scans binary + XDG config + data (profile/s |
| CAP-CLI-034 | CLI | migrate-paths | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-034-CLI.txt | migrate-paths --dry-run reports 'nothing to migrate' and leaves legacy dir untouched; with a legacy ~/.agent-deck presen |
| CAP-CLI-035 | CLI | hook-handler (Claude Code hook ingestion) + hooks install/un | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-035-CLI.txt | hooks install writes ~/.claude/settings.json (status NOT INSTALLED -> INSTALLED -> uninstall); hook-handler requires AGE |
| CAP-CLI-036 | CLI | codex-notify + codex-hooks / gemini-hooks / hermes-hooks ins | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-036-CLI.txt | codex-notify reads JSON from stdin AND argv; fuzzy event mapping confirmed (turn.complete->waiting, turn.start->running) |
| CAP-CLI-037 | CLI | notify-daemon (transition notifier) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-037-CLI.txt | notify-daemon --once performs a single SyncOnce+Flush and exits 0 cleanly within timeout. |
| CAP-CLI-038 | CLI | run-task (one-shot worker completion wrapper) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-038-CLI.txt | run-task --child <valid-id> -- echo TASKRAN runs the command verbatim (stdout passes through), exit 0; worker nonzero ex |
| CAP-CLI-039 | CLI | inbox / inbox drain | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-039-CLI.txt | inbox drain resolves caller id from AGENTDECK_INSTANCE_ID (and explicit id), emits '[]' (never null) in --json and 'No p |
| CAP-CLI-040 | CLI | costs sync/summary/recompute | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-040-CLI.txt | costs summary --json emits the RemoteCostSummary wire shape (cost_today/yesterday/this_week/last_week/this_month/last_mo |
| CAP-CLI-041 | CLI | creds-refresh (OAuth keep-warm daemon) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-041-CLI.txt | creds-refresh --once with no .credentials.json in default/specified dirs -> 'no profile config dir with a .credentials.j |
| CAP-CLI-042 | CLI | openclaw / oc sync\|bridge\|status\|list\|send | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-042-CLI.txt | CLI front door verified: openclaw list/status and the 'oc' alias connect to the configured gateway (default ws 127.0.0.1 |
| CAP-CLI-043 | CLI | remote add/remove/list/sessions/attach/rename/update | partial | 2026-06-11 | evidence/cli/ev-CAP-CLI-043-CLI.txt | add persists [remotes.<name>] to config.toml (host/agent_deck_path/profile) and the ssh CheckBinary probe failing only w |
| CAP-CLI-044 | CLI | web (TUI+HTTP server, or headless --no-tui) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-044-CLI.txt | Security gate: 'web --no-tui --listen 0.0.0.0:PORT' without --token is REFUSED (exit 1) with the exact RCE-surface messa |
| CAP-CLI-044 | TUI | web (TUI+HTTP server, or headless --no-tui) | untestable-locally | 2026-06-11 | evidence/cli/ev-CAP-CLI-044-WEB.png | The combined TUI+HTTP mode (bubbletea + server) belongs to the TUI surface area; the --no-tui headless path and the secu |
| CAP-CLI-044 | WEB | web (TUI+HTTP server, or headless --no-tui) | pending |  |  |  |
| CAP-CLI-045 | CLI | mcp-proxy (internal stdio↔unix-socket bridge) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-045-CLI.txt | mcp-proxy with no arg -> 'Usage: agent-deck mcp-proxy <socket-path>' exit 1; against a live python unix-socket echo serv |
| CAP-CLI-046 | CLI | try (dated experiments) | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-046-CLI.txt | try --list on empty reports 'No experiments in <dir>/tries'; 'try myexp --no-session -c bash' creates the date-prefixed  |
| CAP-CLI-047 | CLI | feedback | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-047-CLI.txt | feedback --help documents the disclose-before-post contract (#679: shows URL/gh login/exact body, default-N confirm, no  |
| CAP-CLI-048 | CLI | debug-dump / version / help | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-048-CLI.txt | version, --version, -v all print 'Agent Deck v1.9.56-verify' exit 0 (offline, no network); debug-dump writes debug-dump- |
| CAP-CLI-049 | CLI | shared CLI infrastructure (resolution, output, exit-code con | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-049-CLI.txt | ResolveSession verified across all three modes: exact title, ID prefix (>=6 chars), exact path. CLIOutput: --json error  |
| CAP-CLI-028 | daemon |  | untestable-locally | 2026-06-11 | evidence/cli/ev-CAP-CLI-028-CLI.txt | Heartbeat launchd/systemd daemon + bridge.py + transition-notifier as long-lived daemons require a real systemd/launchd  |
| CAP-CLI-035 | daemon |  | untestable-locally | 2026-06-11 | evidence/cli/ev-CAP-CLI-035-CLI.txt | The daemon-side consumers (transition notifier reading these status files, DrainForStopHook stdout block injection) are  |
| CAP-CLI-037 | daemon |  | untestable-locally | 2026-06-11 | evidence/cli/ev-CAP-CLI-037-CLI.txt | The long-lived daemon loop (SIGINT/SIGTERM context, watchBinaryVersion re-exec every 60s, #1214 stale-daemon recycle) is |
| CAP-CLI-041 | daemon |  | untestable-locally | 2026-06-11 | evidence/cli/ev-CAP-CLI-041-CLI.txt | The actual OAuth refresh-token exchange against Anthropic, atomic rewrite under proper-lockfile, and the SIGINT/SIGTERM  |
| CAP-CLI-044 | web |  | verified | 2026-06-11 | evidence/cli/ev-CAP-CLI-044-WEB.png | web --no-tui boots headless ('Headless mode: TUI disabled', 'Web server: http://127.0.0.1:8492'); GET /api/sessions retu |

## CAP-TUI (37 caps, 44 rows) — partial:7, pending:3, verified:34

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-TUI-001 | TUI | Home screen: session list / group tree navigation | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-001-TUI.txt | Home renders flattened tree: group headers with counts (ad-sbx... (3)), nested sessions (alpha/beta/cl-sess), second emp |
| CAP-TUI-002 | TUI | Attach / detach / open-in-new-window | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-002-TUI.txt | Enter on running 'beta' attached via PTY into its tmux ([agentdeck_beta_...] 0:bash*, status line 'ctrl+q detach \| beta |
| CAP-TUI-003 | TUI | New session dialog (n) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-003-TUI.txt | n opens New Session dialog: Name field, Multi-repo toggle, Path (defaulted to cursor group's path), command pill picker  |
| CAP-TUI-004 | TUI | Quick create (N) and zoxide quick-open (z) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-004-TUI.txt | N quick-create instantly created session 'stone-pine' with no dialog and an auto-generated unique name; appeared in ad l |
| CAP-TUI-005 | TUI | Fork: quick fork (f) and fork dialog (F) | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-005-CLI.txt | Fork gating verified on both surfaces: F/f did NOT open the fork dialog on shell sessions or non-resumable claude sessio |
| CAP-TUI-005 | WEB | Fork: quick fork (f) and fork dialog (F) | pending |  |  |  |
| CAP-TUI-006 | TUI | Session lifecycle: delete (d) / close (D) / remove (X) / bul | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-006c-TUI.txt | ConfirmDialog verified for all variants. d: 'Delete Session? beta, Any running processes will be killed' -> y -> removed |
| CAP-TUI-006 | WEB | Session lifecycle: delete (d) / close (D) / remove (X) / bul | pending |  |  |  |
| CAP-TUI-007 | TUI | Restart (R) and restart-fresh (T) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-007-TUI.txt | R restart on running beta kept it live; R on errored gitsess started it (created live tmux session, status flipped error |
| CAP-TUI-008 | TUI | MCP Manager (m) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-008-TUI.txt | m on a claude session opens MCP Manager: scopes [LOCAL] GLOBAL USER, Tab cycles them (LOCAL='.mcp.json this project'; GL |
| CAP-TUI-009 | TUI | Plugin Manager (L) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-009-TUI.txt | L on claude session opens Plugin Manager: title, 'space toggle · up/down move · enter apply · esc cancel' keys, empty-ca |
| CAP-TUI-010 | TUI | Skills Manager (s) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-010-TUI.txt | s on claude session opens Skills Manager: POOL scope, 'Writes to: .agent-deck/skills.toml + .claude/skills (project)', e |
| CAP-TUI-011 | TUI | Edit Session dialog (P) and Edit Paths dialog (p) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-011-TUI.txt | P opens Edit Session dialog (local sessions): Title field (cl-sess), Tool pill row (restart), Claude-specific '[x] Skip  |
| CAP-TUI-012 | TUI | Group management (g create, r rename, M move, reorder/indent | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-012-TUI.txt | g opens GroupDialog 'Create New Group' with [Root]/Subgroup toggle (Tab toggle/next, Shift+Tab prev), Name field. M open |
| CAP-TUI-013 | CLI | Group-scoped launch (--group) and group-scoped navigation la | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-013-TUI.txt | 'agent-deck --group myscope' launched TUI scoped to that subtree: header shows 'Agent Deck [myscope]' and list shows ONL |
| CAP-TUI-013 | TUI | Group-scoped launch (--group) and group-scoped navigation la | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-013-TUI.txt | Alt+j/Alt+k navigate within current group (moved beta<->stone-pine). nav-hint sentinel '.nav-hint-v1760-shown' written u |
| CAP-TUI-014 | TUI | Jump mode (Space) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-014-TUI.txt | Space enters jump mode: home-row hint labels spliced into item names (alpha->dlpha, beta->feta, cl-sess->jl-sess, group- |
| CAP-TUI-015 | TUI | Search: local (/), global (G), status-keyword filters | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-015-TUI.txt | / opens '🔍 Local Search (Agent Deck sessions)' with status-keyword hint (waiting/running/idle); typing 'beta' live-filte |
| CAP-TUI-016 | TUI | Status filters and filter bar | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-016-TUI.txt | Filter bar renders with legend '!@#$ filter • 0 all • % open' and live status counts (All ● ◐ ○ ✕ counts). 0 clears, ! @ |
| CAP-TUI-017 | TUI | Preview pane (right/bottom panel) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-017-TUI.txt | Preview pane shows session info card (Repo/Path/tool/timestamps, Claude status card), live tmux output tail (typed 'echo |
| CAP-TUI-018 | TUI | Copy / send output (c, C, x) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-018b-TUI.txt | x opens SessionPickerDialog 'Send Output To...' (Enter send / Esc cancel / j/k navigate). c (copy last AI response) and  |
| CAP-TUI-019 | TUI | Insert mode (I): type-through to session | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-019b-TUI.txt | I on running beta enters insert mode: bottom bar becomes '-- INSERT -- → beta  Esc to exit · Enter to submit'. Typed 'ec |
| CAP-TUI-020 | TUI | Quick approve (a), mark unread (u), YOLO toggle (y), Gemini  | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-020-TUI-markunread.txt, ev-CAP-TUI-020-TUI-yolo-toggle.txt, ev-CAP-TUI-020-TUI-gemini-model.txt, ev-CAP-TUI-020-TUI-quickapprove.txt | u mark-unread persists acknowledged=0 to SQLite (verified). y yolo toggle on gemini session flipped tool_data {} -> {"ge |
| CAP-TUI-021 | TUI | Worktree finish (W) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-021-TUI-worktree-finish.txt | W on a real git-worktree session opened the two-step Finish Worktree dialog: Session/Branch/Status (clean, async uncommi |
| CAP-TUI-022 | TUI | Settings panel (S) + tool visibility panel | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-022-TUI-settings.txt, ev-CAP-TUI-022-TUI-settings-full.txt, ev-CAP-TUI-022-CLI-config-preserved.txt | S opens settings with all sections (Theme radio, Default tool, Claude dangerous/config-dir, Gemini/Codex/Hermes YOLO, Up |
| CAP-TUI-023 | TUI | Watcher panel (w) + watcher event dispatch | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-023-TUI-watcher.txt | w opens the Watcher overlay: WATCHERS header, 'No watchers configured' empty state, quick actions [Enter] Details [s] St |
| CAP-TUI-024 | TUI | Cost dashboard ($) and analytics | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-024-TUI-cost-dashboard.txt | $ opened the full-screen Cost Dashboard (costs.Store IS wired in this build): Today/Week/Month/Projected cards, Today to |
| CAP-TUI-025 | TUI | Remote sessions (SSH remotes section) | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-025-CLI-remote.txt, ev-CAP-TUI-025-TUI-remotes.txt | Remote registration front door works: 'remote add' wrote [remotes.testremote] and 'remote list' shows it (host/path/prof |
| CAP-TUI-026 | TUI | Import tmux sessions (i) and manual reload (ctrl+r) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-026-TUI-import.txt, ev-CAP-TUI-026-TUI-reload.txt | i discovered an orphan tmux session (orphan_import_test) not in the registry, appended it as an instance AND added to th |
| CAP-TUI-027 | TUI | Status detection, notifications, hooks prompt | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-027-TUI-hooks-prompt.txt | First-run ConfirmInstallHooks dialog shown ('Claude Code Hooks', Install/Skip, y install/n skip). Choosing Skip ('n') pe |
| CAP-TUI-028 | TUI | Quit flow and shutdown | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-028-TUI-quit.txt | q cleanly exits the TUI (tmux session ends). On quit, ui_state metadata is persisted (cursor_group_path, preview_mode) a |
| CAP-TUI-029 | TUI | Help overlay (?), footer help bar, update nudge, feedback di | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-029-TUI-help.txt, ev-CAP-TUI-029-TUI-feedback.txt | ? opens the scrollable shortcuts modal with live bindings (NAVIGATION, GROUP NAVIGATION v1.7.60 Alt-layer, SESSIONS sect |
| CAP-TUI-030 | TUI | Setup wizard (first run) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-030-TUI-setup-wizard.txt, evidence/tui/ev-CAP-TUI-030-CLI-wizard-config.txt | Launching with no config.toml shows the wizard: Welcome -> Tool selection (claude/gemini/opencode/codex/pi/shell/...) -> |
| CAP-TUI-031 | TUI | Hotkey remapping ([hotkeys] config) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-031-TUI-hotkey-remap.txt | [hotkeys] new_session='z' remapped 'z' to open the New Session dialog (overriding default zoxide binding). The rebound d |
| CAP-TUI-032 | TUI | Mouse support | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-032-TUI-mouse.txt | SGR mouse click at row 8 moved the cursor to the correctly-mapped list item (m2) — Y-to-index math accounts for header/b |
| CAP-TUI-033 | TUI | Notes editing (e) | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-033-TUI-notes-edit.txt, ev-CAP-TUI-033-TUI-notes-saved.txt | With [preview] show_notes=true, e on a live session opens the Notes textarea ('Ctrl+S save • Esc cancel', split with Out |
| CAP-TUI-034 | TUI | Sandbox shell exec (E) | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-034-TUI-sandbox-shell.txt | E on a non-sandboxed session is a no-op (correctly gated — only sessions with a SandboxContainer open a container shell) |
| CAP-TUI-035 | TUI | Web bridge (WebMutator) and web menu snapshots | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-035-WEB.png | Web/TUI share the same Home/profile; the web UI's session list/detail mirror the TUI state (publishWebMenuSnapshot). No  |
| CAP-TUI-035 | WEB | Web bridge (WebMutator) and web menu snapshots | pending |  |  |  |
| CAP-TUI-036 | TUI | Theme handling (system dark/light watcher) | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-036-TUI-theme-system.txt, ev-CAP-TUI-036-TUI-theme-launched.txt | Theme setting offers dark/light/system radio (settings panel + setup wizard). Toggling theme persists to config.toml the |
| CAP-TUI-037 | TUI | Keyboard compatibility layer (Kitty/CSI u, Shift+Enter) | partial | 2026-06-11 | evidence/tui/ev-CAP-TUI-032-TUI-mouse.txt | The Private-Use rune U+E5E5 (the Shift+Enter relay) was processed by normalizeMainKey without crashing the TUI (no-op on |
| CAP-TUI-008 | CLI |  | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-008-TUI.txt | CLI mirror confirmed present: 'agent-deck mcp attach/detach/attached/list' subcommands exist (referenced in contract; su |
| CAP-TUI-025 | CLI |  | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-025-CLI-remote.txt | CLI mirror verified: remote add/list/sessions present and functioning; unreachable remote errors are surfaced cleanly wi |
| CAP-TUI-035 | web |  | verified | 2026-06-11 | evidence/tui/ev-CAP-TUI-035-WEB-sessions.json, ev-CAP-TUI-035-WEB-create.json, ev-CAP-TUI-035-WEB.png | ad web --no-tui serves the REST API: GET /api/sessions returns the full TUI-mirrored list (titles, notes, titleLocked, s |

## CAP-WEB (18 caps, 23 rows) — partial:1, pending:4, verified:18

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-WEB-001 | CLI | Web server bootstrap, config and security gate | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-001-CLI-help.txt, ev-CAP-WEB-001-CLI-bindgate.txt, ev-CAP-WEB-001-CLI-emptyhost.txt, ev-CAP-WEB-016-CLI-pushtest-requires.txt | web --help shows all flags (--listen/--read-only/--token/--insecure-bind/--push/--push-vapid-subject/--push-test-every/- |
| CAP-WEB-001 | TUI | Web server bootstrap, config and security gate | pending |  |  |  |
| CAP-WEB-001 | WEB | Web server bootstrap, config and security gate | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-001-mutdisabled.txt, ev-CAP-WEB-002-healthz-unauth.json | --read-only sets webMutations=false; POST mutation returns 403 MUTATIONS_DISABLED while GET still 200. Error envelope un |
| CAP-WEB-002 | WEB | Authentication (bearer token) + CSRF protection | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-002-noauth.json, ev-CAP-WEB-002-csrf.txt, ev-CAP-WEB-002-csrf-mismatch.txt, ev-CAP-WEB-002-csrf-ok.txt, ev-CAP-WEB-002-healthz-unauth.json | With --token: no token->401 UNAUTHORIZED, wrong token->401, correct Bearer->200, ?token= on REST rejected 401. healthz u |
| CAP-WEB-003 | WEB | Menu / session-list read API with hook-status overlay | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-003-menu.json, ev-CAP-WEB-003-sessions.json, ev-CAP-WEB-003-groups.json, ev-CAP-WEB-003-session-single.json, ev-CAP-WEB-003-404.json | GET /api/menu returns {profile,generatedAt,totalGroups,totalSessions,items[]} with group/session MenuItems. /api/session |
| CAP-WEB-004 | WEB | SSE live menu stream | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-004-sse.txt | GET /events/menu emits 'event: menu' + full MenuSnapshot JSON immediately. Content-Type text/event-stream, Cache-Control |
| CAP-WEB-005 | WEB | Session lifecycle mutations (create/start/stop/restart/close | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-005-create.txt, ev-CAP-WEB-005-create-bad.txt, ev-CAP-WEB-005-badaction.txt, ev-CAP-WEB-005-tui-restart.txt, ev-CAP-WEB-005-tui-delete.txt, ev-CAP-WEB-005-tui-undelete.txt | POST /api/sessions create->201 {sessionId} and persists (read-back confirms). Missing title->400 INVALID_REQUEST. Unknow |
| CAP-WEB-006 | WEB | Session field edit (PATCH /api/sessions/{id}) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-006-tui-patch-valid.txt, ev-CAP-WEB-006-emptytitle.txt, ev-CAP-WEB-006-nofields.txt, ev-CAP-WEB-006-tui-restartreq.txt | PATCH /api/sessions/{id} via TUI mutator returns 200 {sessionId,updatedFields:[notes,color,title],restartRequired:false} |
| CAP-WEB-007 | WEB | Group management (create/rename/delete) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-007-tui-create.txt, ev-CAP-WEB-007-tui-rename.txt, ev-CAP-WEB-007-tui-del.txt, ev-CAP-WEB-007-subgroup.txt | Via TUI mutator: POST /api/groups {name}->201 {path}; PATCH /api/groups/{path} {name}->200 {name,path} returning OLD pat |
| CAP-WEB-008 | WEB | Worktree finish (merge + teardown) endpoint | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-008-tui-finish.txt, ev-CAP-WEB-008-finish-404.txt | POST /api/sessions/{id}/worktree/finish on a non-worktree session->400 INVALID_REQUEST 'session is not in a worktree' (E |
| CAP-WEB-009 | WEB | Children topology endpoint (conductor tree) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-009-children.json | GET /api/sessions/{id}/children->200 {sessionId,children:[]} (empty array for leaf). Non-GET (POST)->405. Unknown id->40 |
| CAP-WEB-010 | TUI | Skills management API | pending |  |  |  |
| CAP-WEB-010 | WEB | Skills management API | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-010-skills.json, ev-CAP-WEB-010-session-skills.txt, ev-CAP-WEB-010-attach.txt, ev-CAP-WEB-010-claude-skills.txt, ev-CAP-WEB-010-claude-attach.txt, ev-CAP-WEB-010-claude-detach.txt | GET /api/skills->{skills:[]} catalog (empty in sandbox). GET /api/sessions/{id}/skills->200 {skills:[]}. Attach to shell |
| CAP-WEB-011 | TUI | MCP management API | pending |  |  |  |
| CAP-WEB-011 | WEB | MCP management API | partial | 2026-06-11 | evidence/web/ev-CAP-WEB-011-mcps.txt, ev-CAP-WEB-011-session-mcps.txt, ev-CAP-WEB-011-attach.txt, ev-CAP-WEB-011-patch.txt | CONFIRMED PRODUCTION WIRING GAP (matches v2 notes): the shipped 'agent-deck web' binary never calls SetMCPManager, so GE |
| CAP-WEB-012 | WEB | Costs API + costs SSE stream | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-012-summary.txt, ev-CAP-WEB-012-daily.txt, ev-CAP-WEB-012-batch.txt, ev-CAP-WEB-012-batch-post.txt, ev-CAP-WEB-012-stream.txt, ev-CAP-WEB-012-export.csv | /api/costs/summary->{today/week/month_usd+events,projected_usd}. /api/costs/daily?days=7->[]. /api/costs/batch GET and P |
| CAP-WEB-013 | WEB | System stats endpoint | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-013-system.json | GET /api/system/stats->200 with cpu, disk, load, memory, network sections (gpu absent as expected when sysinfo reports u |
| CAP-WEB-014 | WEB | Settings + profiles read endpoints | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-014-settings.json, ev-CAP-WEB-014-profiles.json | GET /api/settings->{profile,readOnly,webMutations,version,toolFilter,visibleTools[],toolFilterFallback,hiddenTools[],pic |
| CAP-WEB-015 | WEB | WebSocket terminal bridge (live tmux attach in browser) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-015-ws.txt | GET /ws/session/{id} upgrades to WebSocket; emits {status,connected} then {status,ready}. When tmux session absent, send |
| CAP-WEB-016 | CLI | Web Push notifications (PWA) | pending |  |  |  |
| CAP-WEB-016 | WEB | Web Push notifications (PWA) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-016-config.json, ev-CAP-WEB-016-subscribe.txt, ev-CAP-WEB-016-presence.txt, ev-CAP-WEB-016-unsubscribe.txt, ev-CAP-WEB-016-files.txt, ev-CAP-WEB-016-CLI-pushtest-requires.txt, ev-CAP-WEB-016-subscribe-disabled.txt | --push generates VAPID keypair: /api/push/config->{enabled:true,subject:mailto:agentdeck@localhost,vapidPublicKey(87-cha |
| CAP-WEB-017 | WEB | Static assets, SPA serving and PWA shell | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-017-spa.png, ev-CAP-WEB-017-index.html | GET / serves index.html (text/html, Referrer-Policy: no-referrer, Cache-Control no-cache/no-store). GET /s/{deeplink}->2 |
| CAP-WEB-018 | WEB | Parity contract with TUI/CLI (what web can and cannot do) | verified | 2026-06-11 | evidence/web/ev-CAP-WEB-018-parity.txt, ev-CAP-WEB-018-methodnotallowed.txt | Parity probes: TUI-only MISSING actions all 404 (restart-fresh, rename, move, yolo, approve, /api/search, /api/help). In |

## CAP-FORK (14 caps, 31 rows) — partial:8, pending:14, untestable-locally:2, verified:7

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-FORK-001 | CLI | Claude conversation fork (capture-resume via --fork-session) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-002-003-004-CLI-gate.txt | Fork CLI front door verified: `agent-deck session fork` exists with all documented flags (-t,-g,-w,-b,--with-state,--wit |
| CAP-FORK-001 | TUI | Claude conversation fork (capture-resume via --fork-session) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-TUI-help.txt | TUI keybinding `f/F Fork session (Claude/Pi)` confirmed present in help screen. Pressing F on an unstarted (error-state) |
| CAP-FORK-001 | WEB | Claude conversation fork (capture-resume via --fork-session) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-005-WEB.txt | Web /api/sessions exposes per-session `canFork` boolean (=false for unstarted claude, matching the CanFork freshness gat |
| CAP-FORK-002 | CLI | OpenCode fork (export/sed/import script) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-002-003-004-CLI-gate.txt | CanForkOpenCode gate verified: `session fork tool-opencode` -> 'cannot be forked: no resumable session for tool opencode |
| CAP-FORK-002 | TUI | OpenCode fork (export/sed/import script) | pending |  |  |  |
| CAP-FORK-002 | WEB | OpenCode fork (export/sed/import script) | pending |  |  |  |
| CAP-FORK-003 | CLI | Pi fork (JSONL file fork) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-002-003-004-CLI-gate.txt | CanForkPi gate verified: `session fork tool-pi` -> 'cannot be forked: no resumable session for tool pi' (exit 1). pi bin |
| CAP-FORK-003 | TUI | Pi fork (JSONL file fork) | pending |  |  |  |
| CAP-FORK-003 | WEB | Pi fork (JSONL file fork) | pending |  |  |  |
| CAP-FORK-004 | CLI | Codex fork (`codex fork <sid>`) | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-004-CLI-notforkable.txt | Non-forkable rejection verified: `session fork bashsess` -> 'not a forkable session (tool: shell)' (exit 1). CanForkCode |
| CAP-FORK-004 | TUI | Codex fork (`codex fork <sid>`) | pending |  |  |  |
| CAP-FORK-004 | WEB | Codex fork (`codex fork <sid>`) | pending |  |  |  |
| CAP-FORK-005 | CLI | Fork dispatch and post-create pipeline | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-005-CLI-noexist.txt | Fork dispatch pipeline gates verified in order: session-resolve (non-existent -> 'session not found' exit 2), tool gate  |
| CAP-FORK-005 | TUI | Fork dispatch and post-create pipeline | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-TUI-help.txt | TUI fork dispatch surfaces present: `f/F Fork session`, `F -> w Fork session into worktree`. TUI renders home list and f |
| CAP-FORK-005 | WEB | Fork dispatch and post-create pipeline | pending |  |  |  |
| CAP-FORK-006 | CLI | Fork-with-state worktree (git, #1029/#1263) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-006-CLI-nostate-now.txt | --with-state / --with-state-and-gitignored flags present and documented (implies/requires -w). On a shell session the to |
| CAP-FORK-006 | TUI | Fork-with-state worktree (git, #1029/#1263) | pending |  |  |  |
| CAP-FORK-007 | CLI | Fork-with-state workspace (jujutsu, #1305) | untestable-locally | 2026-06-11 | evidence/fork/ev-CAP-FORK-010-nonrepo.txt | jujutsu (jj) is NOT installed on this host (command -v jj = NO). The jj fork-with-state workspace path (WorkingCopyParen |
| CAP-FORK-007 | TUI | Fork-with-state workspace (jujutsu, #1305) | pending |  |  |  |
| CAP-FORK-008 | CLI | Plain worktree creation, path generation, branch resolution | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-008-DB.txt | Plain worktree creation fully verified via `add -w`: NEW branch with -b creates branch+worktree at <repo>/.worktrees/<sa |
| CAP-FORK-008 | TUI | Plain worktree creation, path generation, branch resolution | pending |  |  |  |
| CAP-FORK-009 | CLI | Worktree removal, session-dismiss cleanup, #1200 data-loss g | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-009-CLI-finish.txt | Worktree removal/cleanup verified: top-level `remove` cleans up the worktree dir while keeping the main repo intact (#12 |
| CAP-FORK-009 | TUI | Worktree removal, session-dismiss cleanup, #1200 data-loss g | pending |  |  |  |
| CAP-FORK-010 | CLI | VCS backend abstraction and detection (git/jujutsu) | partial | 2026-06-11 | evidence/fork/ev-CAP-FORK-010-git.txt | git backend detection verified: `worktree list --json` resolves repo_root, marks main vs worktree types, routes worktree |
| CAP-FORK-010 | TUI | VCS backend abstraction and detection (git/jujutsu) | pending |  |  |  |
| CAP-FORK-011 | DAEMON/INTERNAL | Child environment construction (telegram/CLAUDE_CONFIG_DIR i | pending |  |  |  |
| CAP-FORK-012 | CLI | Parent-child session links (sub-sessions) | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-012-DB.txt | Parent-child links fully verified: `add --parent` sets parent_session_id (persisted in state.db); single-level guard rej |
| CAP-FORK-012 | TUI | Parent-child session links (sub-sessions) | pending |  |  |  |
| CAP-FORK-013 | TUI | Multi-repo worktree creation and fork propagation | untestable-locally | 2026-06-11 | evidence/fork/ev-CAP-FORK-009-cleanup.txt | Multi-repo worktree creation and fork propagation is TUI-driven (the 'p' Edit multi-repo paths keybind / fork of a multi |
| CAP-FORK-014 | CLI | Spawn singleflight guard (#1040 restart storm) | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-014-storm.txt | Spawn singleflight guard (#1040) verified: `session start` creates locks/instance-spawn-<id>.stamp under the data locks  |
| CAP-FORK-011 | internal-daemon |  | verified | 2026-06-11 | evidence/fork/ev-CAP-FORK-011-env.txt | Child env isolation verified by inspecting the spawned tmux session env: parent shell exported TELEGRAM_BOT_TOKEN/TELEGR |

## CAP-TMUX (21 caps, 45 rows) — partial:6, pending:14, untestable-locally:4, verified:21

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-TMUX-001 | CLI | Session naming and identity | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-001-CLI.txt | add -t 'My Cool!Sess@01' produced tmux_session 'agentdeck_My-Cool-Sess-01_<8hex>'. Prefix 'agentdeck_', sanitizeName rep |
| CAP-TMUX-001 | TUI | Session naming and identity | pending |  |  |  |
| CAP-TMUX-001 | WEB | Session naming and identity | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-WEB.txt | /api/sessions returns tmuxSession 'agentdeck_logtest_<hex>' and 'agentdeck_My-Cool-Sess-01_<hex>' — same prefix+sanitize |
| CAP-TMUX-002 | CLI | Session creation (Start) with 3-tier systemd launch fallback | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-002-CLI.txt,evidence/tmux/ev-CAP-TMUX-002b-CLI.txt | session start created tmux session (direct launch, no systemd needed in sandbox). show-options confirms the batched set- |
| CAP-TMUX-002 | TUI | Session creation (Start) with 3-tier systemd launch fallback | pending |  |  |  |
| CAP-TMUX-003 | CLI | Socket isolation (-L plumbing) | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-003-CLI.txt,evidence/tmux/ev-CAP-TMUX-003-isolation.txt | All sessions live ONLY on isolated socket 'advfixed' (adtmux -L). Zero agentdeck test sessions on the default server (pr |
| CAP-TMUX-003 | TUI | Socket isolation (-L plumbing) | pending |  |  |  |
| CAP-TMUX-003 | WEB | Socket isolation (-L plumbing) | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-WEB.txt | tmuxSocketName:'advfixed' present per session in /api/sessions — socket isolation plumbed through to web API. |
| CAP-TMUX-004 | CLI | Existence/liveness probing and per-tick caches | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-status.txt,evidence/tmux/ev-CAP-TMUX-010-CLI.txt | Liveness reflected in list/status: running session shows idle, stopped shows stopped, 'status' aggregates '0 waiting • 0 |
| CAP-TMUX-004 | TUI | Existence/liveness probing and per-tick caches | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-TUI.txt | TUI list shows live status markers (○ idle, ■ stopped) per session, refreshed per tick; preview pane shows live capture. |
| CAP-TMUX-004 | WEB | Existence/liveness probing and per-tick caches | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-WEB.png | Web UI Fleet view renders per-session status (idle/stopped) and group counts. |
| CAP-TMUX-005 | CLI | Send-keys / prompt delivery semantics | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-005-CLI.txt,evidence/tmux/ev-CAP-TMUX-005-large.txt | session send delivered 'echo HELLO_FROM_SEND_12345' which executed (output captured). SendKeysAndEnter path: 100ms paste |
| CAP-TMUX-005 | TUI | Send-keys / prompt delivery semantics | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-008-confirm.txt | In TUI attach mode, typed keystrokes ('echo ATTACHED_PANE_OK_777') forwarded to the pane and executed — SendNamedKey/ins |
| CAP-TMUX-005 | WEB | Send-keys / prompt delivery semantics | pending |  |  |  |
| CAP-TMUX-006 | CLI | Output capture | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-006-CLI.txt,evidence/tmux/ev-CAP-TMUX-006-parity.txt | session output returns visible pane content (CapturePane). Parity with raw adtmux capture-pane confirmed (both reflect s |
| CAP-TMUX-006 | TUI | Output capture | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-TUI.txt | TUI preview pane streams live captured Output (sudo banner + prompt) for the selected session. |
| CAP-TMUX-006 | WEB | Output capture | pending |  |  |  |
| CAP-TMUX-007 | TUI | Control-mode pipe layer (ControlPipe + PipeManager) | pending |  |  |  |
| CAP-TMUX-007 | WEB | Control-mode pipe layer (ControlPipe + PipeManager) | pending |  |  |  |
| CAP-TMUX-008 | CLI | Attach / detach (PTY proxy) | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-008-TUI-attached.txt | session attach exists as a CLI subcommand and the underlying Attach PTY proxy is the same code driven via TUI (verified) |
| CAP-TMUX-008 | TUI | Attach / detach (PTY proxy) | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-008-TUI-attached.txt,evidence/tmux/ev-CAP-TMUX-008-TUI-typed.txt,evidence/tmux/ev-CAP-TMUX-008-TUI-detached.txt,evidence/tmux/ev-CAP-TMUX-008-confirm.txt | Enter attached to full-screen pane (status-right 'ctrl+q detach │ 📁 logtest \| proj2'); typed command ran; Ctrl+Q (0x11  |
| CAP-TMUX-009 | TUI | Terminal-reply filtering (internal/termreply) | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-008-TUI-detached.txt | termreply Filter is an internal stdin-filtering layer active during attach quarantine. Indirectly exercised: attach/deta |
| CAP-TMUX-010 | CLI | Status detection state machine (GetStatus) | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-010-CLI.txt,evidence/tmux/ev-CAP-TMUX-010-show.txt,evidence/tmux/ev-CAP-TMUX-010-transition.txt | Status state machine verified for inactive/idle/stopped/active-on-create transitions via lifecycle (start->idle, stop->s |
| CAP-TMUX-010 | TUI | Status detection state machine (GetStatus) | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-004-TUI.txt | TUI status bar shows color-coded counts (idle/stopped) and per-session markers; status legend ● ◐ ○ ■ rendered. |
| CAP-TMUX-010 | WEB | Status detection state machine (GetStatus) | pending |  |  |  |
| CAP-TMUX-011 | CLI | Tool detection and configurable patterns | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-011-CLI.txt | DetectTool by command basename verified: command '/tmp/.../bin/claude' detected as tool 'claude'; command 'bash' detecte |
| CAP-TMUX-011 | TUI | Tool detection and configurable patterns | pending |  |  |  |
| CAP-TMUX-012 | CLI | Kill / respawn / process-tree reaping | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-012-CLI.txt | session restart respawned the session (new 8-hex suffix, tmux still alive). session stop killed the tmux session (has-se |
| CAP-TMUX-012 | TUI | Kill / respawn / process-tree reaping | verified | 2026-06-11 | evidence/tmux/ev-CAP-TUI-help.txt | TUI help confirms R=Restart session, T=Restart with new session ID, d=Delete session bindings — same Kill/RespawnPane fr |
| CAP-TMUX-013 | CLI | Per-session tmux environment variables | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-013-CLI.txt | SetEnvironment stamped AGENTDECK_INSTANCE_ID into the tmux session env at Start; show-environment returns 'AGENTDECK_INS |
| CAP-TMUX-014 | TUI | Status bar, notification bar, quick-switch keys, terminal ti | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-014-CLI.txt,evidence/tmux/ev-CAP-TMUX-014b-CLI.txt,evidence/tmux/ev-CAP-TMUX-008-TUI-attached.txt | ConfigureStatusBar set status-right '#[..]ctrl+q detach#[default] │ 📁 My Cool!Sess@01 \| proj one ', status-left-length  |
| CAP-TMUX-015 | CLI | iTerm2 badge chrome and mid-attach badge updates | pending |  |  |  |
| CAP-TMUX-015 | TUI | iTerm2 badge chrome and mid-attach badge updates | untestable-locally | 2026-06-11 | evidence/tmux/ev-CAP-TUI-help.txt | iTerm2 OSC 1337 badge emission is gated on TERM_PROGRAM=iTerm.app / LC_TERMINAL=iTerm2 (macOS). This Linux host is not i |
| CAP-TMUX-016 | CLI | Session log maintenance | pending |  |  |  |
| CAP-TMUX-017 | CLI | Terminal emulator detection and capabilities | untestable-locally | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-018-CLI.txt | DetectTerminal/GetTerminalInfo is an internal capability table with no CLI/TUI front door that prints the result. On thi |
| CAP-TMUX-017 | TUI | Terminal emulator detection and capabilities | pending |  |  |  |
| CAP-TMUX-018 | CLI | System clipboard copy (internal/clipboard) | pending |  |  |  |
| CAP-TMUX-018 | TUI | System clipboard copy (internal/clipboard) | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-018-CLI.txt | clipboard.Copy native chain verified available: platform.Detect on Linux selects wl-copy/xclip/xsel; xclip is present on |
| CAP-TMUX-019 | TUI | Pop-out to native terminal window (internal/terminal) | untestable-locally | 2026-06-11 | evidence/tmux/ev-CAP-TUI-help.txt | Pop-out 'Shift+Enter Open session in new iTerm window (macOS)' confirmed as a TUI binding. OpenSessionInNewWindow darwin |
| CAP-TMUX-020 | CLI | Waiting/readiness pollers | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-005-CLI.txt,evidence/tmux/ev-CAP-TMUX-002-CLI.txt | WaitForShellPrompt is exercised implicitly: Start waits for shell pane readiness before delivering the command, and sess |
| CAP-TMUX-021 | CLI | Test/host-safety guards | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-021-CLI.txt | assertTestTmuxIsolation is a no-op for the production (non-go-test) binary: every session start succeeded (exit 0) witho |
| CAP-TMUX-021 | TUI | Test/host-safety guards | pending |  |  |  |
| CAP-TMUX-002 | daemon |  | untestable-locally | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-002b-CLI.txt | 3-tier systemd-run launch fallback (service/scope/direct) not exercised: sandbox has no systemd user session, so direct  |
| CAP-TMUX-007 | daemon |  | verified | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-007-CLI.txt | With web (PipeManager) running, list-clients shows control_mode=1 client on the isolated socket — that is the ControlPip |
| CAP-TMUX-016 | daemon |  | partial | 2026-06-11 | evidence/tmux/ev-CAP-TMUX-016-CLI.txt | LogDir resolution under XDG data dir confirmed (data/agent-deck tree present). No .log files were created in this build' |

## CAP-STOR (20 caps, 48 rows) — partial:1, pending:28, untestable-locally:2, verified:17

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-STOR-001 | CLI | state.db open/connection model | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-002-CLI.txt | After 'ad add', state.db created under XDG data dir with parent dirs at 0700 (profiles/ and profiles/default/ both 0700, |
| CAP-STOR-001 | TUI | state.db open/connection model | pending |  |  |  |
| CAP-STOR-001 | WEB | state.db open/connection model | pending |  |  |  |
| CAP-STOR-002 | CLI | state.db schema + migrations (SchemaVersion=9) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-002-CLI.txt | metadata.schema_version=9. All 8 tables present: instances, groups, instance_heartbeats, recent_sessions, cost_events, w |
| CAP-STOR-002 | TUI | state.db schema + migrations (SchemaVersion=9) | pending |  |  |  |
| CAP-STOR-002 | WEB | state.db schema + migrations (SchemaVersion=9) | pending |  |  |  |
| CAP-STOR-003 | CLI | SaveInstances bulk save (the 2026-06-04 wipe site) + S1/S2 g | partial | 2026-06-11 | evidence/stor/ev-CAP-STOR-003-013-CLI.txt | SaveInstances bulk persistence verified: 3 added sessions survive across fresh CLI processes (ad list --json shows alpha |
| CAP-STOR-003 | TUI | SaveInstances bulk save (the 2026-06-04 wipe site) + S1/S2 g | pending |  |  |  |
| CAP-STOR-003 | WEB | SaveInstances bulk save (the 2026-06-04 wipe site) + S1/S2 g | pending |  |  |  |
| CAP-STOR-004 | CLI | Targeted single-column writes (no-clobber primitives) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-004-CLI.txt | Targeted single-column writes via CLI: 'session set-title-lock on/off' toggles title_locked col only; 'session set-trans |
| CAP-STOR-004 | TUI | Targeted single-column writes (no-clobber primitives) | pending |  |  |  |
| CAP-STOR-004 | WEB | Targeted single-column writes (no-clobber primitives) | pending |  |  |  |
| CAP-STOR-005 | CLI | withBusyRetry concurrency policy | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-005-CLI.txt | 5 parallel concurrent 'ad rm' via xargs -P5 against the same state.db: exactly 5 of 10 instances removed, correct surviv |
| CAP-STOR-005 | TUI | withBusyRetry concurrency policy | pending |  |  |  |
| CAP-STOR-005 | WEB | withBusyRetry concurrency policy | pending |  |  |  |
| CAP-STOR-006 | CLI | Groups persistence | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-006-011-CLI.txt | SaveGroups persistence: 'group create mobile' and 'group create ios --parent mobile' persist path/name/sort_order/max_co |
| CAP-STOR-006 | TUI | Groups persistence | pending |  |  |  |
| CAP-STOR-006 | WEB | Groups persistence | pending |  |  |  |
| CAP-STOR-007 | CLI | tool_data JSON blob model + extras preservation | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-007b-CLI.txt | tool_data JSON blob: 'session set color #ff8800' (color #391), claude-session-id (claude_session_id+claude_detected_at), |
| CAP-STOR-007 | TUI | tool_data JSON blob model + extras preservation | pending |  |  |  |
| CAP-STOR-007 | WEB | tool_data JSON blob model + extras preservation | pending |  |  |  |
| CAP-STOR-008 | CLI | Legacy sessions.json → SQLite migration | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-008-CLI.txt | Dropped a legacy sessions.json (camelCase projectPath/groupPath/sortOrder) in profiles/default/ with no state.db. 'ad li |
| CAP-STOR-008 | TUI | Legacy sessions.json → SQLite migration | pending |  |  |  |
| CAP-STOR-008 | WEB | Legacy sessions.json → SQLite migration | pending |  |  |  |
| CAP-STOR-009 | CLI | Cross-profile session migration primitives (issue #928) | untestable-locally | 2026-06-11 | evidence/stor/ev-CAP-STOR-012-CLI.txt | Cross-profile migrate_xfer primitives (InsertInstanceRow, LoadInstanceByID, profile_migrate.go) are compiled into the bi |
| CAP-STOR-010 | TUI | Heartbeats, primary election, change detection, metadata | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-010-TUI.txt | Launching the TUI registered a row in instance_heartbeats (pid, started, heartbeat timestamps) — RegisterInstance + Hear |
| CAP-STOR-011 | CLI | Recent sessions (deleted-session re-create picker) | pending |  |  |  |
| CAP-STOR-011 | TUI | Recent sessions (deleted-session re-create picker) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-011-dialog.txt | Drove TUI 'Delete Session?' dialog (cursor on session row) and confirmed with y. recent_sessions table got the row: solo |
| CAP-STOR-012 | CLI | Watchers + watcher_events store | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-012-CLI.txt | Watchers store via the independent 'ad watcher' DB-writer process: 'watcher create webhook' persists to watchers table ( |
| CAP-STOR-013 | CLI | Storage layer save/load orchestration + verify-loop race fix | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-013-CLI.txt | Storage orchestration: 'ad rm beta' does a targeted DeleteInstance + SaveGroupsOnly + verify loop, leaving alpha/gamma i |
| CAP-STOR-013 | TUI | Storage layer save/load orchestration + verify-loop race fix | pending |  |  |  |
| CAP-STOR-013 | WEB | Storage layer save/load orchestration + verify-loop race fix | pending |  |  |  |
| CAP-STOR-014 | CLI | Profile resolution (single source of truth, #881) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-014-CLI.txt | Profile resolution ladder driven end-to-end by which sessions each resolution sees: (1) -p flag (work profile isolates w |
| CAP-STOR-014 | TUI | Profile resolution (single source of truth, #881) | pending |  |  |  |
| CAP-STOR-014 | WEB | Profile resolution (single source of truth, #881) | pending |  |  |  |
| CAP-STOR-015 | CLI | Profile lifecycle + config.json (global pointer file) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-015-CLI.txt | Profile lifecycle: create/list (sorted)/default/delete. config.json schema {default_profile, version:1} written under XD |
| CAP-STOR-015 | TUI | Profile lifecycle + config.json (global pointer file) | pending |  |  |  |
| CAP-STOR-016 | CLI | XDG/HOME path resolution + legacy split (agentpaths) | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-016-CLI.txt | XDG/HOME resolution: absolute XDG_DATA_HOME honored (state.db under $XDG_DATA_HOME/agent-deck/...), no legacy ~/.agent-d |
| CAP-STOR-016 | TUI | XDG/HOME path resolution + legacy split (agentpaths) | pending |  |  |  |
| CAP-STOR-016 | WEB | XDG/HOME path resolution + legacy split (agentpaths) | pending |  |  |  |
| CAP-STOR-017 | CLI | Legacy → XDG layout migration (agentpaths.MigrateLegacyLayou | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-017-CLI.txt | 'migrate-paths': --dry-run reports planned copies (config.toml/config.json/skills->ConfigDir, profiles->DataDir, update- |
| CAP-STOR-018 | CLI | atomicfile: symlink-preserving atomic/durable writes | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-018-019-CLI.txt | Symlink-preserving atomic write: made config.toml a symlink to a real target, then 'remote add' triggered a config write |
| CAP-STOR-018 | TUI | atomicfile: symlink-preserving atomic/durable writes | pending |  |  |  |
| CAP-STOR-018 | WEB | atomicfile: symlink-preserving atomic/durable writes | pending |  |  |  |
| CAP-STOR-019 | CLI | config.toml model: load cache, save guards (S2/S3), panel me | verified | 2026-06-11 | evidence/stor/ev-CAP-STOR-019-CLI.txt | config.toml model: 'remote add'/'remote remove' do read-modify-write preserving existing [tmux]/[instances] sections and |
| CAP-STOR-019 | TUI | config.toml model: load cache, save guards (S2/S3), panel me | pending |  |  |  |
| CAP-STOR-019 | WEB | config.toml model: load cache, save guards (S2/S3), panel me | pending |  |  |  |
| CAP-STOR-020 | CLI | Test-time data-loss guards (S4/S5) + sandbox infrastructure | untestable-locally | 2026-06-11 | evidence/stor/ev-CAP-STOR-012-CLI.txt | S4 (agentpaths.ensureSafeForTest) and S5 (pathsafety guard_test) error strings ARE compiled into the production binary ( |

## CAP-MCP (15 caps, 32 rows) — broken:1, partial:1, pending:12, untestable-locally:2, verified:16

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-MCP-001 | CLI | MCP catalog definition in config.toml | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-001-CLI-list.txt, ev-CAP-MCP-001-config.txt | [mcps.<name>] catalog loads from XDG config.toml; mcp list shows transport tags [S]/[H]/[E], sorted names, has_server_co |
| CAP-MCP-001 | TUI | MCP catalog definition in config.toml | pending |  |  |  |
| CAP-MCP-001 | WEB | MCP catalog definition in config.toml | pending |  |  |  |
| CAP-MCP-002 | CLI | CLI: agent-deck mcp list / attached / attach / detach | untestable-locally | 2026-06-11 | evidence/mcp/ev-CAP-MCP-006-user-scope.txt | --restart sub-behavior (inst.Restart + 2s sleep + SendKeysAndEnter 'continue') requires a real claude/gemini agent proce |
| CAP-MCP-003 | CLI | CLI: agent-deck mcp server start/stop/status (HTTP MCP serve | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-003-CLI-server.txt, ev-CAP-MCP-010-CLI-httpstart.txt | server status lists all HTTP/SSE catalog MCPs with status + SERVER CONFIG column; fresh CLI shows pool_not_initialized.  |
| CAP-MCP-003 | TUI | CLI: agent-deck mcp server start/stop/status (HTTP MCP serve | pending |  |  |  |
| CAP-MCP-004 | CLI | Attached-state reads (MCPInfo) + caching | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-004-CLI-parentwalk.txt | Parent-dir walk confirmed: deepchild session at projA/sub/deep (no .mcp.json there) reports LOCAL context7+ssemcp = exac |
| CAP-MCP-004 | TUI | Attached-state reads (MCPInfo) + caching | pending |  |  |  |
| CAP-MCP-004 | WEB | Attached-state reads (MCPInfo) + caching | pending |  |  |  |
| CAP-MCP-005 | CLI | LOCAL scope write: .mcp.json generation with merge-preserve  | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-002-CLI-attach.txt, ev-CAP-MCP-002-scope-lie.txt | .mcp.json written mode 0644 (the documented secret-exposure flaw, env EXA_API_KEY in plaintext). stdio entry {type:stdio |
| CAP-MCP-005 | TUI | LOCAL scope write: .mcp.json generation with merge-preserve  | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-013-TUI-applied.txt, ev-CAP-MCP-009-live-dialog.txt | TUI Apply (LOCAL scope) writes .mcp.json via WriteMCPJsonFromConfig with merge-preserve; pool-live Apply emits agent-dec |
| CAP-MCP-005 | WEB | LOCAL scope write: .mcp.json generation with merge-preserve  | pending |  |  |  |
| CAP-MCP-006 | CLI | GLOBAL and USER scope writes (.claude.json) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-006-CLI-global.txt | GLOBAL: attach --global writes $CLAUDE_CONFIG_DIR/.claude.json (resolved to ~/.claude/.claude.json) mcpServers, mode 060 |
| CAP-MCP-006 | TUI | GLOBAL and USER scope writes (.claude.json) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-013-TUI-scope-global.txt, ev-CAP-MCP-013-TUI-mcpdialog.txt | Dialog exposes LOCAL/GLOBAL/USER scopes; GLOBAL 'Writes to: Claude config (profile-specific)', USER tab present (WriteUs |
| CAP-MCP-006 | WEB | GLOBAL and USER scope writes (.claude.json) | pending |  |  |  |
| CAP-MCP-007 | TUI | Socket pool: shared stdio MCP processes multiplexed over Uni | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-007-socketpool.txt, ev-CAP-MCP-011-systemd-scope.txt, ev-CAP-MCP-012-TUI-home.txt | Pool creates per-MCP unix socket at <data>/agent-deck/sockets/mcp-exa.sock (mode 0600, dir 0700), per-MCP stderr log at  |
| CAP-MCP-008 | CLI | agent-deck mcp-proxy <socket> (hidden CLI bridge command) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-008-CLI-mcpproxy.txt, ev-CAP-MCP-009-live-dialog.txt | Hidden 'agent-deck mcp-proxy <socket>' command: connects to unix socket and copies stdin->socket (server received exact  |
| CAP-MCP-009 | CLI | Pool resolution during config generation (tryPoolSocket) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-009-poolresolution.txt | CLI mode (pool==nil): pool enabled + exa pooled, but no live socket discoverable (socket-path drift: pool at <data>/sock |
| CAP-MCP-009 | TUI | Pool resolution during config generation (tryPoolSocket) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-009-live-dialog.txt | TUI process (pool live): tryPoolSocket resolves IsRunning -> emits socket entry {command:agent-deck,args:[mcp-proxy,<dat |
| CAP-MCP-009 | WEB | Pool resolution during config generation (tryPoolSocket) | pending |  |  |  |
| CAP-MCP-010 | CLI | HTTP MCP server pool (auto-start [mcps.X.server]) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-010-CLI-httpstart.txt, ev-CAP-MCP-011-systemd-scope.txt | server start autohttp with real python http.server + health_check=/: waitReady polled until healthy, exit 0 'Started HTT |
| CAP-MCP-010 | TUI | HTTP MCP server pool (auto-start [mcps.X.server]) | pending |  |  |  |
| CAP-MCP-010 | WEB | HTTP MCP server pool (auto-start [mcps.X.server]) | pending |  |  |  |
| CAP-MCP-011 | CLI | systemd per-MCP scope isolation (Linux) | pending |  |  |  |
| CAP-MCP-011 | TUI | systemd per-MCP scope isolation (Linux) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-011-systemd-scope.txt | systemd-run available; ISOLATION=1 wraps BOTH socket-proxy (cat) and HTTP (python http.server) children in transient sco |
| CAP-MCP-012 | TUI | Pool lifecycle: TUI init, multi-instance sharing, quit seman | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-012-TUI-home.txt, ev-CAP-MCP-012-TUI-quitdialog.txt | TUI startup eager-starts pool: mcp-exa.sock + exa_socket.log created (context7 excluded). Quit (q) with pool running ->  |
| CAP-MCP-013 | TUI | TUI MCP Manager dialog ("M" key) | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-013-TUI-mcpdialog.txt, ev-CAP-MCP-013-TUI-scope-global.txt, ev-CAP-MCP-013-TUI-applied.txt | 'm' key opens MCP Manager on a claude session: LOCAL/GLOBAL/USER scopes, Attached/Available two-column lists, [S]/[H]/[E |
| CAP-MCP-014 | WEB | Web API MCP management | pending |  |  |  |
| CAP-MCP-015 | CLI | Session-launch and restart MCP integration | verified | 2026-06-11 | evidence/mcp/ev-CAP-MCP-015-CLI-add-mcp.txt | add --mcp exa --mcp context7 -> LOCAL .mcp.json with both entries, exit 0. Unknown --mcp bogus -> exit 1 with available  |
| CAP-MCP-015 | TUI | Session-launch and restart MCP integration | untestable-locally | 2026-06-11 | evidence/mcp/ev-CAP-MCP-013-TUI-applied.txt | Restart-path MCP regeneration (regenerateMCPConfig swapping stdio<->socket), SkipMCPRegenerate flag, CaptureLoadedMCPs s |
| CAP-MCP-001 | internal |  | partial | 2026-06-11 | evidence/mcp/ev-CAP-MCP-001-manage-false.txt, ev-CAP-MCP-005-manage-false.txt | DEVIATION: contract says manage_mcp_json=false makes ALL .mcp.json writes silent no-ops, but both CLI 'mcp attach' AND ' |
| CAP-MCP-014 | web |  | broken | 2026-06-11 | evidence/mcp/ev-CAP-MCP-014-WEB.txt, ev-CAP-MCP-014-WEB-tui.txt | BROKEN: GET /api/mcps, GET/POST/DELETE/PATCH /api/sessions/{id}/mcps all return HTTP 503 {code:NOT_IMPLEMENTED, message: |

## CAP-COND (10 caps, 27 rows) — partial:1, pending:8, untestable-locally:5, verified:13

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-COND-001 | CLI | Conductor entity + meta.json store | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-001-meta.json | conductor setup writes ~/.agent-deck/conductor/<name>/meta.json with full schema (name, agent, profile normalized to 'de |
| CAP-COND-002 | COND | Conductor setup/teardown lifecycle (CLI orchestration) | pending |  |  |  |
| CAP-COND-003 | TUI | is_conductor column + session/group relationship | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-003-TUI-home.txt | TUI renders conductor group (3) pinned at top with conductor-gamma/epsilon/delta, and child1 visually nested under paren |
| CAP-COND-003 | WEB | is_conductor column + session/group relationship | pending |  |  |  |
| CAP-COND-004 | DAEMON/INTERNAL | Heartbeat daemon (OS-level per-conductor timer) | pending |  |  |  |
| CAP-COND-005 | DAEMON/INTERNAL | bridge.py legacy channel bridge (Telegram/Slack ↔ conductor  | pending |  |  |  |
| CAP-COND-006 | DAEMON/INTERNAL | Plugin-based Telegram channel integration (--channels) + pol | pending |  |  |  |
| CAP-COND-007 | COND | Per-conductor env_file injection and config_dir profile over | pending |  |  |  |
| CAP-COND-008 | CLI | Child→parent transition notification (transition daemon + no | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-008-drain.txt | notify-daemon --once runs exit 0 (one adaptive poll of all profiles). inbox drain --json <parent>: empty=[]; crafted fin |
| CAP-COND-009 | COND | Worker-asserted completion ([DONE] sentinel + run-task kerne | pending |  |  |  |
| CAP-COND-010 | CLI | Conductor instruction/policy template system | pending |  |  |  |
| CAP-COND-001 | Filesystem |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-001-meta.json | meta.json perms 0644 when no env/env_file (alpha), 0600 when env present (beta). IsConductorSetup = meta.json exists. co |
| CAP-COND-002 | CLI |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-002-setup.txt | setup: interactive Telegram/Slack/Discord prompts, saves [conductor] block (enabled=true, heartbeat_interval=15) to conf |
| CAP-COND-002 | Filesystem |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-002-teardown.txt | config.toml [conductor] block persisted with plaintext token after telegram configured. teardown --remove deletes conduc |
| CAP-COND-003 | internal |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-003-statedb.txt | statedb instances table has is_conductor INTEGER NOT NULL DEFAULT 0 (col 14) and parent_session_id TEXT (col 13). Both c |
| CAP-COND-004 | Filesystem |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-004-heartbeat.sh | setup --heartbeat writes heartbeat.sh from template: {NAME}=gamma, {PROFILE}=default, {HEARTBEAT_PREFIX}='[HEARTBEAT]',  |
| CAP-COND-004 | internal-daemon |  | untestable-locally | 2026-06-11 | evidence/cond/ev-CAP-COND-004-setup.txt | systemctl --user enable failed in sandbox (no user systemd bus): 'failed to enable heartbeat timer: exit status 1' handl |
| CAP-COND-005 | CLI |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-005-setup.txt | bridge.py (2010 lines, embedded conductorBridgePy) written to conductor/bridge.py ONLY when a channel is configured (abs |
| CAP-COND-005 | internal-daemon |  | untestable-locally | 2026-06-11 | evidence/cond/ev-CAP-COND-005-setup.txt | Live Telegram (aiogram polling) / Slack (socket mode) message routing, queue draining, and hook execution require real b |
| CAP-COND-006 | CLI |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-006-doctor2.txt | session set <claude-session> channels 'plugin:telegram@...' updates+persists (JSON metadata col {"channels":[...]}). Mut |
| CAP-COND-006 | internal |  | untestable-locally | 2026-06-11 | evidence/cond/ev-CAP-COND-006-doctor2.txt | Per-session scratch CLAUDE_CONFIG_DIR rewrite (enabledPlugins.telegram=true) and TELEGRAM_* env strip happen only on the |
| CAP-COND-007 | CLI |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-007-delta-meta.json | meta.json layer fully verified: setup -env KEY=VALUE (repeatable) and -env-file persist env (map) and env_file (absolute |
| CAP-COND-007 | internal |  | partial | 2026-06-11 | evidence/cond/ev-CAP-COND-007-injection.txt | Runtime spawn env injection (buildEnvSourceCommand sourcing meta.EnvFile + exporting meta.Env as highest-priority step 6 |
| CAP-COND-008 | internal-daemon |  | untestable-locally | 2026-06-11 | evidence/cond/ev-CAP-COND-008-notifydaemon.txt | transition-notifier systemd service written with RuntimeMaxSec=86400 recycle, Restart=always, logs to transition-notifie |
| CAP-COND-009 | CLI |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-009-runtask.txt | run-task wrapper: worker printing '===AGENTDECK_DONE=== status=ok summary=all good' -> durable CompletionRecord {status: |
| CAP-COND-010 | Filesystem |  | verified | 2026-06-11 | evidence/cond/ev-CAP-COND-010-templates.txt | Per-conductor instruction file rendered: alpha CLAUDE.md has {NAME}=alpha, {PROFILE}=default, {AGENT}=Claude Code substi |
| CAP-COND-010 | internal |  | untestable-locally | 2026-06-11 | evidence/cond/ev-CAP-COND-010-templates.txt | ClearOnCompact runtime behavior (blocking claude auto-compaction and sending /clear via instructions) requires driving t |

## CAP-WATCH (13 caps, 30 rows) — partial:4, pending:6, untestable-locally:2, verified:18

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-WATCH-001 | CLI | Watcher engine event pipeline | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-001-CLI.txt | watcher create webhook/ntfy/slack OK (exit 0); github without secret rejected (exit 1). list --json emits {name,type,sta |
| CAP-WATCH-001 | TUI | Watcher engine event pipeline | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-001-CLI-webhook-e2e.txt | FULL event pipeline e2e: TUI engine starts in-process (debug.log watcher_engine_started watcher_count:1); webhook adapte |
| CAP-WATCH-002 | TUI | Watcher source adapters (webhook, github, ntfy, slack, gmail | pending |  |  |  |
| CAP-WATCH-003 | TUI | Watcher health tracking + health alert bridge | partial | 2026-06-11 | evidence/watch/ev-CAP-WATCH-001-CLI-webhook-e2e.txt | HealthTracker observable: state.json snapshot persisted by writerLoop carries adapter_healthy:true and error_count:0; wa |
| CAP-WATCH-004 | CLI | Event routing (clients.json router) | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-004-CLI.txt | watcher routes (table) and --json read clients.json correctly: exact email keys and *@domain wildcard keys both shown wi |
| CAP-WATCH-004 | TUI | Event routing (clients.json router) | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-005-CLI.txt | Router.Match verified via 'watcher test': exact sender match -> 'Match: exact / Routes to conductor: <name> / Group: <gr |
| CAP-WATCH-005 | TUI | Triage pipeline (unrouted event → Claude classifier session) | partial | 2026-06-11 | evidence/watch/ev-CAP-WATCH-001-CLI-webhook-e2e.txt | Triage entry verified: a real unrouted webhook event persists with routed_to='triage' (confirmed in watcher status recen |
| CAP-WATCH-006 | CLI | Hook-to-status derivation (sessionstatus.Derive) | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-006-CLI.txt | sessionstatus.Derive output observed via 'ad status' (0 waiting / 0 running / 1 idle) and list --json status field. Bash |
| CAP-WATCH-006 | TUI | Hook-to-status derivation (sessionstatus.Derive) | pending |  |  |  |
| CAP-WATCH-006 | WEB | Hook-to-status derivation (sessionstatus.Derive) | pending |  |  |  |
| CAP-WATCH-007 | CLI | Cost event capture (Claude Stop hook → file drop → fsnotify  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-007-CLI.txt | agent-deck hook-handler (hidden subcmd) given a Stop payload {hook_event_name,session_id,transcript_path} + AGENTDECK_IN |
| CAP-WATCH-007 | TUI | Cost event capture (Claude Stop hook → file drop → fsnotify  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-007-TUI-ingested.txt | Full e2e ingestion: with TUI running, a hook-handler drop file is picked up by CostEventWatcher (fsnotify+100ms debounce |
| CAP-WATCH-008 | WATCH | Cost parsers + tmux capture poller (multi-tool cost extracti | pending |  |  |  |
| CAP-WATCH-009 | CLI | Cost store, summaries, remote aggregation | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-009-CLI.txt | costs summary (table: Today/Week/Month/Projected + event counts) and --json (microdollar keys cost_today/week/month/last |
| CAP-WATCH-009 | TUI | Cost store, summaries, remote aggregation | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-009-TUI-dashboard.txt | Cost Dashboard ($ key) renders Today/Week/Month/Projected, token breakdown (Input/Output/Cache R/W), Top Sessions and Co |
| CAP-WATCH-009 | WEB | Cost store, summaries, remote aggregation | pending |  |  |  |
| CAP-WATCH-010 | CLI | Pricing (defaults, cache, overrides, daily fetcher, recomput | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-010-CLI.txt | costs recompute --dry-run and live both exit 0 with 'Would update/Updated/Skipped (unknown model)' summary (idempotent). |
| CAP-WATCH-010 | TUI | Pricing (defaults, cache, overrides, daily fetcher, recomput | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-007-TUI-ingested.txt | Pricer resolution (hardcoded defaults -> ComputeCost) drives the live TUI cost display: claude-opus-4-7 priced at $0.09  |
| CAP-WATCH-011 | TUI | Budget limits (warn/stop) | partial | 2026-06-11 | evidence/watch/ev-CAP-WATCH-011-CLI.txt | BudgetConfig [costs.budgets] daily/weekly/monthly (and per-group daily_limit) parses cleanly from config.toml; costs sum |
| CAP-WATCH-012 | TUI | System stats collection + formatting | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-TUI-home.txt | sysinfo Collector renders the tmux/header status segment: '⚙ 0% │ ⛁ 19.3G/62.5G │ ▪ 1543.6G/1831.7G' (CPU first-sample 0 |
| CAP-WATCH-012 | WEB | System stats collection + formatting | pending |  |  |  |
| CAP-WATCH-013 | CLI | Credential keep-warm refresh daemon (creds-refresh) | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-013-CLI.txt | creds-refresh --once full lifecycle: (a) no .credentials.json dir -> error exit 1 (contract: only non-zero on no-config- |
| CAP-WATCH-002 | CLI |  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-002-CLI.txt | github adapter create requires secret: via $GITHUB_WEBHOOK_SECRET env OR --secret-file (chmod 600); secret persisted to  |
| CAP-WATCH-002 | daemon |  | partial | 2026-06-11 | evidence/watch/ev-CAP-WATCH-002-CLI.txt | WebhookAdapter and GitHubAdapter create+persist verified. ntfy/slack create OK. Gmail adapter: per contract v2 notes it  |
| CAP-WATCH-005 | CLI |  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-005-CLI.txt | watcher test drives synthetic event through router: unrouted -> 'would go to triage'; routed -> resolves conductor/group |
| CAP-WATCH-006 | web |  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-web-endpoints.txt | Web read path (snapshot_hook_refresh, AllowStaleWaiting=true) exercised via GET /api/sessions returning {sessions,groups |
| CAP-WATCH-008 | none |  | untestable-locally | 2026-06-11 | evidence/watch/ev-CAP-WATCH-007-CLI.txt | Cost parsers (gemini/openai/minimax) + CostPoller tmux capture-pane polling are DEAD CODE at runtime per spec (NewCostPo |
| CAP-WATCH-009 | web |  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-009-WEB.txt | GET /api/costs/summary returns {today_usd,week_usd,month_usd,projected_usd,*_events} 200; /api/costs/daily -> [] 200; /a |
| CAP-WATCH-012 | web |  | verified | 2026-06-11 | evidence/watch/ev-CAP-WATCH-012-WEB.txt | GET /api/system/stats -> 200 with cpu{usage_percent}, disk{total/used bytes+human, usage_percent}, load{load1/5/15 from  |
| CAP-WATCH-013 | daemon |  | untestable-locally | 2026-06-11 | evidence/watch/ev-CAP-WATCH-013-CLI.txt | Daemon loop (Run() immediate tick + every 25m interval, systemd --user unit) and the real lock-dir contention against li |

## CAP-MISC (19 caps, 34 rows) — partial:3, pending:14, untestable-locally:6, verified:11

| CAP-ID | Surface | Capability | Status | Verified | Evidence | Notes |
|---|---|---|---|---|---|---|
| CAP-MISC-001 | CLI | Docker container lifecycle (sandbox sessions) | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-001-CLI.txt | Drove real Docker against running daemon (29.3.0). add --sandbox + session start created container agent-deck-<title>-<i |
| CAP-MISC-001 | TUI | Docker container lifecycle (sandbox sessions) | pending |  |  |  |
| CAP-MISC-002 | CLI | Sandbox container config builder + mount security blocklists | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-002-CLI.txt | docker inspect of the real sandbox container confirmed the full security contract: ReadonlyRootfs=true, CapDrop=[ALL], S |
| CAP-MISC-002 | TUI | Sandbox container config builder + mount security blocklists | pending |  |  |  |
| CAP-MISC-003 | DAEMON/INTERNAL | Agent-config sandbox sync (host credentials/config → contain | pending |  |  |  |
| CAP-MISC-004 | CLI | Docker availability detection | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-004-CLI.txt | With docker removed from PATH, session start of a sandbox session failed with the exact sentinel 'docker CLI is not inst |
| CAP-MISC-004 | TUI | Docker availability detection | pending |  |  |  |
| CAP-MISC-005 | CLI | Self-update: version check, cache, nudge | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-005-CLI.txt | update --check hit GitHub, computed latest=1.9.54 vs current 1.9.56-verify -> 'running the latest version' (CompareVersi |
| CAP-MISC-005 | TUI | Self-update: version check, cache, nudge | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-005-TUI-settings.txt | TUI home banner shows v1.9.56-verify (annotation slot); no nudge bar since current>=latest (ShouldNudge false). Settings |
| CAP-MISC-006 | CLI | Self-update: verified download + binary install + remote dep | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-006-CLI.txt | update --version 1.9.54 (run on a sandbox COPY of the binary, not the shared $AD_BIN): fetched release v1.9.54, correctl |
| CAP-MISC-007 | CLI | install.sh (curl\|bash installer) | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-007-note.txt | install.sh is a curl\|bash bootstrap with no front door in the shipped binary. Running it would mutate the real system ( |
| CAP-MISC-008 | CLI | Makefile build/test/release pipeline | pending |  |  |  |
| CAP-MISC-009 | CLI | OpenClaw gateway client (WebSocket JSON-RPC) | partial | 2026-06-11 | evidence/misc/ev-CAP-MISC-009-CLI.txt | OpenClaw client drives a real WebSocket dial to the default loopback gateway ws://127.0.0.1:31337. status -> 'Gateway: O |
| CAP-MISC-009 | TUI | OpenClaw gateway client (WebSocket JSON-RPC) | pending |  |  |  |
| CAP-MISC-010 | CLI | OpenClaw bridge TUI + agent session sync | pending |  |  |  |
| CAP-MISC-010 | TUI | OpenClaw bridge TUI + agent session sync | partial | 2026-06-11 | evidence/misc/ev-CAP-MISC-010-TUI.txt | openclaw bridge --agent testagent launched the full bubbletea chat TUI in tmux: header 'openclaw > testagent', [DISCONNE |
| CAP-MISC-011 | CLI | Feedback prompt pacing + state | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-011-CLI.txt | agent-deck feedback drives the rating prompt (1-5, n=never-again, q=quit) with input validation. --help documents the pa |
| CAP-MISC-011 | TUI | Feedback prompt pacing + state | pending |  |  |  |
| CAP-MISC-012 | CLI | Feedback sender (three-tier submission) | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-012-CLI.txt | Feedback sender front door: rating 5 -> comment -> disclosure block showing public URL discussions/600, gh-CLI submissio |
| CAP-MISC-012 | TUI | Feedback sender (three-tier submission) | pending |  |  |  |
| CAP-MISC-013 | CLI | Structured logging system | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-013-CLI.txt | agent-deck debug-dump wrote <XDG cache>/agent-deck/debug-dump-<unix>.jsonl and reported the path ('Share this file when  |
| CAP-MISC-014 | TUI | safego panic-recovering goroutines | pending |  |  |  |
| CAP-MISC-015 | MISC | Platform detection + headless + fsnotify support check | pending |  |  |  |
| CAP-MISC-016 | CLI | Experiments / `agent-deck try` (quick experiment folders) | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-016-CLI.txt | agent-deck try fully verified: try --list (empty -> message); try redis-cache --no-session created 2026-06-11-redis-cach |
| CAP-MISC-017 | DAEMON/INTERNAL | Test infrastructure: testutil isolation + perf + seams | pending |  |  |  |
| CAP-MISC-018 | DAEMON/INTERNAL | Integration test suite (real tmux) | pending |  |  |  |
| CAP-MISC-019 | DAEMON/INTERNAL | Release regression manifest + release-pinning tests | pending |  |  |  |
| CAP-MISC-003 | daemon/session |  | verified | 2026-06-11 | evidence/misc/ev-CAP-MISC-003-CLI.txt | On sandbox session start, RefreshAgentConfigs ran: created ~/.claude/sandbox (0700 shared dir), copied top-level host fi |
| CAP-MISC-008 | developer-CLI |  | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-008-note.txt | Makefile build/test/release pipeline is a developer/CI surface (make build/css/test/release-local), not a runtime featur |
| CAP-MISC-014 | internal |  | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-014-note.txt | safego.Go is an internal panic-recovery library with no CLI/TUI/web front door. Its effect (a background goroutine panic |
| CAP-MISC-015 | internal |  | partial | 2026-06-11 | evidence/misc/ev-CAP-MISC-015-CLI.txt | platform.Detect/IsHeadless/SupportsUnixSockets/CheckFsnotifySupport is an internal library consumed by clipboard choice, |
| CAP-MISC-017 | test-only |  | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-017-note.txt | testutil isolation/perf/seams (IsolateHome, IsolateTmuxSocket, testmain_audit, perfbudget, fakeclock/fakeinotify/logasse |
| CAP-MISC-018 | test-only |  | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-018-note.txt | internal/integration real-tmux suite is test-only (go test ./internal/integration). No source tree available in this env |
| CAP-MISC-019 | test-only |  | untestable-locally | 2026-06-11 | evidence/misc/ev-CAP-MISC-019-note.txt | internal/releasetests regression manifest + release-pinning tests (manifest_test, issue1146_lefthook, issue1206_install_ |

