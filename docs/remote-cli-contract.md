# Remote CLI contract

This page covers what an external poller (the Mac app, a script) must do when it drives a remote deck through the CLI, and what the CLI guarantees in return. The commands involved are:

- `agent-deck remote sessions --json <remote>`
- `agent-deck remote <remote> session send|output ...`

The polling rules themselves (single flight per remote, deadline, exponential backoff capped at 5 minutes, no new poll while one runs, across processes too) are in [remote-polling-contract.md](remote-polling-contract.md). This page adds the parts that are specific to driving the CLI as a child process.

## What a caller must do

1. **End the process you started, with SIGTERM first.** Give each CLI call a deadline. When the deadline passes, send SIGTERM (or SIGINT) to the CLI process, and SIGKILL only if it is still alive a few seconds later. On SIGTERM the CLI kills its ssh process groups before it exits. SIGKILL cannot be handled, so the CLI cannot clean up its ssh; then only the remote-side `timeout` wrapper (below) ends the remote command.
2. **Sends may run beside a poll**, as user actions, but never run two `remote sessions` against the same remote at once.
3. **Single-target commands do not list.** `remote <remote> session send|output|show ...` forwards one command in one ssh round trip, with no `list` first. `remote rename` and `remote attach` resolve their target with the remote's `session show --json <ref>`. They fall back to a plain `list --json`, never `--stats`, only when the remote lacks that command. Do not run `remote sessions` just to resolve a session you already know.

## What the CLI guarantees

- Every one-shot ssh runs in its own process group. A timeout, a cancellation, or a signal to the CLI kills the whole group.
- One-shot ssh calls pass `ServerAliveInterval=15` and `ServerAliveCountMax=3`, so ssh gives up on a dead link in about 45 s. They also keep `ConnectTimeout=10`.
- A deadline-bound remote command runs under the remote's own `timeout -k 5 <N>` when `timeout(1)` exists there. The remote process therefore ends even if ssh disappears.
- The process-group, keepalive and remote-timeout guarantees come with #2764; the TUI backoff and the polling contract with #2763.
