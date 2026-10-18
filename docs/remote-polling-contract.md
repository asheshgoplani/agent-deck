# Remote polling contract

Every client that polls a remote host for its session list (the TUI, the Mac
app, scripts that call `agent-deck remote sessions --json <name>`) runs
`agent-deck list --json --stats` on that host over SSH. Each listing costs the
host a status pass: a few tmux queries and one pane capture batch, a transcript
tail scan per live Claude session, and a SQLite read. That is cheap once. It
stops being cheap when a client keeps asking while the host is already slow:
the listings start to overlap and every one makes the next one slower.

The incident that prompted this page: a host under memory pressure collected
about 105 concurrent `list --json --stats` processes, started roughly every
30 s by pollers whose earlier calls had timed out on the client side but kept
running on the host. The host only recovered after the stuck listings were
cleared by hand.

A poller must follow these four rules.

## 1. Single flight per host

Never start a poll of a host while an earlier poll of the same host is still
running, whatever triggered the new one (timer, push notification, user
refresh, focus retry). Skip it and keep showing the last good rows. Different
hosts are independent: a slow host must not delay a fast one.

The TUI keys this on the remote name (`beginRemotePoll` / `remotePollActive`
in `internal/ui/remote_poll.go`), and the slot is held until the poll has
really finished.

## 2. Timeout, and stop the remote work with it

Every poll has a deadline, `command_timeout_seconds` for the remote (default
15 s). When the deadline passes the client must end the SSH child process and
not just stop waiting for it. A poll whose SSH child outlives it still loads
the host, and it is invisible to rule 1.

The TUI runs ssh under the poll's context, so the deadline kills the ssh
child. A separate change covers making sure the listing on the host stops
with it.

## 3. Back off when slow or failing

A poll that **fails** (error, timeout, auth failure) adds a strike. The next
automatic poll waits `interval * 2^strikes`, capped at **5 minutes**:

| strikes | wait before next automatic poll (15 s interval) |
|--------:|--------------------------------------------------|
| 0 | 15 s (normal cadence) |
| 1 | 30 s |
| 2 | 60 s |
| 3 | 2 min |
| 4 | 4 min |
| 5+ | 5 min (cap) |

A poll that **succeeds but is slow** (takes longer than half the poll
interval) clears the strikes and rests the host for one interval, so the next
automatic poll waits one extra tick. It never escalates: the host answers, so
its rows must not go stale for minutes. A failure after it starts again at 1
strike.

The first **fast success** (no error, under half the interval) clears the
hold and the normal cadence resumes. While a host is held, show its last good
rows marked as stale. Do not show them as gone.

An explicit user refresh (TUI: Ctrl+R, the retry on an auth failure, or
`session focus --remote` for that host) clears the hold and polls at once,
still subject to rule 1. `remote list --retry` from another process clears it
too, through the poll-state cache. The hold is kept in memory as well, so a
failed write of that cache does not release it.

A host that is always a little slow (a large fleet over a slow link) is polled
every other tick under this rule. Raise its poll interval (`[ui]
remote_session_refresh_secs`) so that a normal poll fits in half of it.

## 4. No new poll while one runs, across processes too

Rule 1 covers one client process. A client that shells out to
`agent-deck remote sessions --json <name>` for each poll (the Mac app does)
must also keep at most one such child per host at a time. It should wait for
the child to exit, or kill it when its own deadline passes, before starting
the next one. It must also apply rule 3 to the child's outcome: a non-zero
exit counts as a strike, and a duration over half the interval rests the host
one interval.

## Where this lives

- TUI: `internal/ui/remote_poll.go` (single flight) and
  `internal/ui/remote_poll_backoff.go` (backoff), tested by
  `internal/ui/remote_poll_backoff_test.go` with a fake slow fetcher.
- Host side: the companion change to `list --json` (perf/list-json-stats)
  captures all live panes with one batched tmux call per socket, so the
  cost of one listing no longer includes one subprocess per live session.
