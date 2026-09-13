# gh-watcher: GitHub event poller for the conductor

An opt-in polling watcher that rings the conductor's doorbell when something
happens on a GitHub repository. It follows the pattern in
[`docs/WATCHER-SETUP.md`](../../docs/WATCHER-SETUP.md): the watcher notices,
dedupes and forwards a tiny trigger; the conductor decides what to do.

It is off by default. Nothing here runs until you enable it in `config.toml`
and rerun `conductor/setup.sh`.

## What it does

Every `cadence.events_seconds` (default 30) `scripts/tick.sh` runs:

1. `poll-events.sh`: `gh api /repos/<owner>/<repo>/events` with an ETag. A 304
   costs nothing and does not count against the rate limit.
2. `ingest-events.py`: new event ids go into `gh_events_seen` (SQLite, WAL).
   Dedup is the primary key, so a re-delivered event never fires twice.
3. `classify.py`: `classes.json` maps each event type to `immediate`,
   `paced`, `paced-coalesced`, `paced-digest` or `suppressed`, and builds the
   trigger text. Review comments on the same PR coalesce into one row.
4. `dispatcher.sh`: pops at most one row per tick. Immediate rows bypass the
   pacing gate; paced rows wait `dispatcher.min_interval_seconds` between
   sends. Rows are marked `sent` only after delivery succeeds (at least once).
5. `digest-flush.sh`: once a day at `dispatcher.digest_flush_local_time`,
   the digest bucket (green workflow runs and anything classed `paced-digest`) becomes one low
   priority row.

Authentication is whatever `gh` already has: `gh auth login` or `GH_TOKEN`.
No token is stored by the watcher.

## Trigger format

Triggers are lean, per `docs/WATCHER-SETUP.md`: at most 200 characters, an
identifier the conductor can fetch, no payload.

```
[github:<type>:<owner>/<repo>#<n>] <short hint>

[github:issue_opened:example-org/example-repo#2134] conductor: ship the GitHub event watcher
[github:pr_opened:example-org/example-repo#2140] fix(tui): preserve rapid SSH input
[github:pr_review_submitted:example-org/example-repo#2140] state=approved
[github:coalesced:example-org/example-repo#2140] 4 review events
[github:digest:example-org/example-repo] stars x7 in last 24h
```

`<type>` is the event name (`issue`, `pr`, `issue_comment`, `pr_review`,
`workflow_run`, ...) plus the GitHub action when there is one
(`issue_opened`, `pr_closed`). Push and workflow events identify by
`<owner>/<repo>@<sha7>`.

### Burst summary

When more than `dispatcher.high_water` rows are pending (a backfill, a bot
storm, the conductor was down), the dispatcher does not drip them out one by
one. It sends a single summary listing counts and item numbers, marks every
summarized row as sent, and the conductor triages from the live API:

```
[github:burst:example-org/example-repo] 31 pending: pr x14 (#2101 #2103 #2105 ...), issue x9 (#2130 #2131 ...), issue_comment x8
```

This is the one trigger allowed past 200 characters (capped at 400).

## Modes

| `mode`     | Behaviour |
|------------|-----------|
| `log`      | Default. Polls, classifies, queues and pops rows, but only writes `would send: ...` lines to `task-log.md`. Nothing reaches the conductor. Run this first for a day and read the log. |
| `dispatch` | `agent-deck -p <profile> session send <conductor> "<trigger>" --no-wait` for each popped row. |

`mode` lives in `watcher.toml` under `[watcher]`. The `[conductor.github_watcher]`
table in `config.toml` carries a `mode` key as well so the Go binary and
`setup.sh` can report it; keep the two in sync (the watcher scripts read only
`watcher.toml`).

## Enabling it

1. In `~/.agent-deck/config.toml` (or your XDG config path):

   ```toml
   [conductor.github_watcher]
   enabled = true
   mode = "log"
   ```

2. Run `conductor/setup.sh`. With `enabled = true` it copies this directory to
   `<conductor dir>/gh-watcher/`, writes `watcher.toml` from
   `watcher.example.toml` if none exists, and installs the scheduler:
   * macOS: `~/Library/LaunchAgents/com.agentdeck.gh-watcher.plist`
     (`StartInterval` = `cadence.events_seconds`), loaded immediately.
   * Linux: `~/.config/systemd/user/agent-deck-gh-watcher.{service,timer}`,
     then `systemctl --user daemon-reload && systemctl --user enable --now agent-deck-gh-watcher.timer`.

   With `enabled = false` (or no table) setup.sh prints one line and does
   nothing else.

3. Edit `<conductor dir>/gh-watcher/watcher.toml`: `[repo]` owner and name,
   `[routing]` profile and the exact conductor session title.

4. Watch `task-log.md` for a while in `log` mode, then switch `mode = "dispatch"`.

Run a tick by hand at any time: `AD_GH_WATCHER_HOME=<conductor dir>/gh-watcher scripts/tick.sh`.

`conductor/teardown.sh` unloads and removes the launchd unit.

## Liveness

Every successful round trip to GitHub (200 or 304) writes the epoch time to
`liveness_path` (`last-poll` next to `watcher.toml`). A quiet repository keeps
the file fresh; only a dead scheduler, a broken `gh` login or a network outage
let it age.

`scripts/status.sh` prints one line and exits 2 when the file is older than
`cadence.stale_after_seconds` (default 3 x `events_seconds`):

```
gh-watcher repo=example-org/example-repo mode=log last_poll_age=12s liveness=fresh pending=0
```

A heartbeat rule can call it and raise a NEED line on exit 2, for example:
`NEED: gh-watcher stale (last poll 412s ago); check launchctl list com.agentdeck.gh-watcher`.

## Files

```
watcher.example.toml           copy to watcher.toml; every path and name comes from here or env
schema.sql                     SQLite tables (own db file, not the agent-deck store)
classes.json                   event type -> delivery class and priority
com.agentdeck.gh-watcher.plist launchd template (setup.sh substitutes __PLACEHOLDERS__)
systemd/                       service + timer templates for Linux
scripts/watcherlib.py              config loader; `--shell` prints GHW_* exports for bash
scripts/tick.sh                    poll + dispatch + daily digest flush
scripts/poll-events.sh             ETag poll, liveness stamp, ingest, classify
scripts/ingest-events.py           JSON events -> gh_events_seen
scripts/classify.py                delivery class, trigger text, queue/digest routing
scripts/dispatcher.sh              pacing, immediate override, high_water burst
scripts/burst-summary.py           the burst message
scripts/digest-flush.sh            daily digest bucket -> one queue row
scripts/send.sh                    mode gate; GHW_SEND_CMD override for tests
scripts/status.sh                  liveness + queue depth, exit 2 when stale
```

Environment overrides: `AD_GH_WATCHER_CONFIG` (path to a `watcher.toml`),
`AD_GH_WATCHER_HOME` (directory holding `watcher.toml`), `GHW_SEND_CMD`
(replacement send command, used by tests).

## Why it was retired before

A maintainer-side copy of this poller ran on 2026-04-19 and was retired the
same day together with the webhook based `github-watcher`. The task logs do
not record a reason; the poller's own log ends with normal paced sends. The
retired webhook watcher's log shows it re-firing on one test issue for hours,
which is the kind of noise the `log` mode and the ETag dedup here are meant
to surface before anything reaches a conductor. Run `log` mode first.
