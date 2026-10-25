# Architecture

How Agent Deck is put together: the processes, the packages, where data
lives on disk, and where to start reading for a change. Everything here was
checked against the code; when the code and this page disagree, the code
wins and this page needs a fix.

One Go module (`github.com/asheshgoplani/agent-deck`), one shipped binary
(`cmd/agent-deck`). Every process below is that same binary started with a
different subcommand.

## 1. Processes and how they talk

```
          you                       Mac app (separate repo)        phone/browser
           |                          |  CLI --json, events follow       |
           v                          v                                  v
  +-------------------+     +-------------------+          +-----------------------+
  | TUI  agent-deck   |     | CLI  agent-deck X |          | web  agent-deck web   |
  | internal/ui       |     | cmd/agent-deck    |          | internal/web (+ TUI   |
  +---------+---------+     +---------+---------+          | model in same proc)   |
            |                         |                    +-----------+-----------+
            +-----------+-------------+-------------+------------------+
                        |                           |
                        v                           v
          per-profile state.db (SQLite)       tmux server(s)  <-- the session substrate
          hooks/, inboxes/, bus/, comms/      agentdeck_* sessions running claude,
                        ^                     codex, gemini, opencode, pi, shell...
                        |                           |
          +-------------+--------------+            | harness hooks run
          | notify-daemon              |            v `agent-deck hook-handler`
          | session.TransitionDaemon   |      writes hooks/<instance-id>.json
          +----------------------------+

  ssh remotes: local TUI <== one ssh channel ==> `agent-deck remote-agent` on the remote host
```

**tmux is the substrate.** Every session is a tmux session named
`agentdeck_...` (`tmux.SessionPrefix`, `internal/tmux/tmux.go`). The agent
(Claude Code, Codex, ...) runs inside the pane. Agent Deck reads panes with
`capture-pane` (through a control-mode pipe when one is connected,
`internal/tmux/pipemanager.go`) and types with `send-keys`. `[tmux]
socket_name` in config.toml picks a dedicated tmux server; empty means the
default server.

**TUI** (`agent-deck` with no subcommand). Bubble Tea app in `internal/ui`;
the main model is `Home` in `internal/ui/home.go`. It loads sessions from
state.db, polls tmux for status, watches hook files with fsnotify
(`internal/session/hook_watcher.go`) and reloads when another process
commits to state.db (`internal/ui/storage_watcher.go`). It also runs the
profile's watcher engine (`internal/watcher`) unless another process owns it.

**CLI** (`agent-deck <command>`). `cmd/agent-deck/main.go` dispatches on
`args[0]` with a plain `switch` (no cobra); each command parses its own
flags with the standard `flag` package, in `cmd/agent-deck/<name>_cmd.go`
or, for `add`, `list`, `remove`, `rename`, `status`, `update` and
`uninstall`, in `main.go` (grep `flag.NewFlagSet("<name>"`).
The CLI opens state.db and tmux directly, the same as the TUI. Five
commands (`session start|stop|restart`, `list`, `group list`) also go
through the typed registry in `internal/core` (docs/core-registry.md).

**Web** (`agent-deck web`, default `127.0.0.1:8420`). `internal/web` serves
the web UI, a JSON API and a terminal websocket (`/ws/session/<id>`). It
runs in the same process as the TUI `Home` model: mutations go through
`web.SessionMutator`, implemented by `ui.WebMutator`
(`internal/ui/web_mutator.go`). `--no-tui` skips Bubble Tea but still uses
that model. `cmd/agent-deck-test-server` is a test-only server entry point.

**notify-daemon** (`agent-deck notify-daemon`). Runs
`session.TransitionDaemon` (`internal/session/transition_daemon.go`): it
polls sessions, turns status changes into parent notifications (writes
`inboxes/`), wakes conductors, raises desktop notifications, writes the comms
ledger, drains remote talkback and starts the recall backfill. `conductor
setup` installs it as a systemd user unit or launchd agent
(`session.InstallTransitionNotifierDaemon`, `internal/session/conductor.go`).

**Hook handler** (`agent-deck hook-handler`, plus `codex-notify`). Short
processes the harness itself starts on its hook events. They write
`hooks/<instance-id>.json`, which the TUI and the daemon read as the
freshest status signal (docs/status-detection.md). Installed by `hooks`,
`codex-hooks`, `gemini-hooks`, `pi-hooks`, `hermes-hooks`, `cursor-hooks`.

**Core daemon** (`agent-deck daemon serve`). Optional. Serves the command
registry and the event bus on a 0600 unix socket; the CLI only uses it when
`[core] daemon = true` (docs/daemon-protocol.md).

**Remotes.** A remote is an ssh host listed under `[remotes.<name>]`. The
local TUI keeps one ssh session per remote to `agent-deck remote-agent`
(`cmd/agent-deck/remote_agent_cmd.go`), which answers JSON-line requests by
running the same binary and pushes `{"event":"changed"}` when the remote's
state.db changes. Local side: `internal/session/ssh.go` and
`internal/session/remote_*.go` (`remote_channel.go` is the channel). When
the channel is down, each command falls back to one `ssh host agent-deck
...` exec. See docs/REMOTE-COMMANDS.md and docs/remote-cli-contract.md.

**Mac app.** Lives outside this repo. It drives the CLI with `--json` and
follows the per-profile event bus (`agent-deck events follow`).
`internal/macapp` holds only the Browser-panel wire contract; `macapp.*`
event kinds are in `internal/events/macapp_kinds.go`. Reference:
docs/macapp-core.md.

**Conductors.** Long-lived agent sessions that manage other sessions. The
Telegram/Slack/Discord/Mattermost bridge is a Python script embedded from
`internal/session/conductor_bridge.py`; `conductor/` holds its setup
scripts and templates.

## 2. Package map

`go list ./...` gives 100 packages. `cmd/agent-deck` links 72 `internal/`
packages. Packages marked *(test-only)* are not linked into the binary.

| Package | What it is |
|---|---|
| `agentpaths` | XDG and legacy (`~/.agent-deck`) path resolution and migration; refuses real-home paths under `go test` |
| `agents` | Portable role/post definitions (`agent-deck agents`) |
| `atomicfile` | Symlink-preserving atomic writes for user config files |
| `childenv` | Environment for child processes agent-deck spawns |
| `clipboard` | Copy to the system clipboard, OSC 52 fallback |
| `comms` | Comms Ledger: append-only per-profile message log with consumer cursors |
| `core`, `core/daemon`, `core/schema` | Typed command registry, its unix-socket daemon, JSON Schema from Go types |
| `costs` | Token cost store, pricing, budgets, dashboards |
| `ctxinspect` (+ `claude`, `codex`, `ctxtext`, `registry`, `sessionhost`, `verify`) | Context-window inspector: what fills a session's context before turn one |
| `desknotify` | Desktop notification when a session needs you |
| `docker` | Container lifecycle for sandboxed sessions |
| `events` | The per-profile event bus (append log, cursors, subscribe) |
| `experiments` | Experiment folders behind `agent-deck try` |
| `feedback` | In-app feedback sender and its state |
| `filelinks` | Serves `file://` links from the web terminal |
| `fleet` | Recovery when every managed tmux pane died at once |
| `fswatch` | Bounded directory watches on kqueue platforms |
| `git`, `jujutsu`, `vcs`, `vcsbackend` | Worktree operations for git and jj, the shared interface, backend detection |
| `harness` | Static table of harnesses: binary, install/login commands, docs |
| `health` | Process-local runtime observations (`agent-deck health`) |
| `intervalhook` | User shell commands on a timer while the TUI runs |
| `logging` | slog setup, `debug.log`, ring buffer, event aggregation |
| `macapp` | Mac app Browser-panel wire contract |
| `mcppool` | Shared MCP server pool (socket proxy and HTTP) |
| `menudata` | Menu snapshot DTOs, `BuildMenuSnapshot`, the in-memory menu store and the `SessionMutator` contract shared by `web` and `ui` (leaf: imports only `session`) |
| `openclaw` | OpenClaw gateway client and bridge TUI (`agent-deck openclaw`) |
| `platform` | OS / WSL detection |
| `procfd`, `procowner` | Open files of a process; durable process-ownership receipts |
| `quota` | Subscription allowance left, from the provider's numbers (`limits`) |
| `recall` (+ `cards`, `classify`, `enrich`, `ingest`, `mcp`, `query`, `reader`, `store`) | recall.db: cross-harness conversation index, its readers, writer, query side and MCP server (docs/recall.md) |
| `safego`, `safeio` | Panic-safe goroutines; never-lose-user-data file primitives |
| `sandboxguard` | Stops a test from resolving a path in the real home |
| `selfheal` | Deterministic self-heal policy |
| `send`, `sendqueue`, `slashcmd` | Prompt detection and send verification; durable `session send --queue` outbox; slash commands into Claude panes |
| `session` | The hub: `Instance`, storage, config (`userconfig.go`), groups, conductors, hooks, inbox, remotes, notify daemon. ~281 non-test files |
| `sessionstatus` | Single hook-to-status derivation shared by every surface |
| `shellwords`, `termtext`, `termreply`, `tmuxutf8` | Shell tokenizing; safe terminal text; filtering terminal replies from input; tmux `-u` decision |
| `statedb` | SQLite `state.db`: schema, migrations, queries |
| `sysinfo` | CPU, memory, disk, GPU, network stats |
| `telemetry` | Opt-in anonymous usage events (TELEMETRY.md) |
| `terminal` | Open a new terminal window attached to a tmux session |
| `tmux` | Everything tmux: sessions, capture, send-keys, status from pane frames, control pipes |
| `triggers` | Read model for `agent-deck triggers` |
| `ui` | Bubble Tea TUI |
| `update` | Version check and self-update |
| `watcher` | Watcher engine and adapters (webhook, ntfy, GitHub, Slack, Gmail) |
| `web` | HTTP server, JSON API, websocket terminal, embedded static UI |
| `integration`, `pathsafety`, `releasetests`, `testutil/*`, `tuitest`, `ctxinspect/ctxfixture`, `recall/testcorpus` | Test suites, test helpers, recorded and synthetic transcripts *(test-only)* |

Known oddities, stated plainly:

- The menu snapshot types and the `SessionMutator` contract live in the leaf
  package `internal/menudata`, so `internal/ui` does not import `internal/web`;
  `internal/web` keeps type aliases for them (`menudata_alias.go`). Only one ui
  test still imports web.
- `internal/session` is a hub: about 112k lines and 40 internal imports,
  including features such as `update` and `recall/*`. Most changes touch it.
- The four biggest files were split by topic into same-package files:
  `internal/ui/home*.go`, `internal/session/instance*.go`,
  `internal/tmux/tmux*.go`, `cmd/agent-deck/session_cmd.go` with `session_*_cmd.go` and
  `session_send_*.go`. A few large functions stay in the original file
  because source-level tests read them from it by name (`Home.updateInner`
  and `Home.handleMainKey` in `home.go`, `Instance.Start` and
  `Instance.updateStatus` in `instance.go`, `handleSessionFork` in
  `session_cmd.go`). Search by function name.
- Some package doc comments sit in an arbitrary file and describe that file,
  not the package (`session`, `tmux`, `web`, `watcher`); trust the table above.
- `*_test_helper.go` files in `session` and `tmux` are not `_test.go` files,
  so they ship in the binary.

## 3. Data on disk

`D` below is the data root:

- `$XDG_DATA_HOME/agent-deck`, default `~/.local/share/agent-deck`, or
- the legacy `~/.agent-deck`.

For most paths `agentpaths.EffectiveDataPath` uses XDG when the item exists
there, else legacy when it exists there, else XDG. The profile stores use a
stricter rule (`internal/session/store_root.go`): if only one root has
`profiles/`, it wins; if neither does, XDG. If both do, an XDG
`profiles/.active-root` marker (written by `agent-deck migrate-paths`) pins
XDG, an empty store never wins over a populated one, and with both populated
and no marker legacy wins (WARN; run `migrate-paths`).

| What | Path | Code |
|---|---|---|
| Sessions, groups, watchers, costs, triggers | `D/profiles/<profile>/state.db` (SQLite, WAL) | `session.GetDBPathForProfile`, `internal/statedb`, docs/internal/state-db-schema.md |
| Send queue | `D/profiles/<profile>/sendqueue/` | `internal/sendqueue` |
| User config | `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/`), or `~/.agent-deck/config.toml` if only that exists | `session.GetUserConfigPath`, `internal/session/userconfig.go` |
| Global profile config | `config.json` beside config.toml, same rule | `session.GetConfigPath` |
| Hook status | `D/hooks/<instance-id>.json` | `session.GetHooksDir` |
| Parent inbox (the durable per-parent outbox children write to) | `D/inboxes/<parent-id>.jsonl`, dead letters in `D/inboxes/dead-letter/` | `internal/session/inbox.go`, `inbox_outbox.go` |
| Conductor to human outbox | `D/runtime/human-outbox/<conductor>.jsonl` | `internal/session/human_outbox.go` |
| Event bus | `D/bus/<profile>/active.ndjson`, sealed segments, `writer.lock` | `internal/events`, docs/events.md |
| Comms ledger | `D/comms/<profile>/` | `internal/comms` |
| Core daemon socket | `D/runtime/profiles/<profile>/daemon.sock`, `daemon.lock` | `internal/core/daemon` |
| Recall index | `D/recall.db`, sidecars in `D/recall/` (machine-wide, not per profile) | `internal/recall/paths.go` |
| Logs | `$XDG_CACHE_HOME/agent-deck/debug.log` (default `~/.cache/agent-deck/`); other logs in `D/logs/` | `cmd/agent-deck/main.go`, `internal/logging` |

Agent transcripts belong to the harnesses. Agent Deck reads them (recall,
last response, context inspector) and does not own them. Roots as
`internal/session/recall_roots.go` and `internal/recall/reader` compute them:

| Harness | Location |
|---|---|
| Claude Code | `<config dir>/projects/<encoded project path>/<session-id>.jsonl`, where every char outside `[A-Za-z0-9-]` becomes `-` (`ConvertToClaudeDirName`). Config dir, first match wins: the session's account, conductor, group, `CLAUDE_CONFIG_DIR`, profile, global `[claude] config_dir`, else `~/.claude` (`resolveClaudeConfigDir` in `internal/session/claude.go`) |
| Codex | Every configured `[codex] config_dir` (profile and global), `$CODEX_HOME` and `~/.codex`; under each, `sessions/YYYY/MM/DD/rollout-*.jsonl` and `archived_sessions/` |
| Gemini | `~/.gemini/tmp/<project hash>/chats/session-*.json` |
| OpenCode | `$XDG_DATA_HOME/opencode/storage` or `~/.local/share/opencode/storage` |
| Pi | `~/.pi` and the parent of `PI_CODING_AGENT_DIR`; under each, `agent/sessions/` and `agent-deck/` |
| Hermes | `~/.hermes/state.db` |

Tests must never resolve any of these under a real home: `agentpaths` and
`internal/sandboxguard` fail the test if they do.

## 4. Where to start reading

| Change | Start here |
|---|---|
| Add a flag to a command | The command's `flag.NewFlagSet`: in `cmd/agent-deck/<name>_cmd.go` (`session_cmd.go` dispatches `session ...`; the subcommands are in `session_*_cmd.go`), or in `main.go` for `add`/`list`/`remove`/`rename`/`status`/`update`/`uninstall` (grep `flag.NewFlagSet("<name>"`). For a new top-level command, also the `switch` and `commandRegistry` in `main.go`, `printHelp` (`main.go`), `completionTree` (`completion_cmd.go`), `telemetry_features.go` |
| Add a config key | The struct with the `toml:"..."` tag in `internal/session/userconfig.go`; `agent-deck config schema` lists settable keys (`cmd/agent-deck/config_cmd.go`); user docs in `skills/agent-deck/references/config-reference.md` |
| Add a tool/harness | `builtinTools()` in `internal/session/builtins.go` (name, detection), `internal/harness/table.go` (install facts), command building in `internal/session/instance_cmd_build.go`, status patterns in `internal/tmux` (`patterns.go`, `frame_classify.go`), hooks installer if it has hooks (`internal/session/*_hooks.go`, `cmd/agent-deck/*_hooks_cmd.go`), transcript reader in `internal/recall/reader` |
| Change status detection | docs/status-detection.md first. Pane layer: `Session.GetStatus` in `internal/tmux/tmux_status.go`, `tmux.ClassifyPaneFrame`; instance layer: `Instance.updateStatus` in `internal/session/instance.go`; hook layer: `cmd/agent-deck/hook_handler.go`, `internal/sessionstatus`. Golden corpus: `internal/tmux/testdata/status_corpus` |
| Add or change a TUI key | `internal/ui/hotkeys.go` (action names, user rebinding via `[hotkeys]`), `Home.handleMainKey` in `internal/ui/home.go`, help overlay in `internal/ui/help.go` |
| Add a web endpoint | Core routes in `NewServer` in `internal/web/server.go`; feature routes register from `handlers_<feature>.go` via `registerFeatureRoutes` (`routes.go`). Mutations go through `SessionMutator` (`internal/menudata/mutator.go`, aliased in `internal/web`) and `ui.WebMutator` |
| Change remote behaviour | `internal/session/ssh.go`, `internal/session/remote_channel.go` and the other `remote_*.go`; CLI side `cmd/agent-deck/remote_cmd.go`, `remote_agent_cmd.go`, `remote_exec.go`; TUI side `internal/ui/remote_*.go` |
| Notifications to parents/conductors | `internal/session/transition_daemon.go`, `inbox*.go`, `cmd/agent-deck/notify_daemon_cmd.go` |
| Paths and storage | `internal/agentpaths`, `internal/session/store_root.go`, `config.go`, `storage.go`, `internal/statedb` |

## 5. Repository layout

| Path | What it holds |
|---|---|
| `cmd/agent-deck/` | The binary: `main.go` (dispatch plus several commands) and `<name>_cmd.go` files for the rest |
| `cmd/agent-deck-test-server/` | Web server without the TUI, for browser tests; not released |
| `internal/` | All library code (section 2) |
| `docs/` | Design and reference docs; this is the docs home. `docs/diagrams/` has d2 sources and SVGs |
| `documentation/` | User guides linked from README (watchers, watchdog, skills, harness identity) plus two internal notes; new docs go in `docs/` |
| `skills/` | Agent skills shipped with the plugin (`.claude-plugin/marketplace.json`); `skills/agent-deck/references/` has the CLI and config references |
| `conductor/` | Conductor setup/teardown scripts, launchd plist, templates |
| `scripts/` | Release checks, CI helpers, verification scripts, `check-fix-has-test` |
| `tests/` | Suites outside `go test ./internal/...`: `capability` (Go E2E), `e2e` (Playwright web), `eval`, `web`, `comms_matrix`, `ci` (workflow tests), `lighthouse` |
| `tools/` | Dev commands: `bench`, `capability-report`, `dist-local`, `funccheck`, `replay2469`, `visualcheck` |
| `bench/` | Synthetic fleet benchmark harness and reports |
| `sandbox/` | Dockerfile of the container image for sandboxed sessions |
| `assets/` | Watcher templates |
| `site/` | Static landing page (published by `.github/workflows/pages.yml`) |
| `demos/` | Demo GIFs/videos and their VHS `.tape` sources |
| `bin/` | Gitignored; only `stress-matrix.sh` is tracked. `make build` writes to `build/` |
| `tasks/` | Tracked working notes (`todo.md`, `lessons.md`) left from one branch; not project docs |
| `.github/` | Workflows, PR/issue templates, intake rules (`INTAKE.md`) |
| `.claude/` | Maintainer Claude Code commands and plans |
| Root files | `Makefile`, `.goreleaser.yml`, `lefthook.yml`, `.golangci.yml`, `install.sh`, `uninstall.sh`, `CHANGELOG.md`, `CONTRIBUTING.md`, `SECURITY.md`, `TELEMETRY.md`, `llms.txt`, `llms-full.txt` |
