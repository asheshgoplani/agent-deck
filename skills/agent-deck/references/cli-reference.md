# CLI Command Reference

Complete reference for all agent-deck CLI commands.

## Table of Contents

- [Global Options](#global-options)
- [Basic Commands](#basic-commands)
- [Shell Completion](#shell-completion)
- [Web Command](#web-command)
- [Session Commands](#session-commands)
- [Fleet Recovery Commands](#fleet-recovery-commands)
- [Worktree Commands](#worktree-commands)
- [MCP Commands](#mcp-commands)
- [Skill Commands](#skill-commands)
- [Group Commands](#group-commands)
- [Profile Commands](#profile-commands)
- [Inbox Commands](#inbox-commands)
- [Remote Commands](#remote-commands)
- [Health Command](#health-command)
- [Inbox Commands](#inbox-commands)
- [Codex Hook Commands](#codex-hook-commands)
- [DeepSeek Commands](#deepseek-commands)
- [Conductor Commands](#conductor-commands)

## Global Options

```bash
-p, --profile <name>    Use specific profile
--json                  JSON output
-q, --quiet             Minimal output
```

`--help` and `-h` are read-only on every human-facing command. Bare `help` is
recognized only in a command position; in a value position it remains usable
as a workspace, remote, session, or other identifier.

## Basic Commands

### add - Create session

```bash
agent-deck add [path] [options]
```

| Flag | Description |
|------|-------------|
| `-t, --title` | Session title |
| `-g, --group` | Group path |
| `-c, --cmd` | Tool/command (claude, codex, gemini, opencode, pi, shell, copilot, crush, muse, cursor, hermes, deepseek, or a custom command string) — `shell` is a plain terminal with no AI tool attached |
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
agent-deck launch . -c claude --model claude-opus-5 --effort high   # the dialog's Model / Reasoning effort rows
agent-deck launch . -g ard -c claude -m "Review dataset"
agent-deck launch . -c "codex --dangerously-bypass-approvals-and-sandbox"
agent-deck launch -g book-keeper -c claude   # no path: lands on the group's default_path
```

Notes:
- `[path]` omitted: resolves the target group's `default_path`, then the global `default_path` config key, then cwd — the same chain as `add` (#1303). An explicit `.` always means the current directory.
- `--account <name>` selects a named slot from `[profiles.<name>.claude].config_dir` for this session, matching `add --account`.
- `--model <id>` and `--effort <level>` are the per-session overrides behind the TUI's Model ID and Reasoning effort rows (also on `add`). Effort levels: claude `low|medium|high|xhigh|max`, codex `minimal|low|medium|high|xhigh`; other tools refuse the flag. Both are echoed in `--json` output (`model`, `effort`) and by `session show --json`.
- `--account` requires an explicit name. If the next token is another launch flag, launch stops with an error before resolving a fallback account or creating a session; use `--account=<name>` when a name intentionally begins with a dash.
- `--no-identity` (also on `add`): skip the harness identity injection for this session only. By default every spawn tells the model it runs inside agent-deck, its session metadata and how to use the CLI (`[launch] inject_identity` in config-reference.md, `documentation/HARNESS_IDENTITY.md`). Persisted, so restarts honour it.

### accounts - List named account slots

```bash
agent-deck accounts [--json]
```

Lists profiles that configure a Claude `config_dir`; these names are accepted by `add --account`, `launch --account`, `session set <id> account`, `session switch-account`, and the account rows in the TUI's New Session and Edit Session dialogs.

Each row reports the slot name, its config dir, and whether that directory exists yet — a configured account with no directory has never been logged in (`CLAUDE_CONFIG_DIR=<dir> claude`, then `/login`). The `--json` form carries the same three fields (`name`, `config_dir`, `exists`).

### list - List sessions

```bash
agent-deck list [--json] [--all]
agent-deck ls  # Alias
```

Both JSON forms always include `account`: the exact stored per-session slot, including an empty string when no slot is explicitly stored. Human tables show the slot in a quoted `ACCOUNT` column, escaping controls. This is stored metadata, not a resolved account or login identity.

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

### update - Check for and install a new release

```bash
agent-deck update                      # check GitHub, show changelog, Y/n, install
agent-deck update --check              # only check
agent-deck update --check --json       # {"current","latest","available","publishing","auto_install","auto_restart","timer":{...}}
agent-deck update --version 1.7.3      # install a specific release (may downgrade)
agent-deck update --unattended         # no prompts, no changelog, no stdin
agent-deck update --unattended --trigger timer|tui|manual
agent-deck update --install-timer [--dry-run]
agent-deck update --uninstall-timer [--dry-run]
agent-deck update --timer-status
```

- `--unattended` is what the daily timer and the TUI's `auto_install` run. It honours `[updates] auto_install` (off means "nothing installed", exit 0), never runs Homebrew (prints the `brew` command, exit 2), takes `<cache dir>/update.lock` so two runs never replace the binary at once (busy means exit 0), skips the remotes prompt, and exits 1 when the install or the macOS launchd hygiene failed. `--trigger` (default `$AGENTDECK_UPDATE_TRIGGER`, then `manual`) only tags the debug log lines.
- `--install-timer` writes `~/Library/LaunchAgents/com.agentdeck.autoupdate.plist` (macOS, daily at 07:MM with a random minute, program `/bin/sh`) or `~/.config/systemd/user/agent-deck-autoupdate.{service,timer}` (Linux, `OnCalendar=daily`, `RandomizedDelaySec=1h`) and loads it. Installing over an existing timer replaces it; `--dry-run` prints the exact files and commands and executes nothing. The timer's output goes to `<log dir>/auto-update.log` on macOS and the journal on Linux.
- On macOS every install (interactive, `--version`, the TUI prompt and `--unattended`) re-registers the `com.agentdeck.*` launch agents whose program is the replaced binary (`launchctl bootout` then `bootstrap`, then a `state = running` check for KeepAlive/RunAtLoad agents). Without this they crash-loop with `EX_CONFIG` (exit 78) because macOS ties a launch agent's identity to the file at its program path. If an agent does not come back the command exits 1 and prints the two `launchctl` commands to run by hand; the binary is already updated at that point.

## Shell Completion

### completion - Print a shell completion script

```bash
agent-deck completion bash    # -> stdout
agent-deck completion zsh     # -> stdout
agent-deck completion fish    # -> stdout
```

Completes top-level commands and, for the ones with their own subcommand dispatch (`session`, `mcp`, `skill`, `group`, `remote`, `worktree`, `profile`, `conductor`, `agent(s)`, `watcher`, `openclaw`, `costs`, `hooks`, the `*-hooks` family, `deepseek`), the next word too.

For commands that name a specific resource, the argument after that completes to live values, fetched via the hidden `agent-deck __complete <kind>` helper (not a command you'd run directly):

| Kind | Used by |
|------|---------|
| session titles | `remove`/`rename`, most `session <verb>` subcommands (including both positions of `set-parent`), `mcp`/`plugin`/`skill attached\|attach\|detach`, `worktree info\|finish`, `group move`, `conductor move`, `remote attach\|rename` (2nd arg) |
| remote names | `remote remove\|sessions\|attach\|rename\|update` |
| profile names | `profile delete\|default`, `session switch-account` (2nd arg), and right after a leading `-p`/`--profile` |
| group paths | `group show\|update\|delete\|move (2nd arg)\|change\|reorder` |
| adopted agent names | `agent show` |

This is what lets `agent-deck remote update <Tab>` offer your configured remotes and `agent-deck session set-parent <Tab> <Tab>` offer session titles at both positions, three and four words in — dynamic completion isn't limited to the first word after a subcommand. A leading `-p`/`--profile` is detected and forwarded, so completions match the profile being typed rather than the default one. Any other argument (paths, free-form text) falls back to the shell's default completion.

```bash
# bash
echo 'source <(agent-deck completion bash)' >> ~/.bashrc

# zsh — either source it directly, or save it as a file named `_agent_deck`
# in a directory on $fpath for autoload
echo 'source <(agent-deck completion zsh)' >> ~/.zshrc

# fish
agent-deck completion fish > ~/.config/fish/completions/agent-deck.fish
```

Open a new shell (or re-source the config file) for it to take effect.

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
- `account`: the exact stored slot, always present including an empty string. Human output shows a quoted, control-escaped `Account:` field; neither form resolves login identity.
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
# {"session":"test","title":"test","profile":"work","id":"c5bfd4b4","tool":"claude",
#  "group":"projects","account":"","parent_session_id":"","path":"/...","status":"running",
#  "tmux_session":"agentdeck_test_...","identity_file":"/.../runtime/identity/c5bfd4b4/identity.md"}
```

The JSON form is the machine-readable identity a session fetches from inside: `tool`, `account` and `parent_session_id` are always present (empty when unset); `group`, `tmux_session`, `is_conductor`, `worktree_branch` and `identity_file` appear when set. The injected identity block (`[launch] inject_identity`) points the model here for the live record.

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
- Never sends interrupt keys (Ctrl-C) into a target, whatever it observes.

**Read `confirmation`, not the human text.** `--json` carries a stable 3-way `confirmation` field (`confirmed` / `unknown` / `failed`) — that is the contract to branch on. `delivery` is a separate, finer-grained diagnostic string (13 possible values, listed below) for logging and debugging, not for scripted decisions: several `delivery` values map to `confirmation: "unknown"` (still exit 0 — a real, non-failed outcome), and only a handful map to `confirmation: "failed"`.

Delivery verdict (`--json` also carries `delivery` and a machine-checkable `submitted` boolean):
- `submitted` (exit 0, `submitted: true`, `confirmation: "confirmed"`): positive evidence the target accepted the message and began its turn.
- `queued` (exit 0, `submitted: false`, `confirmation: "unknown"`): Claude targets only. The target was mid-turn per its hook-driven status before the send, the body newly arrived in its pane, and Claude's composer showed its own "Press up to edit queued messages" placeholder (the composer element itself, not those words anywhere in the pane). Claude takes it up when the current turn ends. Do not resend. `submitted` on a Claude target is confirmed by the message's own record appearing in the transcript, or by the hook status flipping from idle to running once the body has landed.
- `queued_socket` (exit 0, `submitted: false`, `acknowledged: false`): written to the target's Claude Code messaging socket (opt-in `send_transport = "auto"`) after identity verification; Claude's inbox sends no ack, so this means only "the bytes were written", not that the turn started.
- `delivered` (exit 0, `submitted: false`, `confirmation: "unknown"`) — the message body reached the target and Enter was sent, but the tool exposes no submission signal (a shell, an unknown tool) or its signal didn't arrive in the window; this is the honest "delivered-unconfirmed" outcome, not a failure.
- `unverified` (exit 0, `confirmation: "unknown"`): the payload was small enough to rule out the overflow failure mode, but neither a Claude-shaped submission signal nor a content-arrival check reached a verdict — genuinely unknown, not "probably failed".
- `line_too_long`, `menu_open`, `pane_gone`, `typed_not_submitted`, `no_evidence`, `send_failed`, `composer_blocked`, `socket_write_failed` (exit 1, `confirmation: "failed"`, `code: DELIVERY_FAILED`): not delivered on positive evidence (a gone pane, a composer still holding the body, an open menu) — see the error text for whether a retry is safe. (There is no `typed` verdict — that pre-#1793 catch-all was replaced by `delivered`/`unverified` above so the exit code follows the evidence instead of its absence.)

With `--wait` or `--stream` on a Claude target, the reply is bound to the transcript record of this exact message: a message queued behind a live turn waits for its own turn to start, the read begins after that record, and it stops at the next human prompt (an interrupted turn is reported as incomplete or as a stream error, not as the next turn's answer). Slash commands and non-Claude tools keep the timestamp-based best-effort reply.

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
agent-deck session output [id|title] [--json] [-q] [--pane] [--copy] [--max-tokens N]
```

Get the last response from a session. Default text output strips ANSI and is
bounded to approximately 25,000 tokens (configurable with `--max-tokens`), with
an explicit omission marker and a durable full-output path when truncated.
`--json`, `-q`/`--quiet`, and `--copy` preserve the full source for compatibility;
`--pane --json` is the raw ANSI-preserving transport used by remote previews.

### session context

```bash
agent-deck session context [id|title] [--tab overview|breakdown|verify] [--item <id>] [--all] [--capabilities] [--json] [--strict]
```

Show what is loaded into the agent's context for a session — the instruction-file
hierarchy, skills, agents, deferred tool names and MCP servers — ranked by what
each costs, with the lever (file to edit, directory to delete, command to run)
for everything the user controls.

Every figure states its provenance on two independent axes: whether the *text*
is verbatim as the harness injected it, reconstructed from the same sources the
harness reads, or absent; and whether the *number* was measured by the provider,
estimated, obtained by subtraction, or unknown. An unknown renders as `—` and is
never summed as zero, and a total containing one is prefixed `≥`.

- `--tab overview` (default) — the occupancy gauge and one row per category.
- `--tab breakdown` — every item ranked, actionable first, with its id and lever.
- `--tab verify` — the arithmetic: anchor, attributed, residual, coverage,
  invariant violations, and what the adapter declared it could achieve.
- `--item <id>` — one item's provenance, lever and verbatim text. Accepts any
  unique id prefix.
- `--capabilities` — what this harness can report, without inspecting anything.
- `--json` — the full report on a stable schema (`schema_version`), whatever the
  tab. Unknown token counts encode as `null`, never `0`.
- `--strict` — exit 3 when the report fails its own reconciliation. Without it
  the command exits 0 for any report it managed to produce, including an honest
  "token accounting unsupported for <tool>" inventory.
- `--verify` — the only mode that writes anything: types the harness's own accounting
  command (`/context` on Claude, `/status` on Codex) into the **live** session and reads
  the panel back, so it always asks for confirmation first (`--yes` skips the prompt, for
  CI/non-interactive callers — never combine `--verify` with a non-interactive shell
  without `--yes`, or it will hang waiting for a confirmation no one can answer). It waits
  for the agent to go idle first (`--verify-ready-timeout`, default 2m) and moves an unsent
  draft out of the composer, but the accounting command still joins that session's
  conversation for good. `--tolerance-pct` (default 10) and `--tolerance-tokens` (default
  500) set how much disagreement with the harness's own figure still counts as agreement.
  The TUI's `C` context hotkey never triggers `--verify`.
- `--timeout <dur>` (default 20s) aborts the whole inspection; `--verify-timeout <dur>`
  (default 30s) is the narrower wait for the harness's own panel to render during `--verify`.
- `--glossary` — define every term these screens use (adapter, basis, anchor, residual,
  CAPT/RECON/ABSENT) and exit; `--verbose` includes the per-category notes on how each
  figure was obtained.

Claude Code sessions are fully supported (measured first-turn anchor, verbatim
skill/agent/tool listings, reconstructed CLAUDE.md chain). Harnesses with no
readable accounting still get a populated inventory with `—` for every figure.

```bash
agent-deck session context my-project --tab breakdown
agent-deck session context my-project --json | jq '.report.reconciliation'
agent-deck session context my-project --verify --yes   # CI-safe: skips the confirmation prompt
```

Exit codes: `0` done (with `--verify`, every graded group agreed within tolerance) · `1` could not run (bad arguments, no live pane, unreadable panel — nothing was compared) · `2` no session matched · `3` `--strict` reconciliation/invariant failure · `4` `--verify` disagreement beyond tolerance · `5` `--verify` had nothing gradable (never read this as a pass).

### session children

```bash
agent-deck session children [id|title] [--json] [-q] [--follow] [--until-done] [--interval <dur>] [--heartbeat <dur>]
```

List a session's sub-sessions with live status and last completion history. Completion fields (`done_status`, `done_summary`, `done_at`) describe the last asserted completion; live `status` (running/waiting/idle/error/queued) determines the current turn — a session can show completion history and still be `running` again. Defaults to the current session. Read-only: never clears the inbox, so poll it as often as you like.

`--follow` streams JSONL events instead of a single snapshot: `snapshot` (initial per-child state), `added`, `status` (from/to transition), `done` (completion sentinel), `removed`, `error`, plus a periodic `heartbeat` (default 60s, `--heartbeat 0` disables). `--until-done` exits 0 once every child either needs input or is terminal (waiting, idle with completion history, error, or stopped). `--interval` tunes the poll cadence backing `--follow` (default 2s).

```bash
agent-deck session children --json
agent-deck session children --follow --until-done
```

### session metrics

```bash
agent-deck session metrics <id|title> [--json] [--since 24h]
agent-deck session metrics --all [--json] [--since 24h]
agent-deck remote exec <name> session metrics <id> --json
```

Per-session numbers for evals, derived on demand from the profile's local session event journal (see `[health] session_events` in the configuration reference). Nothing is probed: the command reads the journal once, the dead-letter stores once, and the task-worker completion records once.

| Field | Meaning |
|-------|---------|
| `turns.count` / `turns.measured` | Turns observed (a status leaving `running`); `measured` is how many also had an observed start, so `p50_ms`/`p95_ms` (running → waiting/idle) come only from those. |
| `waiting_ms` | Time the session sat at `waiting` (for input) in the window, including an open interval up to now. |
| `sends.*` | Sends with outcome `confirmed`, `delivered-unconfirmed` or `failed`; `unconfirmed_rate` excludes unknown outcomes; `ack_p50_ms`/`ack_p95_ms` are send-to-confirmed-accept times for confirmed sends only. |
| `restarts` | `session restart` runs (single and `--all`). |
| `dead_letters` | Records in the dead-letter and `_unowned` stores for this session. |
| `worker` | Task-worker completion (status, created → finished duration) when a completion record exists. |
| `last_status_change` | Last observed status, when, and its age. |
| `journal` | `ok`, `no events`, or `disabled` (kill switch off; any numbers are history). |

Unknown values are `null`, never `0`. `--all` returns a JSON array for every session with events in the window. Over `remote exec`, an older remote without the command answers with one line saying so (exit 2).

**How to read these numbers.** Turn duration is measured at the daemon's poll cadence (1–3 s), so treat it as coarse: compare medians across many turns, not single values. A rising `unconfirmed_rate` means sends are landing without a visible accept signal (a busy composer, a tool without hooks), a rising `waiting_ms` means the session is blocked on a human, restarts and dead letters are the "something broke" counters. An eval compares two builds on the same window: `agent-deck health --json` gives the profile roll-up (turns/day, median turn, unconfirmed send rate, restarts/day, sessions with dead letters).

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

## Inbox Commands

### dead-letter - Inspect and resolve terminal delivery failures

```bash
agent-deck inbox dead-letter list [--json]
agent-deck inbox dead-letter show [--json] <record-id>
agent-deck inbox dead-letter retry [--json] <record-id>
agent-deck inbox dead-letter purge [--json] --older-than <duration>
agent-deck inbox dead-letter purge [--json] --yes
```

`list` and `show` (#2111) are read-only forensic inspection of every physical
record, including malformed and undecodable ones: they intentionally do
include the raw on-disk bytes (base64 in `--json`) so a broken record can be
diagnosed without routing or repairing it. They never consume or mutate a
store, and each record's `ref` identifies an exact source snapshot and byte
offset — any append or rewrite to that source invalidates old refs, so a
stale `show <ref>` is refused rather than silently pointing at the wrong
record. `list`/`show` output also includes each record's `id`: a content hash
that stays stable across unrelated changes elsewhere in the same store, and
is what `retry`/`purge` (#2062) actually key off (accepting a unique prefix).

`retry` re-resolves the child's current parent and commits the event to that
parent's durable inbox before removing exactly the delivered dead-letter record.
If the child or parent no longer exists, or the target remains undeliverable,
the command exits non-zero and retains the record. `retry` may act on an
`_unowned` record (it is redelivered like any other).

An unbounded purge requires `--yes`. `--older-than` is the non-interactive,
bounded alternative; corrupt or undated records are never selected by an age
bound. Unlike `retry`, `purge` never removes a record from the `_unowned`
discovery ledger — that ledger has no ack path, so only the TTL sweep
(`SweepInboxByTTL`, the same 7-day-default horizon `inbox` events use) may
reclaim one; purging otherwise would erase the only evidence a remote
session had stalled. `purge`'s human-readable summary reports how many
`_unowned` records were skipped; the count still shows up in `inbox drain`'s
pending total until the TTL sweep clears it.

`retry` and `purge` both accept `--json`, printing a JSON array of
`{"id", "action", "outcome", "reason"}` objects — one entry per record
retry/purge actually considered, including any `_unowned` record purge
skipped (`outcome: "skipped"`) — instead of the human-readable summary line.

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
agent-deck remote list [--json] [--check]
agent-deck remote ls [--json] [--check]
```

Lists all configured remotes. The VERSION column shows the agent-deck version each remote last reported (learned by the TUI poll, `remote update`, or `--check`), with `↑` when it is older than this controller; `-` means never checked. `--check` asks every remote now (one SSH call each) and refreshes that cache. Use `--json` for scripting (`version`, `version_checked_at`, `outdated`).

### remote sessions

```bash
agent-deck remote sessions [name] [--json] [--with-errors|--json-envelope]
```

Fetches active sessions from all remotes, or from a specific remote if `name` is provided. Displays title, tool, live status, and session ID. Use `--json` for scripting: it emits a bare array of sessions, so consumers piping through `jq '.[]'` keep working. Per-remote fetch failures are printed in text mode but are not visible in the bare-array JSON.

To also see fetch failures in JSON, add `--with-errors` (or the equivalent `--json-envelope`, which implies `--json`): the output becomes `{"sessions": [...], "errors": [{"name", "host", "error"}]}` and the command exits `1` if any remote failed. This envelope is always opt-in, so the plain `--json` shape stays stable for existing scripts.

In the TUI, remote sessions use the same status indicators and nested group tree as local sessions. Remote headers and groups can be collapsed, and `K`/`J` preserve a manual order within each remote group. A session's location (local or SSH host plus remote path) is part of its identity, so identical titles at different locations do not collide.

### remote drain

```bash
agent-deck remote drain <remote-name> [--into <session-id>] [--json]
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

### remote exec (management commands forwarded to a named remote)

```bash
agent-deck remote <name> <command> [arguments]
```

`remote <name>` forwards a command to run *on* that remote, using the remote's own accounts, harnesses and worktrees rather than the controller's: `list/status/health`, `show/output/send`, `add/launch`, `session start/stop/restart/fork/archive/unarchive/set`, `session switch/switch-preview/switch-account`, `worktree list/info/cleanup`, `mcp list/attach`, `skill list/attached/attach/detach`, `group list/reorder`. Use `remote exec <name> <command>` if `<command>` happens to collide with a top-level `remote` management verb (e.g. `list`).

`session switch`/`switch-preview` forwarded this way runs the remote's own switch engine with the same guards as a local switch (ownership revalidation, managed-source refusal, journaled account/harness moves) — the CLI only forwards a closed set of subcommands, so no local path or credential can reach it. `switch-preview --json` previews losses/warnings before committing; the confirmed switch reports `verified`, `pending`, or `failed` with `recovery_required` when applicable. This requires the remote to already be a target you can reach and administer — it does not let a controller switch accounts *for* a remote it doesn't own.

### remote update

```bash
agent-deck remote update [name | --all] [--force] [--dry-run] [--json]
agent-deck remote update [name | --all] --from-build <dir>
```

Downloads and installs the correct agent-deck binary (detected platform/arch) on a specific remote, or with `--all` (or no name) on every configured remote whose version is older than this controller's.

`--from-build <dir>` installs from a local directory of release-layout archives (darwin/arm64, linux/amd64, linux/arm64) instead of downloading a published release — for deploying a verified local build to remotes before it's released. Each archive is still checksum- and version-verified before the atomic install; a remote running a local build takes precedence over one running an equal-or-older published release for restart-watcher purposes. `--force` allows reinstalling the same version or downgrading (normally refused). `--dry-run` verifies the artifacts and prints destination paths without installing. Remotes run one at a time and each is reported as updated, already current, or failed with the reason; a remote that fails stays on its version (the archive is checksum-verified before deploy and the remote is re-checked afterwards, never a partial binary). Exit status is 1 when any remote failed. Remotes follow the controller's version automatically unless `[updates] auto_update_remotes = false` is set (see the config reference). When the remote user cannot write the install directory (a root-owned `/usr/local/bin`), the deploy runs through `sudo -n` if the remote allows passwordless sudo; otherwise it fails with `install path <path> is not writable by <user>` and the remedy (move the binary to `~/.local/bin` behind a symlink at the old path, or run the update with sudo). `agent-deck update` on the remote itself reports the same error for that case. The deploy first resolves the install path through symlinks on the remote (`readlink` style), so the documented "symlink at the old path to `~/.local/bin/agent-deck`" layout works: the file behind the link is replaced, its owner and mode are kept (then made readable and executable for everyone), sudo is used only when the resolved file's directory is unwritable, and a symlink is never replaced by a regular file. When `command -v agent-deck` on the remote resolves to a different file than `agent_deck_path`, both are updated and the report names both, unless the `$PATH` binary is already at that version or newer, in which case it is left alone and the report says so. A file owned by another user is replaced through sudo so its owner is kept, and a non-root deploy keeps the file's group; if owner or group cannot be restored the deploy aborts with the original in place. If the remote cannot say what it runs (the `command -v`, resolve or version probe fails or answers ambiguously) nothing is written and the remote is reported as skipped with the probe error. After the deploy, `command -v agent-deck` must resolve to the deployed file's inode and report the new version. When `agent_deck_path` is set explicitly and that entry is verified by inode to be the deployed file (reporting the new version) but sits off the remote's non-interactive `$PATH`, the update counts as a success with a warning in the report (sessions started via SSH may need PATH); without an explicit `agent_deck_path` the controller itself relies on `$PATH`, so that case stays a failure. The deploy stages to a temp file unique to that run, takes a lock directory next to the binary (`<path>.lock`, treated as abandoned after 15 minutes) so two controllers cannot interleave writes; a remote whose lock another deploy holds is reported as skipped, not failed. While a sweep from this controller is still running (the TUI's startup sweep, say), `remote update --all` waits for it up to two minutes and then reports the remotes it covers as `sweep already in progress, remote <name> is being updated by <pid>` with exit status 0. The version cache is refreshed after each remote's deploy, so `remote list` shows the new version right away.

### Examples

```bash
agent-deck remote add dev user@dev-box
agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck
agent-deck remote list
agent-deck remote sessions dev
agent-deck remote attach dev my-session
agent-deck remote rename dev my-session new-name
agent-deck remote update --all    # update every remote older than this controller
agent-deck remote update dev      # update specific remote
```

SSH uses OpenSSH host-key verification and `BatchMode=yes`; unknown or changed hosts fail instead of prompting. Authenticate with an SSH agent or configured key and establish trust in `known_hosts` before registering a remote. `remote update` verifies the downloaded archive against the release checksums before deployment.

## Health Command

### health - Local runtime health

```bash
agent-deck health [--json] [--since <dur>]
```

Reads local runtime health for the selected profile: no data leaves the host. Reports per-process (TUI, notify-daemon, web) samples — CPU%, RSS, open FDs, goroutines, hook files, status-pass latency, session count, tmux calls, session-list DB latency — against the fixed performance budgets (`status_pass_ms_exclusive`, `open_fds_exclusive`, `tmux_calls_per_session`, `remote_poll_ms_exclusive`). `--since <dur>` sets the history window (default `1h`; positive Go duration, e.g. `30m`). `--json` emits the same data machine-readably.

```bash
agent-deck health --json --since 1h
```

## Inbox Commands

### inbox - Durable completion/transition records

```bash
agent-deck inbox <session-id>                          # summary for a session's inbox
agent-deck inbox drain [--json] <session-id>            # consume pending completion events
agent-deck inbox export [--json]                        # read-only: this host's records, nothing consumed
agent-deck inbox dead-letter list|show [--json]         # inspect physical dead-letter / unowned-ledger records
agent-deck inbox writer-status [--json]                 # is a notify-daemon actually recording transitions here?
```

`drain` preserves distinct turns per child and dedups re-delivery via `turn_fingerprint`; run it first on every heartbeat — reading clears the inbox. `export` is what `remote drain` runs over SSH to pull one host's records into another without consuming anything locally. `dead-letter list`/`dead-letter show` inspect records that failed to route, with raw bytes preserved for diagnosis — there is currently no `retry` or `purge` subcommand for dead-letter records (both are explicitly rejected by the CLI; a record must be handled by other means, e.g. fixing the underlying routing issue and re-draining). `writer-status` answers "is anything watching?" — without it, an empty `export` can't be told apart from a host where no notify-daemon has ever run.

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
