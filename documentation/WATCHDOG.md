# Watchdog: auto-restart critical sessions and nudge stuck children

The watchdog is an optional Python daemon that sits alongside agent-deck and keeps critical sessions alive. You do not need it to use agent-deck. You will want it the first time a conductor flips to `error` while you are asleep, or the first time a child session sits frozen on an unseen prompt for an hour.

It is not a replacement for the in-tree session reviver (`internal/session/reviver.go`). The reviver handles dead control pipes for sessions whose tmux is still alive. The watchdog handles three classes of failure the reviver intentionally does not:

1. A critical session that died outright (tmux gone too) and that you have pre-declared should auto-restart.
2. A Telegram bot poller that has disappeared while the conductor it belongs to is otherwise healthy: a "deaf conductor".
3. A child session that has been sitting in `waiting` with an unchanged tmux pane for longer than any child should.

If any of those sound familiar, read on.

## What ships in the repo

Since v1.7.63 the watchdog is a first-class artefact living at `scripts/watchdog/` in this repository. Previous versions only existed in a maintainer's private `~/.agent-deck/watchdog/` directory; the in-tree version is identical, sanitized, test-gated, and upgraded through normal agent-deck releases.

```
scripts/watchdog/
├── watchdog.py          # the daemon (single file, ~1100 lines of Python)
├── test_watchdog.py     # 62-test suite; TDD-gated
├── escalate.sh          # fires a Telegram message when the daemon wants to alert you
├── DESIGN.md            # full architecture, guardrails, decision log
└── README.md            # install + operational quickstart
```

No Go compilation required. Python 3.10 or newer, no third-party packages beyond the standard library.

## What the watchdog does

### Capability 1: auto-restart critical sessions

For a small allow-list of "critical" sessions, the watchdog will run `agent-deck session restart <id>` automatically whenever one flips to `error`. A session counts as critical if any of:

- its `title` starts with `conductor-`,
- its `group` is `"conductor"` or `"watchers"`,
- `is_conductor: true` in its session record,
- or you have touched an opt-in flag file at `~/.agent-deck/watchdog/autorestart/<id>`.

Every restart is bounded by guardrails:

- **Per-session rate limit.** At most three restarts per session per 300 seconds. Hitting the limit escalates to Telegram (if configured) and logs locally.
- **Global cascade guard.** At most one restart across all sessions every 60 seconds. Prevents a bad release from restart-looping the whole host.
- **429 detection.** If a restart output contains "rate limit" or "429", the watchdog backs off and escalates rather than burning quota.
- **Post-restart cool-down.** A just-restarted session is ignored for 60 seconds so the reviver and watchdog do not double-fire (issue #30 is also guarded on the Go side).

Sessions whose status is `stopped` are excluded: "stopped" means you deliberately stopped it, so auto-restart would fight the user.

### Capability 2: Telegram poller liveness (v1.7.63)

For every conductor session with `plugin:telegram@claude-plugins-official` in its channels list, the watchdog verifies a `bun ... telegram ...` subprocess is running and owns the expected `TELEGRAM_STATE_DIR`. The state dir is read from the conductor's `env_file` in `config.toml`.

If a conductor has the channel attached but no bun process owns its state dir, the watchdog fires `agent-deck session restart <id>`. Dedupe window: one restart per hour per conductor.

This catches the "deaf conductor" failure: tmux is alive, Claude is alive, you can `session send` to it just fine, but messages to the Telegram bot go nowhere because the plugin's poller died quietly and nothing in the existing stack notices.

### Capability 3: waiting-too-long patrol (v1.7.63)

For every child session (one with `parent_session_id` set), the watchdog takes a SHA-256 hash of its tmux pane output every safety-poll tick. If the session sits in `status=waiting` with an unchanged pane hash for longer than 10 minutes, the watchdog injects a gentle `report status?` nudge via `agent-deck session send --no-wait`. Dedupe: one nudge per hour per session.

Pane change resets the timer. A status change (e.g. `waiting` → `running`) evicts the tracker entry entirely.

This catches the other quiet failure mode: a child that finished its work, asked the user a yes-or-no question via Claude, and now sits invisibly in `waiting` status because no one is looking at its pane. The nudge makes the child speak up in its output, which the conductor or its watcher will pick up.

## What the watchdog will not do

- Create sessions. It only operates on sessions agent-deck already knows about.
- Merge PRs, push code, trigger deploys. It is a session-health tool; authorization boundaries are kept narrow on purpose.
- Restart itself. The systemd/launchd unit owns that.
- Guess the user's intent on ambiguous state. When rate-limited or confused, it escalates rather than retrying.
- Touch arbitrary worker sessions. Auto-restart is opt-in: either the session declares itself (conductor title, group, `is_conductor`) or you flag it explicitly.

## Installation

### Linux (systemd --user)

```bash
# 1. Stage the daemon under your runtime root.
mkdir -p ~/.agent-deck/watchdog
install -m 755 scripts/watchdog/watchdog.py ~/.agent-deck/watchdog/watchdog.py
install -m 755 scripts/watchdog/escalate.sh ~/.agent-deck/watchdog/escalate.sh

# 2. Dry-run to sanity-check the install.
python3 ~/.agent-deck/watchdog/watchdog.py --once --dry-run --verbose

# 3. Install the user-level systemd unit.
cat > ~/.config/systemd/user/agent-deck-watchdog.service <<'EOF'
[Unit]
Description=agent-deck watchdog
After=default.target

[Service]
ExecStart=/usr/bin/python3 %h/.agent-deck/watchdog/watchdog.py
Restart=always
RestartSec=5
Environment=AGENT_DECK_BIN=/usr/local/bin/agent-deck

[Install]
WantedBy=default.target
EOF

systemctl --user daemon-reload
systemctl --user enable --now agent-deck-watchdog.service

# 4. Check it came up.
systemctl --user status agent-deck-watchdog.service
journalctl --user -u agent-deck-watchdog -n 50
```

Linger needs to be enabled if you want the watchdog to run while you are logged out: `sudo loginctl enable-linger $USER`.

### macOS (launchd)

```bash
mkdir -p ~/.agent-deck/watchdog
install -m 755 scripts/watchdog/watchdog.py ~/.agent-deck/watchdog/watchdog.py
install -m 755 scripts/watchdog/escalate.sh ~/.agent-deck/watchdog/escalate.sh

cat > ~/Library/LaunchAgents/com.agentdeck.watchdog.plist <<'EOF'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>                <string>com.agentdeck.watchdog</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/bin/python3</string>
    <string>/Users/YOUR_USER/.agent-deck/watchdog/watchdog.py</string>
  </array>
  <key>RunAtLoad</key>            <true/>
  <key>KeepAlive</key>            <true/>
  <key>StandardOutPath</key>      <string>/tmp/agent-deck-watchdog.stdout.log</string>
  <key>StandardErrorPath</key>    <string>/tmp/agent-deck-watchdog.stderr.log</string>
</dict>
</plist>
EOF

launchctl load -w ~/Library/LaunchAgents/com.agentdeck.watchdog.plist
```

### Cron fallback

If neither systemd nor launchd is available, a cron entry running the watchdog in `--once` mode every minute is a reasonable degradation:

```
* * * * * /usr/bin/python3 $HOME/.agent-deck/watchdog/watchdog.py --once >> $HOME/.agent-deck/watchdog/cron.log 2>&1
```

You lose the inotify fast-path (a restart then takes up to 60 s instead of ~100 ms), but all three capabilities still function.

## Configuration

All configuration is through environment variables. There is no config file.

| Env var | Purpose | Default |
|---|---|---|
| `AGENT_DECK_ROOT` | Where hooks, logs, and watchdog state live. | `~/.agent-deck` |
| `AGENT_DECK_BIN` | Path to the `agent-deck` binary. Set this explicitly if the watchdog runs outside your login `$PATH`. | `/usr/local/bin/agent-deck` |
| `AGENT_DECK_PROFILE` | The profile to use for `agent-deck list` / `session show` / `session restart`. | `default` |
| `TELEGRAM_ESCALATION_CHAT_ID` | Chat ID the watchdog sends its own alerts to (rate-limit hits, 429s, escalations). **Empty by default: without it, escalations only log locally.** | `""` |
| `POLLER_RESTART_DEDUP_S` | Dedupe window for poller-liveness restarts. | `3600` |
| `WAITING_PATROL_THRESHOLD_S` | How long a child must sit in `waiting` with unchanged pane before a nudge. | `600` |
| `WAITING_PATROL_NUDGE_DEDUP_S` | Dedupe window for waiting-too-long nudges. | `3600` |
| `MIN_GLOBAL_RESTART_INTERVAL_S` | Global cascade guard. Do not lower. | `60` |

Full detail, including internal constants and why they are what they are, is in `scripts/watchdog/DESIGN.md` alongside the daemon.

## Expected output

In normal operation the watchdog is silent. The service runs, the logs grow slowly, nothing happens because nothing needs to happen.

When something fires, log lines land in `$AGENT_DECK_ROOT/watchdog/`:

- `watchdog.log`: general runtime events (safety-poll ticks in verbose mode, state transitions, dedupe decisions).
- `restart.log`: every restart attempt, with the decision path (which capability triggered, which session, why).
- `escalations.log`: entries for each Telegram escalation, whether it was sent or rate-limited.

If you configured `TELEGRAM_ESCALATION_CHAT_ID`, rate-limit hits and repeated-failure escalations go to that chat in addition to logging.

## Running the tests

The watchdog has its own TDD gate.

```bash
cd scripts/watchdog
python3 -m unittest test_watchdog -v
```

The full 62-test suite takes about nine minutes because several tests exercise the global restart serialization. When iterating on the v1.7.63 additions only:

```bash
python3 -m unittest \
  test_watchdog.TestPollerExistence \
  test_watchdog.TestBunTelegramStateDirs \
  test_watchdog.TestWaitingTooLong
# Runs in under a second.
```

Any change to `scripts/watchdog/watchdog.py` or its helpers needs the full suite green before it lands.

## Troubleshooting

**The service is running but nothing happens when I kill a conductor.** Check that the conductor meets the critical-session criteria (title prefix, group, `is_conductor`, or opt-in flag). If not, the watchdog ignores it by design.

**Restarts fire in a loop.** The per-session rate limit should prevent this. If it does not, set `--verbose --dry-run` on the daemon and watch a cycle: either the dedupe is miscomputing (file a bug), or the root cause is a consistently-broken session that needs a real fix, not auto-restart.

**The poller-existence check keeps firing.** The most common cause is that `enabledPlugins.telegram@claude-plugins-official` is not `true` in the profile's `settings.json`, so the bun poller never starts in the first place. Restarting the conductor does not help. Enable the plugin under the right `CLAUDE_CONFIG_DIR` and the restart will stick.

**Waiting-too-long patrol is noisy.** Either the child sessions genuinely are stuck (the watchdog is doing its job: fix the root cause), or your workload has naturally long idle periods and 10 minutes is too short. Raise `WAITING_PATROL_THRESHOLD_S` in the systemd unit's `Environment=` lines.

## Related docs

- [CONDUCTOR.md](CONDUCTOR.md): the sessions the watchdog primarily protects.
- `scripts/watchdog/DESIGN.md`: architecture, guardrails, decision log. Read this before modifying the daemon.
- `scripts/watchdog/README.md`: operational quickstart that lives next to the daemon.
