# agent-deck watchdog

Python daemon that keeps critical agent-deck sessions alive. Ships in-tree from
v1.7.63 onward so every user gets it via normal releases.

See `DESIGN.md` for architecture.

## What it does

1. **Auto-restart** critical sessions (conductors, `meeting-watcher`, `gmail-watcher`,
   `agent-deck`, explicit opt-ins) when they flip to `error` — with per-session
   rate limit (3 per 10 min), global cascade guard, 429 detection, and Telegram
   escalation.
2. **Poller-existence check (v1.7.63)** — for each conductor session with
   `plugin:telegram@claude-plugins-official` attached, verify a matching
   `bun ... telegram ...` subprocess is running and owns the expected
   `TELEGRAM_STATE_DIR`. Fires `agent-deck session restart <id>` if missing
   (max one per hour per conductor).
3. **Waiting-too-long patrol (v1.7.63)** — for each child session
   (`parent_session_id` set) stuck in `status=waiting` with an unchanged tmux
   pane for >10 min, inject `report status?` via `agent-deck session send`
   (max one nudge per hour per session).

## Install

```bash
# 1. Copy the daemon into your runtime dir:
mkdir -p ~/.agent-deck/watchdog
install -m 755 scripts/watchdog/watchdog.py  ~/.agent-deck/watchdog/watchdog.py
install -m 755 scripts/watchdog/escalate.sh  ~/.agent-deck/watchdog/escalate.sh

# 2. Sanity check:
python3 ~/.agent-deck/watchdog/watchdog.py --once --dry-run --verbose

# 3. Wire up a systemd --user unit (example):
cat >~/.config/systemd/user/agent-deck-watchdog.service <<'EOF'
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
```

## Configuration (env vars)

| Env var | Purpose | Default |
|---|---|---|
| `AGENT_DECK_ROOT` | Where hook files + logs live | `~/.agent-deck` |
| `AGENT_DECK_BIN`  | Path to the `agent-deck` binary | `/usr/local/bin/agent-deck` |
| `TELEGRAM_ESCALATION_CHAT_ID` | Chat ID for watchdog's own escalation alerts. **Empty by default — set it or escalations log locally only.** | `""` |

## Running tests

```bash
cd scripts/watchdog
python3 -m unittest test_watchdog -v
# or:
pytest test_watchdog.py
```

The full suite takes ~9 minutes because several tests exercise the global
restart serialization (`MIN_GLOBAL_RESTART_INTERVAL_S=60`). For iterating on
the v1.7.63 additions specifically:

```bash
python3 -m unittest \
  test_watchdog.TestPollerExistence \
  test_watchdog.TestBunTelegramStateDirs \
  test_watchdog.TestWaitingTooLong
# Runs in <1s.
```

## Operational notes

- **Dry-run** (`--dry-run`) logs every action instead of invoking restart /
  send / Telegram. Safe to leave running.
- **One-shot** (`--once`) executes a single safety-poll pass and exits. Useful
  from shell scripts or cron alongside the systemd service.
- Logs land in `$AGENT_DECK_ROOT/watchdog/{watchdog.log,restart.log,escalations.log}`.
