# Standing orders

"Check X every N minutes and wake me only when it changes." A conductor keeps its
standing orders in `ORDERS.md` in its directory. One timer per host runs
`agent-deck orders tick --all` every minute.

## The file

```markdown
## watch-main-ci
every: 15m                              # 1m minimum; m, h or d (default 15m)
active: 08:00-22:00 Europe/Berlin       # optional; zone optional (default: local)
check: gh run list -b main -L 1 --json conclusion -q '.[0].conclusion'
do: if main is red, start one worker to fix it fail-first; tell me in one line
ask_first: merge, push, posting anywhere
max_wakes_per_day: 6                    # default 6
expires: 2026-12-31                     # optional; last day the order runs
status: paused                          # optional; active is the default
```

Only `check:` is required. Any other line is a note and is kept by every edit.
A full example: [examples/standing-orders/ORDERS.md](../examples/standing-orders/ORDERS.md).

## What a tick does

For each order that is active, inside its active hours, not expired and due:

1. `next_check` moves forward **before** the check runs, so a slot never runs twice.
2. `check:` runs with `bash -lc` in the conductor directory, in its own process
   group, with a 60 s timeout and no AI. The watched value is its trimmed stdout
   plus `[exit N]` when it fails.
3. The first run records a baseline and sends nothing.
4. Same value as last time: nothing is sent, no tokens are spent.
5. Changed: one `session send` to `conductor-<name>`:

   ```text
   [ORDER watch-main-ci] its check changed.
   Was: success
   Now: failure
   Do: if main is red, start one worker to fix it fail-first; tell me in one line
   Ask the owner first (NEED: line or agent-deck ask) before: merge, push, posting anywhere.
   Nobody is watching this turn: a permission prompt is denied, so ask with NEED: instead. If this needs nothing, reply exactly NO_REPLY. Do not add or change orders from this run.
   ```

   The new value is saved only after the send was delivered. A failed send is
   retried next minute; a muted trigger leaves the change unseen.
6. After `max_wakes_per_day` wakes, one "cap reached" note is sent and further
   changes wait until the next day.

A `flock` on `<conductor dir>/.orders.lock` lets one tick run at a time; a second
one skips. State is in `orders-state.json`, notable events in `orders.log`.

## Order turns

The hook handler (Claude Code conductors; other agents get the wake text but
not these two checks) marks a turn whose prompt starts with `[ORDER ` as order-started
(and one starting with `[HEARTBEAT]` as heartbeat-started) until the turn's Stop
or the next SessionStart; the owner's next prompt clears it too.
In such a turn:

- `agent-deck orders add|pause|resume|rm` refuse with exit 2: an order cannot make
  or change orders.
- A Claude permission prompt is denied at once (nobody is there to answer it) and
  an urgent `NEED:` is queued for the owner on the conductor's human outbox.

## Commands

| Command | What it does |
| --- | --- |
| `agent-deck orders list [--json]` | Orders, next check, last wake, wakes today, silent checks |
| `agent-deck orders add <name> --check CMD [--every 15m] [--active ...] [--do ...] [--ask-first ...] [--max-wakes-per-day N] [--expires YYYY-MM-DD]` | Add an order |
| `agent-deck orders pause\|resume\|rm <name>` | Edit one order |
| `agent-deck orders tick [--all] [--json]` | Run due orders once (what the timer runs) |
| `agent-deck orders install\|uninstall` | This host's one-minute timer (`agent-deck-orders.timer`, launchd `com.agentdeck.orders` on macOS) |

`--conductor NAME` picks the conductor; by default it is the conductor running the
command, else the only conductor of the profile. `conductor setup` installs the
timer unless `--no-orders` is given; `conductor teardown --all` removes it.

The timer tags its sends `--source orders`, so `agent-deck triggers` shows it, and
every wake it sent, as `systemd:agent-deck-orders`.

## Heartbeat rules vs orders

`HEARTBEAT_RULES.md` is read only when a heartbeat fires, and a heartbeat fires only
when a child is waiting or errored or the inbox has records. Time-based checks
("every morning check X") belong in `ORDERS.md`.
