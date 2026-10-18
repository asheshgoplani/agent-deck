# Remote polling contract

This contract applies to any client that polls a remote deck through the CLI, such as the Mac app or a script. The commands involved are:

- `agent-deck remote sessions --json <remote>`
- `agent-deck remote <remote> session send|output ...`

Each poll costs one ssh session and one remote `agent-deck` process. When a poller does not wait for the previous poll to finish, a slow remote collects a stack of them, and every one of them makes the remote slower still.

## What a poller must do

1. **Single flight per remote.** Do not start a poll of a remote while the previous poll of the same remote is still running. Skip that tick or coalesce it into the poll in flight. Sends are user actions and may run beside a poll, but a client must not run two `remote sessions` against the same remote at once.
2. **Honour timeouts, and end the process you started.** Give each CLI call a deadline. When the deadline passes, send SIGTERM (or SIGINT) to the CLI process. Use SIGKILL only if the process is still alive a few seconds later. On SIGTERM the CLI kills its ssh process groups before it exits. SIGKILL cannot be handled, so the CLI cannot clean up its ssh. In that case only the remote-side `timeout` wrapper (below) ends the remote command.
3. **Back off when slow.** If a poll fails or times out, double the interval for that remote, up to a cap of about a minute. Return to the normal interval after a poll succeeds. A poll that succeeds but takes longer than the interval also means you should poll less often.
4. **Single-target commands do not list.** `remote <remote> session send|output|show ...` forwards one command in one ssh round trip, with no `list` first. `remote rename` and `remote attach` resolve their target with the remote's `session show --json <ref>`. They fall back to a plain `list --json`, never `--stats`, only when the remote lacks that command. Do not run `remote sessions` just to resolve a session you already know.

## What the CLI guarantees

- Every one-shot ssh runs in its own process group. A timeout, a cancellation, or a signal to the CLI kills the whole group.
- One-shot ssh calls pass `ServerAliveInterval=15` and `ServerAliveCountMax=3`, so ssh gives up on a dead link in about 45 s. They also keep `ConnectTimeout=10`.
- A deadline-bound remote command runs under the remote's own `timeout -k 5 <N>` when `timeout(1)` exists there. The remote process therefore ends even if ssh disappears.
- The process-group, keepalive and remote-timeout guarantees above come with #2764. The TUI's poll loop is single-flight on its own; that work is tracked separately. This document covers external pollers.
