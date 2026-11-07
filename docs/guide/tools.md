# Tools, costs and quotas

[Back to the README](../../README.md) . [Guide index](README.md)

## Multi-Tool Support

Agent Deck works with any terminal-based AI tool:

| Tool | Integration Level |
|------|-------------------|
| **Claude Code** | Full (status, MCP, fork, resume) |
| **Gemini CLI** | Full (status, MCP, resume) |
| **OpenCode** | Status detection, MCP, organization, fork |
| **OpenCode 2** (`opencode2`) | Status detection, MCP, model/agent selection, launch, resume, fork |
| **Codex** | Status detection, MCP, organization, conductor, fork |
| **Copilot** | Status detection, organization, launch |
| **Crush** (charmbracelet/crush) | Status detection, organization, launch |
| **Muse Code** (`muse`) | Status detection, organization, launch, resume |
| **Cursor** (terminal) | Status detection (hook-driven), MCP, organization |
| **Hermes Agent** | Status detection (hook-driven, `agent-deck hermes-hooks install`), organization, launch |
| **pi** (`pi-coding-agent`) | Status detection (hook-driven), organization, launch, resume, fork |
| **DeepSeek Harness** (`dsh`) | Status detection, organization, launch, restart, per-account `DSH_HOME` |
| **Oh My Pi** (`omp`) | Status detection, organization, launch, restart, resume, fork, project skills |
| **Custom tools** | Configurable via `[tools.*]` in config.toml |

Select `opencode2` in the new/edit session picker, web picker, or default-tool settings,
or create a session with `agent-deck add -c opencode2`. The `opencode2` launcher must
be installed on the session's host. Sessions retain OpenCode's tool identity while
preserving the selected launcher for restart and fork. V2 model/agent overrides and
forks use the session API rather than unsupported V1 TUI flags; MCP writes use
V2's `mcp.servers` structure. The global OpenCode config is read from and written to
`$XDG_CONFIG_HOME/opencode/opencode.json` when `XDG_CONFIG_HOME` is set (as OpenCode
itself does), otherwise `~/.config/opencode/opencode.json`. V2 status uses the shared service rather than
V1's per-session SSE port, with terminal fallback when service status is unavailable.
Service-based forks must run on the target host; this local fork path does not
call a local service on behalf of SSH or sandbox sessions.

Codex status detection uses Codex's notify hook. Install and verify it once for each Codex home:

```bash
agent-deck codex-hooks install
agent-deck codex-hooks status
```

If you set `CODEX_HOME`, use the same environment here and when launching Codex. Without the hook, turn-level running/waiting status cannot converge reliably.

pi status detection works the same way, through a small agent-deck extension in pi's global extension directory:

```bash
agent-deck pi-hooks install
agent-deck pi-hooks status
```

Restart running pi sessions after installing. Without the extension a pi session's status is read from the pane, which is slower and occasionally wrong; with it, agent-deck uses pi's own turn events and falls back to the pane only when no recent event is available. `PI_CODING_AGENT_DIR` is honored if you set it.

DeepSeek Harness is the `dsh` binary from [`@deepseek-ai/dsh`](https://github.com/deepseek-ai/deepseek-harness)
(`npm install -g @deepseek-ai/dsh`). It boots *profiles*: `web` (a browser UI served
from the pane) and `headless` (answer one task, print it, exit) ship in the box, and
`dsh plugin --profile <name> add <package>` installs others. Pick the profile with
`[deepseek].profile`, give each account its own `DSH_HOME` with
`[profiles.<account>.deepseek].config_dir`, and run `agent-deck deepseek status --json`
to see exactly what agent-deck resolved. dsh has no fork command, and neither shipped
profile takes a resume flag — see [docs/tools/deepseek.md](../../docs/tools/deepseek.md).

Hide tools you don't use from the new-session picker with `[ui].hidden_tools` (applies to TUI and web; `shell` is always available).

### Vendor terms and logins

**Claude Code.** Agent Deck runs the unmodified `claude` CLI with your own login and never reads, copies or reuses a login token to call Anthropic itself. Plan limits assume ordinary individual use, so for heavy many-session or unattended orchestration use an API key or a Team/Enterprise plan. For long-lived logins in sandboxes or on always-on hosts, create a token with Anthropic's own `claude setup-token` and pass it as `CLAUDE_CODE_OAUTH_TOKEN` through the existing `env_file` setting (a file containing `export CLAUDE_CODE_OAUTH_TOKEN=...`, mode 0600):

```bash
claude setup-token
```

```toml
[claude]
env_file = "~/.config/agent-deck/claude-token.env"
```

## Cost Tracking Dashboard

Track token usage and costs across all your AI agent sessions in real-time.

- **Automatic collection** — Claude Code hook integration reads transcript files on each turn. Gemini/Codex/MiniMax support via output parsing (untested)
- **15 models priced** — Claude Opus 4.6/4.7, Sonnet 4.6, Haiku 4.5, Gemini Pro/Flash, GPT-4o/4.1, o3, o4-mini, MiniMax M3/M2.7/M2.7-highspeed/M2.5/M2.5-highspeed with daily price refresh
- **TUI dashboard** — press `$` to view today/week/month costs, top sessions, model breakdown
- **Web dashboard** — `/costs` page with Chart.js charts, group drill-down, session detail views, SSE live updates
- **Budget limits** — configurable daily/weekly/monthly/per-group/per-session limits with 80% warning and 100% hard stop (untested)
- **Historical sync** — `agent-deck costs sync` backfills cost data from existing Claude transcript files
- **Recompute costs** — `agent-deck costs recompute` recalculates `cost_microdollars` for every cost event using current pricing data. Useful after a pricing-data update to retroactively price events that landed at $0 because the model was missing from the pricer. Pass `--dry-run` to preview.
- **Export** — CSV/JSON export from web dashboard
- **Dashboard data from the CLI** — `agent-deck costs daily|sessions|models|groups|budgets --json` returns the same figures and shapes as the web dashboard (`sessions`, `models` and `groups` take `--period today|7d|30d|all`). See the CLI reference for the JSON fields.

```toml
# Optional config ($XDG_CONFIG_HOME/agent-deck/config.toml;
# default ~/.config/agent-deck/config.toml)
[costs]
enabled = true        # default; false turns cost tracking off (also [profiles.<name>.costs])
retention_days = 90

[costs.budgets]
enforce = true         # refuse agent sends and wakes once a limit is spent (default false: display-only)
daily_limit = 50.00
weekly_limit = 200.00

[costs.run_budget]     # per-run defaults for goals (0 = no cap)
max_tokens = 80000
max_turns = 12
max_minutes = 30

[costs.pricing.overrides]
"custom-model" = { input_per_mtok = 1.0, output_per_mtok = 5.0 }
```

### Customizing the status-line cost segment

The home status bar shows a brief cost line drawn from the seven windows below. The default renders `$X.XX today`; configure `cost_line_template` to surface different windows or a per-profile layout. Variables substitute as `$X.XX`; unknown placeholders pass through literally so typos surface in the output.

| Variable | Window |
|---|---|
| `{cost_today}` | Today (00:00 local) |
| `{cost_yesterday}` | Prior day |
| `{cost_this_week}` | Monday-start of this week |
| `{cost_last_week}` | Prior Monday to Sunday |
| `{cost_this_month}` | First of this month |
| `{cost_last_month}` | Prior calendar month |
| `{cost_projected}` | Rolling 7-day average times 30 |

```toml
[costs]
cost_line_template = "{cost_today} today | {cost_this_week} wk"
cost_line_hide_when_zero = true   # default; hide when every recognized var is $0.00

[profiles.work.costs]
cost_line_template = "{cost_yesterday} yda | {cost_today} today | {cost_projected}/mo"
```

Resolution chain: `profiles.<active>.costs.cost_line_template > [costs].cost_line_template > hardcoded "{cost_today} today"`. Setting the template to an empty string explicitly disables the segment.

## Provider Quota (`agent-deck usage`)

Cost tracking answers "how many dollars"; this answers "how much of my
subscription is left". When you run several agents at once, the limit you hit
first is usually the provider's 5-hour or weekly plan window, not a dollar
budget.

The numbers come from the providers themselves — nothing here is estimated from
token counts.

```
$ agent-deck usage
Claude  updated 2m ago
  5h       23.5%  resets in 2h14m
  7d       41.2%  resets in 3d6h
Z.ai (pro)  updated 1m ago
  5h        0.0%
  7d       22.0%  resets in 4d3h
```

`agent-deck usage --json` prints the same report for scripting. `--refresh`
forces a fetch for pull-based providers.

**Claude** is read from the documented `rate_limits` block in the JSON Claude
Code pipes to a `statusLine` command. `agent-deck hooks install` wires the
ingester into every configured account slot's `settings.json` for you
(wrapping an existing `statusLine` command, byte-for-byte, or installing the
plain ingester when there is none); `agent-deck hooks status` reports the feed
per slot. To wire it by hand in `~/.claude/settings.json`:

```json
{"statusLine": {"type": "command", "command": "agent-deck usage ingest claude"}}
```

If you already have a status line, keep it by wrapping it — the same payload is
passed through and your command's output and exit status are forwarded verbatim:

```json
{"statusLine": {"type": "command",
                "command": "agent-deck usage ingest claude -- your-existing-command"}}
```

The quota cache keeps its existing `rate_limits` format. The latest statusline
record also keeps the Claude session ID, capture time, model ID/display name,
cwd, context percentage/capacity/input/output tokens, and five-hour/seven-day
limits. Prompt text, transcript paths and all other fields are discarded.

Run `agent-deck hooks install` to wire this automatically. No custom script or
named account is required: it includes the active Claude config directory and
configured account directories, deduplicating aliases. The installed
`usage statusline-wrap` command ingests stdin and then runs your previous command
with the same stdin, stdout, stderr and exit status. With no previous command it
prints the model plus any reported context, five-hour and seven-day percentages.
Repeated installation does not nest wrappers. `agent-deck hooks uninstall`
restores the previous command and statusline type using a private
`agent-deck-statusline-backup.json` file beside Claude's settings. Other settings
and later padding edits are retained. Unsupported or malformed statusline
settings are left unchanged with a diagnostic. The same installation works on
remote hosts after their core is upgraded and hooks are installed there.

The notify daemon and the TUI keep the wrapper current. They wire the active
Claude config only while agent-deck's hooks are installed in it, so
`hooks uninstall` stays uninstalled. To opt out entirely, set

```toml
[claude]
statusline_feed = false
```

Then no statusLine is wrapped by `hooks install` or the automatic repair, and
`hooks uninstall` still restores one wrapped earlier.

Read the last record with `agent-deck usage statusline --session <id|title> --json`.
Each ingest publishes a built-in `usage.statusline` event on the owning session's
profile bus, without requiring Mac app plugins. Follow it with
`agent-deck events follow --json --kind usage.statusline`.
Linked records are pruned with their sessions; unlinked records are capped at
128 per profile and publish with an empty agent-deck session ID. An unlinked
record goes to the feed profile or the configured default profile, only when
that profile already exists; statusline updates never create a profile.

The wrapper never fails closed: whatever goes wrong on agent-deck's side
(reading or parsing the payload, opening or writing the cache) is a warning on
stderr, and your command still runs with the same bytes on stdin, its output
and exit status forwarded. Your command does not see the `-p <slot>` the
wrapper was given: `AGENTDECK_PROFILE` reaches it exactly as your shell set it.
`hooks install` wires the ingester through the same absolute binary path the
hooks pin, so a status line keeps working across `PATH` changes; if that binary
is moved or removed, Claude Code shows a blank status line until
`agent-deck hooks install` re-pins it (the notify daemon does so on start) or
`agent-deck hooks uninstall` restores your original `statusLine`. A slot whose
config dir does not exist, or whose profile name the cache cannot use as a
directory (`[A-Za-z0-9_-]`, so `team.a` is refused), is skipped and reported
by `hooks status` as `cannot wire (...)`; nothing is created for it.

For Claude, `agent-deck usage` itself makes no network request: it reads the
cache the ingester wrote.

**Z.ai / GLM Coding Plan** is read from the monitor endpoint the vendor's own
coding plugin calls, on the host you configured in `ANTHROPIC_BASE_URL`, using
`ANTHROPIC_AUTH_TOKEN`. There is no default host: if `ANTHROPIC_BASE_URL` is
unset or does not point at a Z.ai host, the provider is skipped and no request
is made. Run `agent-deck usage` inside a session where those variables are set
(an agent-deck-launched session has already sourced its profile's env file).

Snapshots are cached under `$XDG_CACHE_HOME/agent-deck/quota/<profile>/`. A
snapshot older than the freshness bound is still shown, marked `(stale)` — a
Claude snapshot only refreshes while Claude is actually running.
