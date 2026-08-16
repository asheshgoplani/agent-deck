# CLI Command Reference

Complete reference for all agent-deck CLI commands.

## Table of Contents

- [Global Options](#global-options)
- [Basic Commands](#basic-commands)
- [Web Command](#web-command)
- [Session Commands](#session-commands)
- [Fleet Recovery Commands](#fleet-recovery-commands)
- [Worktree Commands](#worktree-commands)
- [MCP Commands](#mcp-commands)
- [Skill Commands](#skill-commands)
- [Group Commands](#group-commands)
- [Profile Commands](#profile-commands)
- [Remote Commands](#remote-commands)
- [Codex Hook Commands](#codex-hook-commands)
- [DeepSeek Commands](#deepseek-commands)
- [Conductor Commands](#conductor-commands)

## Global Options

```bash
-p, --profile <name>    Use specific profile
--json                  JSON output
-q, --quiet             Minimal output
```

## Basic Commands

### add - Create session

```bash
agent-deck add [path] [options]
```

| Flag | Description |
|------|-------------|
| `-t, --title` | Session title |
| `-g, --group` | Group path |
| `-c, --cmd` | Tool/command (claude, gemini, opencode, codex, custom) |
| `--wrapper` | Wrapper command; use `{command}` placeholder |
| `--parent` | Parent session (creates child) |
| `--no-parent` | Disable automatic parent linking |
| `--mcp` | Attach MCP (repeatable) |
| `--attach` | Start and attach to the session immediately after creating it (requires an interactive terminal; not supported with `--ssh`/`--json`) |
| `--ssh <user@host>` | Run the session over SSH; this is a destination, not a registered remote name |
| `--remote-path <absolute-path>` | Working directory on the SSH host; an absolute positional path with `--ssh` is equivalent |

```bash
agent-deck add -t "My Project" -c claude .
agent-deck add -t "Child" --parent "Parent" -c claude /tmp/x
agent-deck add -g ard --parent "conductor-ard" -c claude .
agent-deck add -c "codex --dangerously-bypass-approvals-and-sandbox" .
agent-deck add -t "Research" -c claude --mcp exa --mcp firecrawl /tmp/r
agent-deck add -t "Quick" -c claude --attach .   # create → start → drop into the pane
```

Notes:
- Parent auto-link is enabled by default when `AGENT_DECK_SESSION_ID` is present and neither `--parent` nor `--no-parent` is passed.
- `--attach` does create → start → attach in one step. Without an interactive terminal (or with `--json`) it exits non-zero with a clear error, leaving the session created and started so you can attach later.
- `--parent` and `--no-parent` are mutually exclusive.
- Explicit `-g/--group` overrides inherited parent group.
- If `--cmd` contains extra args and no explicit `--wrapper` is provided, agent-deck auto-generates a wrapper to preserve those args.
- SSH session identity is the SSH destination plus remote working directory. Configure key selection with `IdentityFile`/`Host` in `~/.ssh/config` or use `ssh-agent`; agent-deck has no private-key flag.

### launch - Create + start (+ optional message)

```bash
agent-deck launch [path] [options]
```

Examples:

```bash
agent-deck launch . -c claude -m "Review this module"
agent-deck launch . -c claude --account work -m "Review this module"
agent-deck launch . -g ard -c claude -m "Review dataset"
agent-deck launch . -c "codex --dangerously-bypass-approvals-and-sandbox"
agent-deck launch -g book-keeper -c claude   # no path: lands on the group's default_path
```

Notes:
- `[path]` omitted: resolves the target group's `default_path`, then the global `default_path` config key, then cwd — the same chain as `add` (#1303). An explicit `.` always means the current directory.
- `--account <name>` selects a named slot from `[profiles.<name>.claude].config_dir` for this session, matching `add --account`.
- `--account` requires an explicit name. If the next token is another launch flag, launch stops with an error before resolving a fallback account or creating a session; use `--account=<name>` when a name intentionally begins with a dash.

### accounts - List named account slots

```bash
agent-deck accounts [--json]
```

Lists profiles that configure a Claude `config_dir`; these names are accepted by `add --account` and `launch --account`.

### list - List sessions

```bash
agent-deck list [--json] [--all]
agent-deck ls  # Alias
```

### remove - Remove session

```bash
agent-deck remove <id|title>
agent-deck rm  # Alias
```

### status - Status summary

```bash
agent-deck status [-v|-q|--json]
```

- Default: `2 waiting - 5 running - 3 idle`
- `-v`: Detailed list by status
- `-q`: Just waiting count (for scripts)

### migrate-paths - Copy legacy data into XDG layout

```bash
agent-deck migrate-paths [--dry-run] [--force]
```

Copies known legacy `~/.agent-deck` files into the split XDG layout (config under `~/.config/agent-deck`, durable data under `~/.local/share/agent-deck`, cache under `~/.cache/agent-deck`) without deleting the legacy directory. Use `--dry-run` to preview what would be copied.

## Web Command

### web - Start browser UI

```bash
agent-deck web [options]
```

| Flag | Description |
|------|-------------|
| `--listen` | Listen address (default: `127.0.0.1:8420`) |
| `--read-only` | Disable terminal input, stream output only |
| `--token` | Require bearer token for API and WS access |
| `--open` | Reserved placeholder (currently no-op) |

```bash
agent-deck web
agent-deck web --read-only
agent-deck web --token my-secret
agent-deck -p work web --listen 127.0.0.1:9000
```

When token auth is enabled, open the web UI with:

```bash
http://127.0.0.1:8420/?token=my-secret
```

## Session Commands

### session start

```bash
agent-deck session start <id|title> [-m "message"] [--attach] [--json] [-q]
```

`-m` sends initial message after agent is ready.
`--attach` drops you into the session's pane after it starts (requires an interactive terminal; refused under `--json`). On a clean detach you return to the shell; without a TTY it exits non-zero, leaving the session started.
Flags can be placed before or after the session identifier.

### session stop

```bash
agent-deck session stop <id|title>
```

### session restart

```bash
agent-deck session restart <id|title> [--env KEY=VALUE ...]
```

Reloads MCPs without losing conversation (Claude/Gemini).

`--env` injects an environment variable into the replacement process for this
restart only. It can be repeated, and a command-line value overrides configured
environment sources with the same name. The value is not saved to the session:

```bash
agent-deck session restart my-project --env API_URL=https://api.example.com
agent-deck session restart my-project --env FOO=one --env BAR="two words"
```

Supplying `--env` forces the requested restart past the recent-session guard.
Use `--all --env KEY=VALUE` to inject the variable into every active session.
Claude's existing protection that removes `TELEGRAM_*` variables from sessions
that do not own a Telegram channel remains in effect.

### session fork (Claude, OpenCode, Pi, Codex)

```bash
agent-deck session fork <id|title> [-t "title"] [-g "group"]
```

Creates a new session with the same conversation context for supported tools.

In the TUI, quick fork (`f`) is comprehensive by default: it creates a new git worktree + branch, carries the parent's uncommitted state, matches Docker isolation, and inherits the Claude launch options. Defaults are configured in the `[fork]` section — see [config-reference.md](config-reference.md#fork-section). The Web/API fork is a plain tool-native fork and does not apply the `[fork]` defaults.

**Requirements:**
- Claude sessions must have a valid Claude session ID
- Pi sessions use Agent Deck's per-instance Pi session directory and Pi's native `pi --fork`

### session attach

```bash
agent-deck session attach <id|title>
```

Interactive PTY mode. Press `Ctrl+Q` to detach.

### session show

```bash
agent-deck session show [id|title] [--json] [-q]
```

Auto-detects current session if no ID provided.

**JSON output includes:**
- Session details (id, title, status, path, group, tool)
- Claude/Gemini session ID
- Attached MCPs (local, global, project)
- tmux session name

### session current

```bash
agent-deck session current [--json] [-q]
```

Auto-detect current session and profile from tmux environment.

```bash
# Human-readable
agent-deck session current
# Session: test, Profile: work, ID: c5bfd4b4, Status: running

# For scripts
agent-deck session current -q
# test

# JSON
agent-deck session current --json
# {"session":"test","profile":"work","id":"c5bfd4b4",...}
```

**Profile auto-detection priority:**
1. `AGENTDECK_PROFILE` env var
2. Parse from `CLAUDE_CONFIG_DIR` (`~/.claude-team` -> `work`)
3. Config default or `default`

### session set

```bash
agent-deck session set <id|title> <field> <value>
```

**Fields:** title, path, command, tool, claude-session-id, gemini-session-id, account

Setting `account` auto-migrates the Claude conversation into the target account's config dir (same migration as `session switch-account`, but without the automatic stop/restart).

### session send

```bash
agent-deck session send <id|title> "message" [--wait|--stream|--no-wait] [-q] [--json]
agent-deck session send <id|title> --message-file <file|-> [--wait|--stream|--no-wait] [-q] [--json]
```

Use `--message-file` for long or multiline messages, or `--message-file -` for stdin. Do not combine it with an inline message.

```bash
git diff | agent-deck session send my-project --message-file -
agent-deck session send my-project --message-file task.md --wait
```

Default behavior:
- Waits for agent readiness before sending.
- Verifies processing starts after send.
- If Claude leaves a pasted prompt unsent (`[Pasted text ...]`), retries `Enter` automatically.
- Avoids unnecessary retry `Enter` presses when session is already `waiting`/`idle`.

### session approve

```bash
agent-deck session approve <id|title> [once|always|session|N] [--timeout 5s] [-q] [--json]
```

Resolves one currently visible Codex numbered approval menu. It validates that
the same menu is still visible immediately before sending one digit keypress,
then verifies that the original prompt clears. It never sends Enter or retries
the decision automatically. Do not use `session send <id> "1"` for a Codex
approval: that path sends composer text followed by Enter.

### session output

```bash
agent-deck session output [id|title] [--json] [-q]
```

Get the last response from a session. Transcript-backed extraction is tool-dependent; use `--pane` for a raw tmux capture when structured output is unavailable.

### session set-parent / unset-parent

```bash
agent-deck session set-parent <session> <parent>
agent-deck session unset-parent <session>
```

### session switch-account

```bash
agent-deck session switch-account <session> <account>
```

Moves a session — conversation included — to another configured Claude account: stops the session, migrates the Claude conversation file into the target account's config dir (copy-only, with a destination backup and size verification), sets the account, and restarts with `--resume`.

```bash
agent-deck session switch-account "My Project" work
```

Accounts are the profiles named in `config.toml` (`[profiles.<name>.claude].config_dir`).

## Fleet Recovery Commands

Recovery from a *fleet-wide* session death: every managed pane on the host gone
at once (a killed tmux server, a host reboot, an auth cascade that made the
agents exit). For a single session use `session restart`; for sessions whose
pane is still alive but whose control pipe broke use `session revive`.

### fleet status

```bash
agent-deck fleet status [--group <path>] [--include-idle] [--json]
```

Reports which sessions the registry believes are alive but whose tmux session is
gone. **Read-only** — no restarts, no writes. Prints a `MASS DEATH detected` line
when the down set is large enough (both in absolute count and as a share of
should-be-alive sessions) to be a fleet-wide event rather than one crash.

A session is only counted as down after two independent tmux probes agree it is
gone (`--confirm-probes`), because a single `has-session` miss right after a tmux
server restart is not proof of death.

Sessions you stopped or queued, and archived sessions, are never counted. Status
`idle` is excluded by default (it is also the status of a session that was added
but never started) — `--include-idle` opts in.

### fleet recover

```bash
agent-deck fleet recover                       # plan only (dry run)
agent-deck fleet recover --yes                 # actually recover
agent-deck fleet recover --yes --spacing 8s --limit 10
agent-deck fleet recover --yes --group agent-deck --json
```

Restarts the down sessions **one at a time**, waiting `--spacing` (default 5s,
jittered) between boots and verifying each boot before starting the next.
Sequential spacing is the point: a burst of simultaneous agent boots is what
forks a shared rotating OAuth refresh token and 401s the whole fleet.

**Dry run by default.** Without `--yes` the command prints the plan (order,
waits, estimated runtime) and exits without restarting anything.

Each boot is verified before the next begins: the pane must be back AND the
session must reach a state only a booted agent produces. A restart that returns
successfully but never proves it booted is reported as `unverified`, never as
`recovered`.

The sweep halts early when the trouble looks systemic:

| Brake | Flag | Default | Why |
|-------|------|---------|-----|
| Consecutive failed restarts | `--max-failures` | 3 | Three failures in a row means a common cause; grinding through the rest multiplies the damage |
| Sessions that restart and then die immediately | `--max-dead-boots` | 3 | A pane that is gone again by verification time means the session exited on boot — the way a dead credential actually presents (the agent quits on the 401, so there is no banner to read). Three in a row is a host- or credential-level fault (`0` disables) |
| Sessions booting into an auth failure | `--auth-halt-after` | 2 | Restarting the fleet against a broken credential deepens the cascade (`0` disables) |

A slow boot is not a dead one: a pane that is up but still `starting` when the
verify timeout expires is reported `unverified` and does not trip any brake.

A halted sweep exits non-zero (with `--json` too) and reports the reason.

Options:

```bash
--yes                    Actually restart (without it, plan only)
--dry-run                Force plan-only mode even with --yes
--spacing <dur>          Gap between boots (default 5s; 0 disables — not recommended)
--jitter <fraction>      Random +/- fraction applied to each gap (default 0.2)
--limit <n>              Restart at most N sessions (0 = all)
--verify-timeout <dur>   How long one session may take to prove it booted (default 30s)
--verify-poll <dur>      Verification poll interval (default 500ms)
--max-failures <n>       Halt after N consecutive failed restarts (default 3)
--max-dead-boots <n>     Halt after N consecutive boots whose pane died immediately (default 3, 0 disables)
--auth-halt-after <n>    Halt after N auth-failed boots (default 2, 0 disables)
--group <path>           Only consider sessions in this group and its descendants
--include-idle           Also treat status=idle sessions as down
--confirm-probes <n>     Probes that must agree a session is gone (default 2)
--confirm-delay <dur>    Delay between confirming probes (default 750ms)
--min-dead <n>           Minimum down sessions for a mass-death verdict (default 3)
--dead-fraction <f>      Share of should-be-alive sessions that must be down (default 0.5)
--json, -q               Machine-readable / minimal output
```

Recovery only ever writes the rows it restarted, one at a time, through a
targeted write with no table sweep — a session added by another process during
the (multi-minute) sweep can never be lost.

## Worktree Commands

### worktree list

```bash
agent-deck worktree list
```

Lists worktrees and their associated sessions.

### worktree info

```bash
agent-deck worktree info <session>
```

Shows detailed worktree info for a session.

### worktree cleanup

```bash
agent-deck worktree cleanup [--force]
```

Finds orphaned worktrees/sessions. Dry-run by default; `--force` performs the cleanup.

## MCP Commands

### mcp list

```bash
agent-deck mcp list [--json] [-q]
```

### mcp attached

```bash
agent-deck mcp attached [id|title] [--json] [-q]
```

Shows MCPs from LOCAL, GLOBAL, PROJECT scopes.

### mcp attach

```bash
agent-deck mcp attach <session> <mcp> [--global] [--restart]
```

- `--global`: Write to Claude config (all projects)
- `--restart`: Restart session immediately

### mcp detach

```bash
agent-deck mcp detach <session> <mcp> [--global] [--restart]
```

## Skill Commands

Skills are discovered from configured sources and attached per project for supported runtimes.

### skill list

```bash
agent-deck skill list [--source <name>] [--json] [-q]
agent-deck skill ls
```

`--source` filters by source name (for example `pool`, `claude-global`, `team`).

### skill attached

```bash
agent-deck skill attached [id|title] [--json] [-q]
```

Shows:
- Manifest-managed attachments from `<project>/.agent-deck/skills.toml`
- Unmanaged entries currently present in the managed project skill roots (`<project>/.claude/skills` and `<project>/.agents/skills`)

### skill attach

```bash
agent-deck skill attach <session> <skill> [--source <name>] [--restart] [--json] [-q]
```

- `--source`: Force source when name is ambiguous
- `--restart`: Restart session immediately after attach for Claude, Gemini, and Codex sessions

Attach target root is runtime-specific:
- Claude-compatible sessions -> `<project>/.claude/skills`
- Gemini, Codex, and Pi sessions -> `<project>/.agents/skills`

### skill detach

```bash
agent-deck skill detach <session> <skill> [--source <name>] [--restart] [--json] [-q]
```

- `--source`: Filter by source when detaching
- `--restart`: Restart session immediately after detach for Claude, Gemini, and Codex sessions

### skill source list

```bash
agent-deck skill source list [--json] [-q]
agent-deck skill source ls
```

### skill source add

```bash
agent-deck skill source add <name> <path> [--description "..."] [--json] [-q]
```

### skill source remove

```bash
agent-deck skill source remove <name> [--json] [-q]
agent-deck skill source rm <name>
```

## Group Commands

### group list

```bash
agent-deck group list [--json] [-q]
```

### group create

```bash
agent-deck group create <name> [--parent <group>]
```

### group delete

```bash
agent-deck group delete <name> [--force]
```

`--force`: Move sessions to parent and delete.

### group move

```bash
agent-deck group move <session> <group>
```

Use `""` or `root` to move to default group.

## Profile Commands

```bash
agent-deck profile list
agent-deck profile create <name>
agent-deck profile delete <name>
agent-deck profile default [name]
```

## Conductor Commands

```bash
agent-deck conductor setup <name> [--description "..."] [--heartbeat|--no-heartbeat]
agent-deck conductor teardown <name> [--remove]
agent-deck conductor teardown --all [--remove]
agent-deck conductor status [name]
agent-deck conductor list [--profile <name>]
```

- `setup` creates `~/.agent-deck/conductor/<name>/` plus `meta.json` and registers `conductor-<name>` session in the selected profile.
- `setup` also installs shared `~/.agent-deck/conductor/CLAUDE.md` (or symlink via `--shared-claude-md`).
- Heartbeat timers run per conductor (default every 15 minutes) and can be disabled with `--no-heartbeat`.
- Heartbeat sends use non-blocking `session send --no-wait -q` to avoid timeout churn when sessions are busy.
- Bridge daemon is installed only when Telegram and/or Slack is configured in `[conductor]`.
- Transition notifier daemon (`agent-deck notify-daemon`) is installed by setup and sends event nudges on `running -> waiting|error|idle` transitions (parent first, then conductor fallback).

## Remote Commands

Manage agent-deck instances running on remote SSH servers. Remote sessions appear alongside local sessions in the TUI and CLI.

Registered remotes are named fleet endpoints. They are separate from the per-session SSH destination used by `agent-deck add --ssh`.

Remote configuration is stored in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`) under the `[remotes]` map.

### remote add

```bash
agent-deck remote add <name> <user@host> [options]
```

| Flag | Description |
|------|-------------|
| `--agent-deck-path <path>` | Path to the agent-deck binary on the remote (default: `agent-deck`) |
| `--profile <name>` | Remote profile to use (default: `default`) |

Registers a remote instance. If agent-deck is not found on the remote, it is installed automatically. Remote names must be alphanumeric and may contain underscores or hyphens (no spaces, slashes, dots, or colons).

### remote remove / rm

```bash
agent-deck remote remove <name>
agent-deck remote rm <name>
```

Removes a remote from configuration.

### remote list / ls

```bash
agent-deck remote list [--json]
agent-deck remote ls [--json]
```

Lists all configured remotes. Use `--json` for scripting.

### remote sessions

```bash
agent-deck remote sessions [name] [--json]
```

Fetches active sessions from all remotes, or from a specific remote if `name` is provided. Displays title, tool, live status, and session ID. Use `--json` for scripting.

In the TUI, remote sessions use the same status indicators and nested group tree as local sessions. Remote headers and groups can be collapsed, and `K`/`J` preserve a manual order within each remote group. A session's location (local or SSH host plus remote path) is part of its identity, so identical titles at different locations do not collide.

### remote drain

```bash
agent-deck remote drain <name|user@host> [--into <session-id>] [--json]
```

Pulls the completion and transition records a remote agent-deck instance holds and writes them into **this** machine's inbox, so a conductor that launched workers on another host learns they finished without tmux-scraping or file polling (issue #1948).

Transition notifications are parent-linked, and a `parent_session_id` cannot point across machines — so a remote worker's completion never reaches a conductor on a different host. `remote drain` closes that gap by pulling: the conductor's own command is the delivery event, so there is no delivery handshake, no ack, and nothing to replay.

| Flag | Description |
| --- | --- |
| `--into <session-id>` | Local session whose inbox receives the records (default: the calling session, same resolution as `inbox drain self`) |
| `--json` | Emit `{remote, host, target_session_id, fetched, written, duplicates, records}` for a conductor heartbeat |

- **What it returns.** Completions (from the completion ledger) *and* transitions — including the waiting/error/idle flips of sessions that have no parent on the remote host, which is the normal state for a worker whose conductor is on another machine. Those are kept in a reserved `_unowned` ledger beside the per-parent inboxes; a quota-stalled remote session shows up in a drain because of it. Sessions that opted out with `--no-transition-notify` are never exported.
- **Read-only on the remote.** It runs the remote's `agent-deck inbox export`, which consumes, truncates and marks nothing. Two conductors draining the same host both receive the records, and the host's own conductor still drains its inbox normally.
- **Safe to repeat.** Records are written through the inbox's existing fingerprint dedup, so a second drain reports `0 new` and adds no duplicate line. Across a consumption boundary the `turn_fingerprint` consumed ledger collapses a re-pulled record instead.
- **Records are stored under `<remote>:<child-id>`.** A child id is only unique on the host that minted it — `run-task --child <ID>` takes any string — so two hosts running the same named task would otherwise produce records that destroy each other in the conductor's inbox (every identity rule downstream keys on the child id). The stored id names its host, in the same `<remote>:<session>` spelling the TUI uses for remote sessions.
- **Honest about failure.** Exit `0` = drained (a reachable remote with nothing pending says so explicitly), `2` = unknown remote / none configured, `3` = the remote could not be reached *or could not read its own records*. Neither an ssh failure nor an unreadable record file on the remote ever reads as "nothing to report".
- The remote must run a build that has `inbox export`; an older one is reported as a version error pointing at `agent-deck remote update`.

Narrowing a drain to one conductor's children (`--parent <conductor-id>@<host>`) is deferred; it is sugar over this pull.

### remote attach

```bash
agent-deck remote attach <remote-name> <session-title-or-id>
```

Attaches interactively to a session running on a remote instance. Accepts either a full session title or an ID prefix.

### remote rename

```bash
agent-deck remote rename <remote-name> <session-title-or-id> <new-title>
```

Renames a session on a remote instance.

### remote update

```bash
agent-deck remote update [name]
```

Downloads and installs the correct agent-deck binary (detected platform/arch) on all remotes, or on a specific remote if `name` is provided. Prompts for confirmation before updating.

### Examples

```bash
agent-deck remote add dev user@dev-box
agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck
agent-deck remote list
agent-deck remote sessions dev
agent-deck remote attach dev my-session
agent-deck remote rename dev my-session new-name
agent-deck remote update          # update all remotes
agent-deck remote update dev      # update specific remote
```

SSH uses OpenSSH host-key verification and `BatchMode=yes`; unknown or changed hosts fail instead of prompting. Authenticate with an SSH agent or configured key and establish trust in `known_hosts` before registering a remote. `remote update` verifies the downloaded archive against the release checksums before deployment.

## Codex Hook Commands

```bash
agent-deck codex-hooks install
agent-deck codex-hooks status
agent-deck codex-hooks uninstall
```

Codex turn-level status uses its notify hook. Install it once per Codex home; if `CODEX_HOME` is set, use the same environment for installation and Codex sessions.

## DeepSeek Commands

Inspect the DeepSeek Harness (`dsh`) integration. Read-only; every subcommand takes `--json`.

```bash
agent-deck deepseek status              # resolved binary, version, DSH_HOME, profile, resume/fork support
agent-deck deepseek profiles            # profiles under $DSH_HOME/profiles, with their bundle layers
agent-deck deepseek sessions [path]     # dsh sessions recorded for a workspace (default: cwd)
```

The tool is named for the vendor; the binary it launches is `dsh`
(`npm install -g @deepseek-ai/dsh`). Launch a session with
`agent-deck launch -c deepseek`.

`status --json` reports `resume_supported` and `fork_supported` as explicit booleans:
`dsh` has no fork command, and neither shipped profile (`web`, `headless`) accepts a
resume flag, so both are false on a default install. Configure with `[deepseek]`
(`command`, `config_dir` → `DSH_HOME`, `profile`, `patches`, `host`/`port`/
`trusted_hosts`, `resume_flag`, `extra_args`, `env_file`) and give each account its own
harness home with `[profiles.<account>.deepseek].config_dir`.

See [docs/tools/deepseek.md](../../../docs/tools/deepseek.md) for the full guide.

## Session Resolution

Commands accept:
- **Title:** `"My Project"` (exact match)
- **ID prefix:** `abc123` (6+ chars)
- **Path:** `/path/to/project`
- **Current:** Omit ID in tmux (uses env var)

## Exit Codes

| Code | Meaning |
|------|---------|
| 0 | Success |
| 1 | Error |
| 2 | Not found |
