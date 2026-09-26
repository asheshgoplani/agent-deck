# Usage telemetry: opt-in, anonymous, off until you say yes

agent-deck can share anonymous usage data with its maintainer so we can see which tools and features people use, where new users get stuck, and which errors hit the most installs. Nothing is recorded or sent until you explicitly say yes, and you can turn it off at any time.

- **Off by default.** Missing, unreadable or corrupt state means off. No timer, script, environment variable, CI marker or `--yes` flag can turn it on.
- **Anonymous.** Every property comes from a fixed allow-list compiled into the binary (the tables below). Numbers are rounded into ranges. There is no free text anywhere.
- **Auditable.** `agent-deck telemetry preview` prints the exact request bodies the next upload would send; `AGENTDECK_TELEMETRY=log` writes them to a local file instead of sending anything.

## The question

On the first interactive TUI start after upgrading to a version with schema 2, you see this once (it fits an 80×24 terminal):

```
Help improve agent-deck?

Share anonymous usage data with the agent-deck maintainer.

Sent:   tools and features you use, session counts and lengths,
        the hour and weekday you are active, error types, fleet
        size, version and OS. Numbers are rounded into ranges.
        A random ID links your reports; reset it any time.
Never:  prompts, output, titles, paths, repo, host or user names,
        or anything you type. IP addresses are discarded.
Where:  a few times a day to PostHog (EU). Kept for 1 year.
Check:  agent-deck telemetry preview   (exactly what would be sent)
Off:    agent-deck telemetry off   or   DO_NOT_TRACK=1
More:   github.com/asheshgoplani/agent-deck/blob/main/TELEMETRY.md

         ▶ [ Share anonymous data ]          [ No thanks ]

   Enter: confirm highlighted · n / Esc: no · Ctrl-C: ask me later
```

| Key | Effect |
|---|---|
| Enter | Confirms the highlighted button. **Share anonymous data** is highlighted when the question opens. |
| `y` | Share. |
| `n`, Esc | No, remembered: you are not asked again. |
| Tab, Shift-Tab, ←, →, `h`, `l` | Move the highlight between the two buttons. |
| Ctrl-C | Close without answering; you are asked again at the next TUI start. The TUI keeps running. |
| anything else, pasted text | Ignored. Keys in the first 750 ms are ignored too. |

Accepting works only when the whole question is visible (terminal at least 78×22); otherwise the dialog says so and only `n`, Esc and Ctrl-C act. Your answer is written to disk before anything is recorded. After a yes: `Sharing is on. Nothing is sent before tomorrow. Turn off: agent-deck telemetry off`. After a no: `Telemetry stays off. You will not be asked again. Change later: agent-deck telemetry on`.

The question is never shown in CLI-only use, when stdin or stdout is not a terminal, in CI, in tests, inside an agent-deck session, under a coding agent (`CLAUDECODE`, `GEMINI_CLI`, `CURSOR_AGENT` or `CODEX_*` set), in `web --no-tui`, over SSH on a remote, or when any off switch below is set. If you answered no to the earlier, smaller schema 1 question (which counted every key, even Enter, as no), you are asked once more, with the line `You said no to an earlier, smaller version of this question.`; a no to this question is final. Anyone who said yes to schema 1 is asked again too, because consent is bound to the schema and the destination.

`agent-deck telemetry on` asks the same question in a shell; there it takes an explicit `y` (Enter and end-of-input mean no). In the TUI, **Settings → Privacy → Usage data** shows the state; Enter turns it off immediately or opens the question.

## Turning it off

Any of these, at any time:

| Switch | Effect |
|---|---|
| `agent-deck telemetry off` | Records a no, deletes the install id, salt, local spool and counters. It waits for an upload already in flight (at most 8 seconds); once it returns nothing further is sent. From the TUI Settings row it runs in the background, so the screen never freezes. |
| `DO_NOT_TRACK=1` | Any truthy value turns everything off ([Console Do Not Track](https://consoledonottrack.com)). |
| `AGENTDECK_TELEMETRY=0` | Any value other than `1`, `true`, `yes`, `on` or `log` turns everything off. None of these values turns telemetry on. |
| `[telemetry] disabled = true` | In `config.toml`. An unreadable config also counts as off. |

## Controls

All commands take `--json` and `--help`.

```bash
agent-deck telemetry status          # consent, install id, level, spool size, upload schedule and last result
agent-deck telemetry on              # ask the question (requires y)
agent-deck telemetry off             # turn off and delete local data
agent-deck telemetry preview         # exact PostHog request bodies the next upload would send; never creates an id
agent-deck telemetry show-last       # exact body of the last acknowledged request
agent-deck telemetry reset-id        # new random install id and salt; deletes the spool and counters
agent-deck telemetry schema          # the full allow-list (--markdown, the source of the tables below, or --json)
agent-deck telemetry level basic     # record less (see Levels); `level full` asks before recording more
```

`agent-deck doctor` prints one line with the telemetry state.

The project key never appears in `preview`, `show-last` or log-mode output: those bodies carry `"api_key":"phc_redacted"`, and the key is inserted only into the request itself.

**Log mode.** `AGENTDECK_TELEMETRY=log` never sends and never grants consent. With consent, events are recorded as usual and each upload writes its batch to `telemetry-log.ndjson` (next to `telemetry-state.json`, restarted once it would pass 4 MiB) instead of POSTing it. Without consent, every event that would be recorded is written there (TUI) or to stderr (CLI), marked `"recorded":false`.

## Levels

| Level | Records |
|---|---|
| `full` (default) | Every event in the tables below. |
| `basic` | Only `app.start`, `usage.daily` and `env.snapshot`, without `hour_local`, `weekday_local` or `ds_session`; timestamps are pinned to 12:00. |

`[telemetry] level = "basic"` in `config.toml` can lower the level but never raise it.

## When and where data is sent

- **Recording** happens in every agent-deck process where a person is at a terminal: never in CI, tests, non-TTY runs (scripts, cron, SSH commands on a remote) or before consent. Commands run by a coding agent at a terminal are recorded with `actor = agent`. At most 60 events per local day are recorded (daily rollups are exempt); the rest are counted as `dropped`.
- **Local spool.** Events are appended to `telemetry-spool.ndjson` (mode 0600) in the agent-deck data directory. It holds at most 512 KiB and 5,000 lines; days older than 14 are dropped unsent. `telemetry off` and `reset-id` delete it.
- **Upload.** Only the interactive TUI uploads: at start and then hourly it checks whether an upload is due. An upload happens at most every 6 hours, never on the day you said yes, and contains only **completed local hours** of events and **completed local days** of daily counters. Failed uploads retry after 5 minutes, 30 minutes, 2 hours, then the next day (at most 4 attempts a day, honouring `Retry-After` up to 6 hours). A rejected upload (other 4xx) stops until the next agent-deck version and its data is dropped after 3 days. `agent-deck uninstall` is the one exception: with consent it sends one `uninstall` event immediately (2 s timeout) and asks one optional question with a single key.
- **Destination.** PostHog Cloud, EU region (Frankfurt), via its public capture API: `POST <endpoint>/batch/` with `Content-Type: application/json`, hand-encoded without any SDK. The client follows no redirects, ignores proxy environment variables, sends no cookies and reads at most 1 KiB of the response. At most 500 events and 256 KiB per request, 5 requests per upload.
- **Processor.** PostHog processes the data on the maintainer's behalf. The project is configured to discard client IP addresses, GeoIP enrichment is disabled at project level and on every event (`$geoip_disable: true`), and events are personless (`$process_person_profile: false`): PostHog creates no person profiles. Data is kept for 1 year (PostHog free-plan retention). Only the maintainer has access; no dashboard is public.
- **Floating time.** Each event's `timestamp` is its local day and hour labelled as UTC (for example `2026-09-26T14:00:00Z` for 14:00 wherever you are). It reveals no timezone, and hour-of-day charts show local hours. PostHog also stores the time it received the upload, which is the upload time, not the activity time.

## Deletion

`agent-deck telemetry status` shows your install id. Reports are personless events keyed only by that random id. We cannot currently delete individual reports from PostHog by id; they expire after 1 year. `reset-id` unlinks everything recorded from now on from what was sent before, and `off` stops all further recording.

## What is never collected

- Prompts, agent output, message text (only a length range), terminal content, key sequences.
- Session titles, group, profile, conductor, remote or branch names.
- File paths, working directories, repository names or URLs, or hashes of any of these.
- Hostnames, usernames, emails, account ids, API keys, environment variable values.
- IP addresses or IP-derived location.
- Timezone, minutes, seconds, exact timestamps.
- Custom tool, MCP, skill or plugin names (sent as `other` / `custom`), model names.
- Error messages or stack traces (only an error class from a fixed list).
- Hardware ids, MAC addresses, CPU or memory details.
- Anything from CI, tests, non-TTY runs, or remotes whose own user did not consent (consent is per machine; `remote add`, `remote update` and sweeps never copy it).
- A "no" answer: declining sends nothing.

Two ids exist, both random and local: `install_id` (32 random hex characters, created on consent, rotated by `reset-id`, deleted by `off`) and `ds_session`, the first 16 hex characters of HMAC-SHA256 over the agent-deck session id with a 32-byte random salt that never leaves your machine. The real session id is never sent.

## Wiring the PostHog project key (maintainer, one time)

Builds without a project key record locally (with consent) but **never upload**; `telemetry status` shows `Upload: not configured`. To enable uploads:

1. Create the PostHog project in the **EU** region (`https://eu.posthog.com`), with IP capture set to discard, GeoIP disabled, and no billing card (or a billing limit, see below).
2. Copy the project API key (`phc_...`). It is a write-only public key: it can add events to the project but cannot read anything.
3. Provide it in one of these ways (first match wins):
   - the compiled-in default: set `defaultPostHogKey` in `internal/telemetry/config.go` in a release commit, or build with `-ldflags "-X github.com/asheshgoplani/agent-deck/internal/telemetry.defaultPostHogKey=phc_..."`. When a build has one, the two overrides below are ignored, so nothing in the environment or config can redirect its uploads to another project.
   - `AGENTDECK_POSTHOG_KEY=phc_...` in the environment (dogfooding builds without a compiled-in key),
   - `[telemetry] posthog_key = "phc_..."` in `config.toml`.
4. `[telemetry] endpoint` (default `https://eu.i.posthog.com`) is the base URL; uploads go to `<endpoint>/batch/`. It exists so a hostname we own can front PostHog later without a code change. Changing the effective endpoint asks everyone for consent again.

**Release builds.** Official release binaries carry the project key: the release workflow passes the `AGENTDECK_POSTHOG_KEY` repository secret to GoReleaser, which sets `defaultPostHogKey` through `-ldflags`. Dev, `go build` and snapshot builds have no compiled-in key and stay silent (nothing is uploaded) unless `[telemetry] posthog_key` or `AGENTDECK_POSTHOG_KEY` is set. `agent-deck telemetry status` shows where the key comes from (`Project key: compiled-in`, `environment`, `config` or `none`) and never prints the key itself.

**Volume and billing.** The free plan includes 1M events a month. The client caps each install at 60 events a day plus about a dozen daily rollups. A hard spending cap is a PostHog setting, not client code: in PostHog, Billing → set a billing limit (for example $20/month) once the dashboard shows 70% of the free quota, and a billing alert at 800k events/month.

## Published field list

<!-- schema:begin (generated by `agent-deck telemetry schema --markdown`; do not edit) -->
Schema version: 2. Every event carries the envelope; nothing outside these tables is ever recorded or sent.

#### Envelope (every event)

| Property | Type | Notes |
|---|---|---|
| `install_id` | 32 hex | random, created on consent, rotatable; sent as PostHog distinct_id |
| `schema` | int 2-2 | constant |
| `v` | pattern `^([0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}\|dev)$` | release X.Y.Z or dev |
| `os` | enum: `darwin`, `linux`, `freebsd`, `openbsd`, `netbsd`, `windows`, `other` | Go GOOS; no OS version |
| `arch` | enum: `amd64`, `arm64`, `386`, `arm`, `riscv64`, `other` | Go GOARCH |
| `day` | pattern `^[0-9]{4}-[0-9]{2}-[0-9]{2}$` | local YYYY-MM-DD |
| `hour_local` | int 0-23 | local hour; omitted at level basic |
| `weekday_local` | int 0-6 | 0 = Sunday; omitted at level basic |
| `seq` | int 0-1073741824 | per-install counter, ordering only, resets on reset-id |
| `actor` | enum: `human`, `agent` | human (TTY, not in a session) or agent (TTY inside an agent session) |
| `surface` | enum: `tui`, `cli`, `web` |  |
| `level` | enum: `full`, `basic` |  |
| `install_age` | bucket `install_age` | from the local first-seen day; the date is never sent |
| `install_week` | pattern `^[0-9]{4}-W[0-9]{2}$` | ISO week of first seen, e.g. 2026-W39 |
| `pre_v2` | bool | install first seen before the v2 build |

PostHog additionally receives `$process_person_profile: false`, `$geoip_disable: true` and `$lib: agent-deck` on every event.

#### Bucket edges

Lower edge inclusive, upper edge exclusive.

| Bucket | Values |
|---|---|
| `n` | `0`, `1`, `2-3`, `4-7`, `8-15`, `16-31`, `32-63`, `64+` |
| `dur` | `<1m`, `1-5m`, `5-15m`, `15-60m`, `1-4h`, `4-24h`, `1-7d`, `7d+` |
| `since` | `<1m`, `1-5m`, `5-30m`, `30m-1d`, `1-7d`, `7d+` |
| `len` | `<50`, `50-200`, `200-1k`, `1k+` |
| `ms` | `<50`, `50-100`, `100-250`, `250-500`, `500-1000`, `1-2s`, `2-5s`, `5s+` |
| `rating` | `1-2`, `3`, `4-5` |
| `install_age` | `d0`, `d1`, `d2-7`, `d8-30`, `d31-90`, `d91+` |

Tool bitmask order: `claude` = 0, `codex` = 1, `gemini` = 2, `opencode` = 3, `pi` = 4, `copilot` = 5, `crush` = 6, `cursor` = 7, `hermes` = 8, `deepseek` = 9, `aider` = 10, `shell` = 11; `other` = 31.

Config section bitmask order (`config_sections`): `claude` = 0, `codex` = 1, `gemini` = 2, `opencode` = 3, `cursor` = 4, `copilot` = 5, `crush` = 6, `hermes` = 7, `deepseek` = 8, `tools` = 9, `mcps` = 10, `plugins` = 11, `profiles` = 12, `groups` = 13, `conductors` = 14, `conductor` = 15, `worktree` = 16, `tmux` = 17, `docker` = 18, `remotes` = 19, `notifications` = 20, `updates` = 21, `theme` = 22, `web` = 23, `telemetry` = 24, `feedback` = 25, `experiments` = 26, `harnesses` = 27, `shell` = 28, `hotkeys` = 29, `recall` = 30; any other section = 31.

Funnel step bits (`milestones_before`): `first_run` = 0, `consented` = 1, `first_session_created` = 2, `first_session_running` = 3, `first_attach` = 4, `first_send` = 5, `second_session` = 6, `second_tool` = 7, `first_fork` = 8, `first_worktree` = 9, `first_mcp_attach` = 10, `first_group` = 11, `first_conductor` = 12, `first_remote` = 13, `first_fleet` = 14, `activated` = 15.

#### Events

**`app.start`** (tier 1, call sites 1.16.18, also at level basic): each TUI start; each human CLI command at most once per local hour

| Property | Type |
|---|---|
| `start_kind` | enum: `first_ever`, `normal`, `after_update`, `after_crash` |
| `sessions_total` | bucket `n` |
| `groups` | bucket `n` |
| `profiles` | bucket `n` |
| `remotes` | bucket `n` |
| `conductors` | bucket `n` |

**`app.exit`** (tier 1, call sites 1.16.18): TUI exit (spool write only, no network)

| Property | Type |
|---|---|
| `open_dur` | bucket `dur` |
| `exit_kind` | enum: `quit`, `signal`, `update_restart`, `panic` |

**`session.create`** (tier 1, call sites 1.16.18): session creation

| Property | Type |
|---|---|
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `via` | enum: `tui_new`, `tui_fork`, `tui_quick`, `cli_add`, `cli_launch`, `try`, `fleet`, `conductor`, `web` |
| `worktree` | bool |
| `mcps` | bucket `n` |
| `skills` | bucket `n` |
| `in_group` | bool |
| `remote` | bool |
| `parented` | bool |
| `ds_session` | 16 hex (first 16 hex of HMAC-SHA256(local salt, session id); omitted at level basic) |

**`session.end`** (tier 1, call sites 1.16.18): stop, delete, restart

| Property | Type |
|---|---|
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `end_kind` | enum: `stop`, `delete`, `tool_exit`, `crash`, `restart` |
| `lifetime` | bucket `dur` |
| `restarts` | bucket `n` |
| `ds_session` | 16 hex (first 16 hex of HMAC-SHA256(local salt, session id); omitted at level basic) |

**`activity.hourly`** (tier 1, call sites 1.16.18): once per local hour in which a TUI was open (one sampling TUI per machine)

| Property | Type |
|---|---|
| `running` | bucket `n` |
| `waiting` | bucket `n` |
| `idle` | bucket `n` |
| `error` | bucket `n` |
| `sampled_min` | bucket `n` |
| `human_active` | bool |
| `tools_running` | bitmask (32 bits) |

**`usage.daily`** (tier 1, call sites 1.16.18, also at level basic, daily rollup): one per local day with activity, built at upload time

| Property | Type |
|---|---|
| `human_hours` | bitmask (24 bits) |
| `agent_hours` | bitmask (24 bits) |
| `cli_cmds` | bucket `n` |
| `tui_starts` | bucket `n` |
| `sends` | bucket `n` |
| `attaches` | bucket `n` |
| `dropped` | bucket `n` |

**`feature.daily`** (tier 1, call sites 1.16.18, daily rollup): one per feature used that day

| Property | Type |
|---|---|
| `feature` | enum: `fork`, `restart`, `restart_all`, `rename`, `move_group`, `group_create`, `search`, `filter`, `worktree_create`, `worktree_finish`, `mcp_attach`, `mcp_detach`, `skill_attach`, `plugin_install`, `session_send`, `send_keys`, `send_queue`, `session_children`, `session_handoff`, `session_context`, `session_annotate`, `session_approve`, `inbox_drain`, `fleet_launch`, `launch`, `try`, `conductor_start`, `conductor_telegram`, `watcher`, `remote_add`, `remote_attach`, `remote_agent`, `recall_search`, `recall_timeline`, `costs`, `usage`, `limits`, `accounts_switch`, `creds_refresh`, `web_ui`, `daemon`, `notify_daemon`, `openclaw`, `deepseek`, `harness`, `doctor`, `health`, `update`, `migrate_paths`, `config_edit`, `profile_switch`, `theme_change`, `feedback`, `events`, `revive`, `session_cleanup`, `window`, `hooks_install`, `other` |
| `count` | bucket `n` |
| `errors` | bucket `n` |

**`send.daily`** (tier 1, call sites 1.16.18, daily rollup): one per (tool, via) that day

| Property | Type |
|---|---|
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `via` | enum: `tui`, `cli_send`, `conductor`, `inbox`, `telegram`, `web` |
| `count` | bucket `n` |
| `len_mode` | bucket `len` |
| `queued` | bucket `n` |

**`attach.daily`** (tier 1, call sites 1.16.18, daily rollup): one per (tool, via) that day

| Property | Type |
|---|---|
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `via` | enum: `tui`, `cli`, `web`, `remote` |
| `count` | bucket `n` |
| `total_dur` | bucket `dur` |

**`error`** (tier 1, call sites 1.16.18): per occurrence, deduped per (area, kind) per hour, max 20/day

| Property | Type |
|---|---|
| `area` | enum: `tmux`, `session_start`, `send`, `worktree`, `mcp`, `remote`, `update`, `config`, `hook`, `db`, `web`, `conductor`, `telemetry` |
| `kind` | enum: `tmux_missing`, `tmux_too_old`, `tool_not_found`, `tool_auth`, `worktree_dirty`, `mcp_spawn_failed`, `ssh_auth`, `ssh_unreachable`, `config_parse`, `db_locked`, `timeout`, `permission`, `disk_full`, `panic`, `other` |
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `before_first_success` | bool |
| `onboarding_step` | enum: `none`, `first_run`, `consented`, `first_session_created`, `first_session_running`, `first_attach`, `first_send` |

**`update`** (tier 1, call sites 1.16.18): after an update attempt

| Property | Type |
|---|---|
| `from_minor` | pattern `^([0-9]{1,3}\.[0-9]{1,3}\|other)$` (e.g. 1.16) |
| `to_minor` | pattern `^([0-9]{1,3}\.[0-9]{1,3}\|other)$` (e.g. 1.16) |
| `kind` | enum: `auto`, `manual`, `timer`, `remote_sweep` |
| `outcome` | enum: `ok`, `error`, `rolled_back` |
| `restart` | bool |

**`env.snapshot`** (tier 1, call sites 1.16.18, also at level basic): once per local day, only from the TUI

| Property | Type |
|---|---|
| `terminal` | enum: `iterm`, `ghostty`, `wezterm`, `kitty`, `apple`, `alacritty`, `vscode`, `warp`, `tmux_nested`, `other` |
| `tmux_minor` | pattern `^([0-9]{1,3}\.[0-9]{1,3}\|other)$` (e.g. 3.4, else other) |
| `shell` | enum: `zsh`, `bash`, `fish`, `other` |
| `install_method` | enum: `brew`, `go_install`, `script`, `release_tarball`, `other` |
| `color` | enum: `truecolor`, `256`, `ascii` |
| `tools_installed` | bitmask (32 bits) (tool bits found on PATH) |
| `config_sections` | bitmask (32 bits) (known config sections present) |

**`telemetry.consent`** (tier 1, call sites 1.16.18): on consent (queued like any event)

| Property | Type |
|---|---|
| `answer` | enum: `yes` |
| `source` | enum: `tui_first_run`, `tui_settings`, `cli_on` |
| `previous` | enum: `none`, `v1_granted`, `v1_declined`, `v1_undecided` |
| `prompt_variant` | enum: `v2a` |

**`onboard.baseline`** (tier 1, call sites 1.16.18): once, right after consent, computed from existing local state

| Property | Type |
|---|---|
| `install_method` | enum: `brew`, `go_install`, `script`, `release_tarball`, `other` |
| `tmux_ok` | bool |
| `tools_found` | bitmask (32 bits) |
| `had_config` | bool |
| `milestones_before` | bitmask (16 bits) |
| `sessions_total` | bucket `n` |

**`onboard.milestone`** (tier 1, call sites 1.16.18): the first time each funnel step is reached

| Property | Type |
|---|---|
| `step` | enum: `first_run`, `consented`, `first_session_created`, `first_session_running`, `first_attach`, `first_send`, `second_session`, `second_tool`, `first_fork`, `first_worktree`, `first_mcp_attach`, `first_group`, `first_conductor`, `first_remote`, `first_fleet`, `activated` |
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `via` | enum: `tui_new`, `tui_fork`, `tui_quick`, `cli_add`, `cli_launch`, `try`, `fleet`, `conductor`, `web` |
| `since` | bucket `since` |
| `before_consent` | bool |

**`uninstall`** (tier 1, call sites 1.16.18): agent-deck uninstall, only with consent; one synchronous send (2 s timeout)

| Property | Type |
|---|---|
| `sessions_total` | bucket `n` |
| `last_tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` |
| `reason` | enum: `not_needed`, `too_complex`, `bugs`, `switching_tool`, `skip` |

**`fleet.launch`** (tier 2, call sites 1.16.19+)

| Property | Type |
|---|---|
| `children` | bucket `n` |
| `tools_mix` | bitmask (32 bits) |
| `worktrees` | bool |

**`conductor.daily`** (tier 2, call sites 1.16.19+, daily rollup)

| Property | Type |
|---|---|
| `conductors` | bucket `n` |
| `children` | bucket `n` |
| `heartbeats` | bucket `n` |
| `telegram` | bool |

**`remote.op`** (tier 2, call sites 1.16.19+)

| Property | Type |
|---|---|
| `op` | enum: `add`, `attach`, `exec`, `update_sweep`, `drain` |
| `remotes` | bucket `n` |
| `outcome` | enum: `ok`, `error` |

**`worktree.op`** (tier 2, call sites 1.16.19+)

| Property | Type |
|---|---|
| `op` | enum: `create`, `finish_merge`, `finish_discard`, `cleanup` |
| `vcs` | enum: `git`, `jj` |
| `outcome` | enum: `ok`, `error` |

**`ext.op`** (tier 2, call sites 1.16.19+)

| Property | Type |
|---|---|
| `kind` | enum: `mcp`, `skill`, `plugin` |
| `op` | enum: `attach`, `detach`, `pool_add` |
| `name` | enum: `github`, `playwright`, `filesystem`, `context7`, `memory`, `fetch`, `sequential-thinking`, `exa`, `brave-search`, `postgres`, `slack`, `notion`, `custom` |
| `source` | enum: `builtin`, `pool`, `custom` |
| `count` | bucket `n` |

**`account.switch`** (tier 2, call sites 1.16.19+)

| Property | Type |
|---|---|
| `tool` | enum: `claude`, `codex`, `gemini`, `opencode`, `pi`, `copilot`, `crush`, `cursor`, `hermes`, `deepseek`, `aider`, `shell`, `other` (built-in tool, else other) |
| `trigger` | enum: `rate_limit`, `auth`, `manual` |
| `accounts` | bucket `n` |

**`search.daily`** (tier 2, call sites 1.16.19+, daily rollup)

| Property | Type |
|---|---|
| `scope` | enum: `sessions`, `recall`, `global` |
| `count` | bucket `n` |
| `zero_results` | bucket `n` |

**`tui.view.daily`** (tier 2, call sites 1.16.19+, daily rollup)

| Property | Type |
|---|---|
| `view` | enum: `home`, `new`, `fork`, `mcp`, `skill`, `group`, `settings`, `help`, `worktree_finish`, `session_picker`, `costs`, `usage`, `recall`, `inbox`, `other` |
| `count` | bucket `n` |

**`keybind.daily`** (tier 2, call sites 1.16.19+, daily rollup)

| Property | Type |
|---|---|
| `action` | enum: `new_session`, `quick_new`, `fork`, `delete`, `restart`, `rename`, `move`, `attach`, `search`, `filter`, `group_create`, `mcp_manager`, `skill_manager`, `settings`, `help`, `send`, `preview_toggle`, `worktree_finish`, `collapse_group`, `quit`, `other` |
| `count` | bucket `n` |

**`perf`** (tier 3, call sites 1.16.19+): sampled at 10% locally

| Property | Type |
|---|---|
| `op` | enum: `tui_start`, `session_start`, `list`, `status_poll` |
| `ms` | bucket `ms` |

**`crash`** (tier 3, call sites 1.16.19+)

| Property | Type |
|---|---|
| `area` | enum: `tmux`, `session_start`, `send`, `worktree`, `mcp`, `remote`, `update`, `config`, `hook`, `db`, `web`, `conductor`, `telemetry` |
| `panic_type` | enum: `nil_deref`, `index`, `slice`, `map_concurrent`, `closed_chan`, `custom`, `other` |
| `frames_hash` | 12 hex (SHA-256 over agent-deck function names of the top 8 frames; no paths, lines or values) |

**`doctor.run`** (tier 3, call sites 1.16.19+)

| Property | Type |
|---|---|
| `checks_failed` | bitmask (32 bits) |

**`feedback.rating`** (tier 3, call sites 1.16.19+)

| Property | Type |
|---|---|
| `rating` | bucket `rating` |

<!-- schema:end -->

## What we cannot see, by design

- The decline rate: a "no" sends nothing.
- Installs that never open the TUI, and people who decline.
- `activity.hourly` exists only for hours a TUI was open; CLI-only activity shows in `usage.daily.human_hours`.
- Scripted use without a terminal.

[Design and threat model](docs/TELEMETRY-DESIGN.md) describe the consent boundary, transport and verification.
