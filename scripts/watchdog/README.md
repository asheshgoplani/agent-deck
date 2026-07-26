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
4. **Liveness confirmation before every restart (#1705)** — the last thing before
   a pane is torn down. A restart is destructive and the `status == error`
   reading that authorizes one can be *stale* (restarts are serialized, and a
   cascade revives serially, so a queued decision can execute minutes after its
   sample) or *false* (`error` is partly derived from pane content, so a
   banner-shaped line in scrollback keeps the verdict standing while the agent
   works on). At the moment of the restart the watchdog therefore re-reads the
   status, samples the pane twice, and skips the restart when the session
   recovered, when the pane is still producing output, when the status cannot be
   read, or when the session is held for an auth failure — a restart cannot fix
   a credential. Every decision, skip or restart, is recorded in `liveness.log`
   with its readings (statuses, substates, timestamps, pane digests). Digests
   only: the log never carries pane text.

   A skipped restart does not consume the per-session rate-limit budget, so a
   session the watchdog correctly declined to touch cannot escalate as "keeps
   crashing". Three consecutive skips on a moving pane escalate as
   `liveness-mismatch` instead — that is a status-classification problem, not a
   dead session. Both that alert and the auth-hold one fire once per error
   episode, not once per poll; the counter resets when the session is next seen
   healthy.

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

The whole suite runs in well under a second. `setUpModule` redirects every log
path to a temp dir, points `AGENT_DECK_BIN` at a path that cannot exist, and
zeroes the two sleeps (`MIN_GLOBAL_RESTART_INTERVAL_S`, `LIVENESS_CONFIRM_GAP_S`).
That isolation is not optional: the defaults resolve under `~/.agent-deck`, which
on a maintainer machine is the live data directory, and the real CLI would be
invoked against real sessions. Keep any new test inside it.

## Operational notes

- **Dry-run** (`--dry-run`) logs every action instead of invoking restart /
  send / Telegram. Safe to leave running.
- **One-shot** (`--once`) executes a single safety-poll pass and exits. Useful
  from shell scripts or cron alongside the systemd service.
- Logs land in `$AGENT_DECK_ROOT/watchdog/{watchdog.log,restart.log,escalations.log,liveness.log}`.
- `liveness.log` is the first thing to read after an unexpected restart (or an
  expected one that never came): one JSON record per decision, carrying the
  readings behind it rather than only its outcome.
