# Limits, Known Gotchas, and Backward Compatibility

What agent-deck is not, friction points found in real use with their workarounds, and which features need a recent deck.

## What agent-deck is NOT: a process supervisor

agent-deck manages **interactive agent sessions**. It is not a supervisor for always-on daemons or network listeners, and reaching for it as one leads to subtle failures. Before wrapping a long-lived service (a webhook listener, an SSE bridge, a `claude remote-control` server, any daemon) in a deck session, check this boundary:

- **Sessions live in a user tmux server and die with it.** An SSH logout can take every session down ([#958](https://github.com/asheshgoplani/agent-deck/issues/958) — mitigate with `loginctl enable-linger` + `launch_in_user_scope=true`, but the failure mode remains).
- **Nothing auto-restarts a crashed session** unless you run the optional watchdog daemon ([documentation/WATCHDOG.md](https://github.com/asheshgoplani/agent-deck/blob/main/documentation/WATCHDOG.md)) — and the watchdog restarts *sessions*, with session semantics.
- **`session restart` has conversation semantics, not daemon semantics.** For a Claude session it rebuilds the pane command around `claude --resume <id>` — correct for resuming a chat, wrong for "bring my listener back exactly as it was". Custom-command sessions re-run their stored wrapper, but registry drift on custom commands is a known trap ([#956](https://github.com/asheshgoplani/agent-deck/issues/956), [#911](https://github.com/asheshgoplani/agent-deck/issues/911)).

**Rule of thumb:** always-on listeners and daemons belong under the OS supervisor (launchd on macOS, systemd on Linux — the headless `web --no-tui` daemon itself is run that way, see [#1452](https://github.com/asheshgoplani/agent-deck/issues/1452)); agent-deck owns the interactive sessions and workers. When a session merely *talks to* a service, supervise the service outside the deck and keep the session disposable.

## Known Gotchas (v1.7.0+)

Friction points discovered during real usage. Work around them per the patterns below.

### `session send --no-wait` can leave prompts typed-but-not-submitted

On a freshly-launched Claude session, `agent-deck session send --no-wait <id> "..."` may paste the message into the input buffer before Claude is fully ready, leaving it TYPED but not SUBMITTED. Classic race.

**Workaround (always safe):**
```bash
agent-deck -p <profile> session send <id> "..." --no-wait -q
sleep 3
# Get the tmux session name and send Enter to submit
TMUX=$(agent-deck -p <profile> session show --json <id> | jq -r .tmux_session)
tmux send-keys -t "$TMUX" Enter
```

The Enter is idempotent — if already submitted, it's just a no-op newline. Use this pattern every time you `session send --no-wait` to a freshly-launched session.

**Alternative:** omit `--no-wait` so the built-in 60s readiness wait kicks in before submitting.

### Replacing the binary while agent-deck is running (`text file busy`)

If `/usr/local/bin/agent-deck` is a symlink to a build artifact and the binary is currently running (any tmux session, any daemon), a direct `cp` over it fails with `Text file busy`.

**Workaround — move-then-copy (keeps running processes on the old inode):**
```bash
INSTALL=$(which agent-deck)
TARGET=$(readlink -f "$INSTALL")
go build -ldflags "-X main.Version=X.Y.Z" -o /tmp/agent-deck-new ./cmd/agent-deck
mv "$TARGET" "$TARGET.old"
cp /tmp/agent-deck-new "$TARGET" && chmod +x "$TARGET"
agent-deck --version    # verify
rm "$TARGET.old"
```

Kernel tracks inodes, not names. Running processes keep a reference to the renamed inode; new invocations resolve through the original name to the new inode.

### `-c "claude <subcommand> ..."` silently rewritten — injected flags demote the subcommand (#1800)

> **This is a known bug, not intended behaviour.** Tracked as [#1800](https://github.com/asheshgoplani/agent-deck/issues/1800); a fix is in flight. Delete this entire section once that fix ships — the workaround below is a stopgap, not the supported way to run claude subcommands.

Passing a claude **subcommand** as the session command — e.g. `-c "claude remote-control --name X"` or `-c "claude mcp serve"` — does not run the command you gave. Tool detection splits it into `claude` + extra args and re-appends the extras *after* agent-deck's injected flags, so the pane runs:

```
claude --session-id <uuid> --dangerously-skip-permissions remote-control --name X
```

The subcommand becomes a positional argument of plain interactive claude; no Remote Control server (or MCP server, etc.) ever starts. This affects any claude subcommand. See [#1800](https://github.com/asheshgoplani/agent-deck/issues/1800).

**Workaround — wrap in a shell so tool detection treats the command as opaque:**

```bash
agent-deck add -t rc-server -c "bash -c 'exec claude remote-control --name X'" /path
```

The wrapped form injects nothing and runs the command verbatim. Trade-off: the session is opaque to claude session-id tracking / resume-on-restart — fine for server-style subcommands, which have no conversation to resume. Extra *flags* (e.g. `-c "claude --model opus"`) are unaffected — the wrapper-suffix path handles those correctly.

### Cross-machine config drift (macOS ↔ Linux)

If `~/.agent-deck/skills/sources.toml` (or other config files) were copied verbatim from a macOS machine, paths like `/Users/<name>/` won't exist on Linux (should be `/home/<user>/`). The symptom: `agent-deck skill list` returns "No skills found" while the pool directory is clearly populated.

**Check & fix:**
```bash
grep -n "/Users/" ~/.agent-deck/skills/sources.toml
# If any matches, substitute the Linux home path:
sed -i "s|/Users/<mac-user>|$HOME|g" ~/.agent-deck/skills/sources.toml
```

### Channel subscription for conductor/bot sessions (v1.7.0+)

For a session to receive Telegram/Discord/Slack messages as conversation turns (not just as MCP tool calls), it MUST be started with `--channels <plugin-id>`. Use the first-class field:

```bash
# At creation (preferred):
agent-deck -p personal add --channel plugin:telegram@claude-plugins-official -c claude -t my-bot /path

# Or after creation, then restart:
agent-deck -p personal session set my-bot channels plugin:telegram@claude-plugins-official
agent-deck -p personal session restart my-bot
```

The `channels` field persists and every `session start` / `session restart` rebuilds the claude invocation with `--channels`. Do NOT rely on `.mcp.json` telegram entries — those load the plugin as a regular MCP (tools only), not a channel (inbound delivery).

**Note — v1.7.0 display bug:** `agent-deck session show --json <id>` currently omits the `channels` field (fix pending). `agent-deck list --json | jq '.[] | select(.id==<id>)'` shows it correctly. Data is persisted fine regardless.

### Many competing telegram pollers after multiple session starts

Telegram's Bot API `getUpdates` is single-consumer per bot token. If N Claude sessions all load the telegram plugin, N `bun` pollers race for messages — deliveries land in whichever wins, not where you want them.

**Correct topology:** exactly ONE session loads the telegram channel plugin (normally the conductor, via `--channels` at start-time). All other sessions should NOT have telegram in their enabled plugins.

**Disable globally:** in `~/.claude/settings.json`:
```json
"enabledPlugins": {
  "telegram@claude-plugins-official": false
}
```

**Enable per-session:** via `--channel` on the specific session that should receive messages. See "Channel subscription" above.

**Debug:** `pgrep -af "bun.*telegram" | wc -l` should return 1. Anything higher means a race. Kill extras: `pkill -f "bun.*telegram"` then restart only the intended session.

### Telegram conductor topology (v1.7.22+)

**Supported topology — enforce this on every conductor host:**

- Telegram is activated **per-session** via `--channels plugin:telegram@claude-plugins-official`. This is the only supported activation path for a conductor bot.
- `TELEGRAM_STATE_DIR` is injected **exclusively** via `[conductors.<name>.claude].env_file` in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`). The env file sources deterministically on both fresh-start and `--resume` spawns.
- One bot token = one channel-owning session. Never share tokens between sessions.
- `enabledPlugins."telegram@claude-plugins-official"` in the profile `settings.json` must be **absent or false**. Global enablement makes every claude subprocess (including child agents) load the plugin.

**Codified anti-patterns — agent-deck v1.7.22 emits warnings for these:**

| Anti-pattern | Code | Why it breaks |
|---|---|---|
| `enabledPlugins."telegram@claude-plugins-official" = true` in profile settings | `GLOBAL_ANTIPATTERN` | Every claude process loads the plugin, including every child agent the conductor spawns. Each one starts a `bun telegram` poller. |
| Global enablement **AND** `--channels plugin:telegram@...` on the same session | `DOUBLE_LOAD` | The plugin loads twice in one claude process. Two bun pollers race on one bot token and Telegram rejects with 409 Conflict. |
| `session set wrapper "TELEGRAM_STATE_DIR=... {command}"` | `WRAPPER_DEPRECATED` | Works on the resume path; silently fails on fresh-start due to `bash -c` argv splitting. The env var never reaches claude, so the plugin falls back to the default state dir and two conductors collide. Use `env_file` instead. |
| Relying on `.mcp.json` telegram entries for inbound delivery | — | `.mcp.json` loads the plugin as an MCP server (tool-use only). Inbound message → conversation-turn delivery requires `--channels`. |
| Using the same bot token for multiple concurrent sessions | — | `getUpdates` is single-consumer per token. |
| Assuming an empty `TELEGRAM_STATE_DIR` is fine | — | The plugin falls back to `~/.claude/channels/telegram/`; any DM approval there leaks across unrelated conductors. |

**Verifying steady state (conductor host):**

```bash
pgrep -af 'bun.*telegram' | grep -v grep | wc -l   # expect: exactly one per conductor bot
for PID in $(pgrep -f 'bun.*telegram.*start'); do
  echo "PID=$PID TSD=$(tr '\0' '\n' < /proc/$PID/environ | grep ^TELEGRAM_STATE_DIR= | cut -d= -f2-)"
done
# Each PID must show a distinct TELEGRAM_STATE_DIR; collisions indicate env_file is not being sourced.
```

**When agent-deck emits a `⚠  GLOBAL_ANTIPATTERN` / `DOUBLE_LOAD` / `WRAPPER_DEPRECATED` warning**, the problem is in your topology, not in agent-deck. Fix the profile settings or the conductor env_file; the warning is a leading indicator of the 409-Conflict symptom that follows minutes-to-hours later.

### v1.9.x findings (filed via the self-improvement pipeline)

These were surfaced by mining real conductor transcripts (see [Self-Improvement](autonomy.md#self-improvement)). Each links to its filed GH issue.

| Symptom | Workaround | Issue |
|---|---|---|
| `agent-deck rm` driven by `xargs -P N` reports `✓ Removed` but rows persist | Sequential `while read id; do agent-deck rm "$id"; done` — never `xargs -P` for agent-deck mutations | [#961](https://github.com/asheshgoplani/agent-deck/issues/961) |
| `session send --wait --timeout 300s` still times out at 80s if recipient is busy | Poll until ready: `until [ "$(agent-deck session show --json $id \| jq -r .status)" = "waiting" ]; do sleep 10; done` | [#957](https://github.com/asheshgoplani/agent-deck/issues/957) |
| Conductor restart wipes Claude history when `claude_session_id` is empty / `"none"` | Use `tool: claude` not `tool: shell` for conductors so JSONL-resume applies | [#956](https://github.com/asheshgoplani/agent-deck/issues/956) |
| Transition-notifier replays events for *removed* sessions out of `~/.agent-deck/inboxes/<id>.jsonl` | Truncate the inbox file by hand after `rm` | [#962](https://github.com/asheshgoplani/agent-deck/issues/962) |
| Feedback dialog never shows for users with `feedback_enabled:false` (single boolean gates everything) | Manual flip in `feedback-state.json` to reopt-in | [#967](https://github.com/asheshgoplani/agent-deck/issues/967) |
| All sessions die on SSH logout (tmux server in login-session cgroup) | `loginctl enable-linger` on the host + `launch_in_user_scope=true` | [#958](https://github.com/asheshgoplani/agent-deck/issues/958) |
| Parallel `agent-deck launch` cascade → swap thrash → workers + conductor die | Sequential launches; cap parallelism; don't reach for `vm.overcommit_memory=2` (worsens it) | [#964](https://github.com/asheshgoplani/agent-deck/issues/964) |
| Orphaned context7 MCP procs (PPID=1) accumulating; `pkill -f context7-mcp` from inside the conductor self-immolates | Guard with `$$` check: `grep -q $$ <(pgrep -f "<pat>") \|\| pkill -f "<pat>"` | [#965](https://github.com/asheshgoplani/agent-deck/issues/965) |
| `launch-subagent.sh` puts children in parent's `conductor` group instead of project group | Always pass `-g <project-group>` explicitly | [#972](https://github.com/asheshgoplani/agent-deck/issues/972) |
| Bare slash commands sent via `session send` ignored on a freshly restarted child | Wrap conversationally: `"Please run /cmd …"` | [#966](https://github.com/asheshgoplani/agent-deck/issues/966) |
| `.mcp.json` plugin version pins go stale after plugin upgrade | After `/mcp` reload, rewrite `.mcp.json` from current plugin spec | [#960](https://github.com/asheshgoplani/agent-deck/issues/960) |
| Cron heartbeat `NEED:` lines repeat unchanged for 12-21h with no auto-retire | After 3 repeats, change tactic — escalate explicitly or spawn a different worker | [#971](https://github.com/asheshgoplani/agent-deck/issues/971) |
| `agent-deck launch -m "<rich text>"` short-flag parser misroutes — text after `-m` becomes positional `[path]` | Use long-form flags: `--message`, `--title`, `--group`, `--parent` | (filed in batch) |
| CLI verb inconsistency (resolved): `session update --no-parent` and `group remove` used to be rejected | Both work since #974: `session update <id> --no-parent` is an alias for `session unset-parent <id>`, and `group remove` is an alias for `group delete`. `launch` links the calling session as parent automatically; `--parent <session>` (`-p`) is accepted as an explicit override and `--no-parent` opts out | [#974](https://github.com/asheshgoplani/agent-deck/issues/974) |

See the [Self-Improvement](autonomy.md#self-improvement) section for how these were discovered and how to surface more from your own conductor's transcripts.

## Backward Compatibility

This skill is read by whatever deck version the reading session is on, including a remote running an older build — check with `agent-deck --version` before assuming a feature below exists.

| Needs ≥1.16.11 | On an older deck |
|---|---|
| `session send` `confirmation` field, `queued_socket`/`delivered`/`unverified` delivery values, `send_transport = "auto"` | Read `delivery` and `submitted` only; treat any non-`submitted` exit-0 outcome as "sent, not confirmed" and verify with `session show --json` or `output` |
| `session context` (context inspector), TUI `C` key | No context inspector at all — audit context by reading the instruction files and skills directly |
| `agent-deck health` | No local health/budget reporting — watch for slowness manually (`top`, `agent-deck status`) |
| `remote update --from-build`, `remote list --check --json` version fields | `remote update` from published releases only; `remote list` without live version drift detection |
| `remote sessions --json` bare-array/`--with-errors` split, `remote drain` | Older builds may return `null` instead of `[]` for zero sessions, and lack `remote drain`/`inbox export` entirely (a `remote drain` against one reports a version error pointing at `remote update`) |
| `inbox dead-letter list\|show` | No dead-letter inspection — a lost completion is invisible; fall back to reading transcripts / RESULTS.md for the child's actual outcome |
| `[ui.remote_preview]`/`[ui.header]` `accounts` field | Older builds show version/sessions/load only, no per-account usage in the header or remote panel |
| `session children --follow`, `--until-done` | Fall back to the until-loop in [Fanning out several children?](sub-agents.md#fanning-out-several-children) polling `--json` on an interval |
| `shell` as accepted tool alias | Use a `custom` `-c "bash -c '...'"` command instead — same effect, more typing |
| Harness identity injection (`AGENTDECK_IDENTITY_FILE`, append-system-prompt for claude/pi, developer-instructions override for codex, trust-gated context dir for gemini) | A launched session gets none of this — state its session id, tool, and how to reach its parent explicitly in the launch prompt |

Any command not in this table (session start/stop/send/output, `mcp attach`, `session set-parent`, worktrees, groups) has worked unchanged since well before this refresh.
