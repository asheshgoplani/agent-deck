# Remote machines and the web UI

[Back to the README](../../README.md) . [Guide index](README.md)

## Remote Instances

Manage agent-deck instances running on remote SSH servers from your local terminal. Remote sessions report coarse live status and use the same nested group layout as local sessions; remote groups can be collapsed, and `K`/`J` reorder sessions within a remote group. Session identity includes its location, so the same title can safely exist locally and on different remote host/path pairs.

```bash
# Register a remote
agent-deck remote add dev user@dev-box

# agent-deck is installed automatically if missing on the remote
agent-deck remote add prod user@prod-server --agent-deck-path /usr/local/bin/agent-deck

# List configured remotes
agent-deck remote list

# Browse sessions across all remotes (or one specific remote)
agent-deck remote sessions
agent-deck remote sessions dev

# Attach to a remote session
agent-deck remote attach dev my-session

# Pull finished/stalled reports from a remote into this machine's inbox
agent-deck remote drain dev

# Keep remote binaries up to date
agent-deck remote update --all    # every remote older than this controller
agent-deck remote update dev      # specific remote
agent-deck remote list            # includes each remote's version, ↑ when behind
```

By default the controller pushes its version to older remotes on its own: after `agent-deck update`, and in the background on startup (`[updates] auto_update_remotes = false` in `config.toml` opts out). The TUI shows `v1.15.0 ↑` on a remote header that is behind; `u` on that header updates it after a confirmation. A remote whose binary lives in a directory its user cannot write (a root-owned `/usr/local/bin`) is updated through passwordless `sudo -n` when the remote grants it; otherwise the update reports `install path <path> is not writable by <user>` and the fix: move the binary to `~/.local/bin` behind a symlink at the old path, or run the update with sudo. That symlink layout is supported by the deploy itself: the install path is resolved through symlinks on the remote first, the file behind the link is what gets replaced (owner and mode kept, sudo only if *that* directory is unwritable), a symlink is never overwritten by a regular file, and afterwards `command -v agent-deck` must resolve to the deployed file. If the remote's `$PATH` binary and `agent_deck_path` are different files, both are updated and the report says so.

Unattended installs (the daily timer, and any long-running agent-deck's `check_interval` poll — default 90s) work the other way around: the controller *nudges* each remote to check for the release right now instead of pushing bytes to it, so remotes pull and verify themselves, same as `agent-deck update` on that host would (`[updates] sweep_remotes = true` restores the old push behavior). A remote too old to understand the nudge gets a blocking fallback update instead, so it still ends up current either way.

A conductor that launches workers on another host does not get their completions for free: transition notifications are parent-linked, and a `parent_session_id` cannot point across machines. Run `agent-deck remote drain <name> --into <conductor-session-id>` to pull the remote's records over SSH into that conductor's local inbox. The remote records are not consumed, and repeated drains are deduplicated locally. A new inbox record wakes the heartbeat; the heartbeat does not pull remote records automatically.

Remote polling runs in the background. A failed poll shows its reason (`auth failed`, `timeout`, `host down`, or `poll failed`) beside the remote. An authentication failure pauses automatic polling of that remote for 2 minutes, across restarts; after that the next poll tries once more, so a transient failure (for example an ssh-agent that had not loaded its key yet) heals on its own, while a broken credential is tried only once per 2 minutes. Latency and preview probes stay paused until a poll succeeds. To resume at once, authenticate and run `agent-deck remote list --retry`. This clears cached poll state for all configured remotes without opening SSH; add `--check` for an explicit version check. The next TUI refresh resumes polling.

`remote list --json` includes `last_poll_ms`, `last_poll_status`, and `last_poll_error`. An unobserved remote has `null` latency, status `unknown`, and an empty error. Other statuses are `ok`, `auth_failed`, `timeout`, `host_down`, and `error`. These fields describe the last session-list poll, independently of the version cache. To inspect or reset the remotes configured on another host, use `agent-deck remote exec <host> remote list --json` or append `--retry`.

Remote configuration is stored under `[remotes]` in `$XDG_CONFIG_HOME/agent-deck/config.toml` (default `~/.config/agent-deck/config.toml`). `remote list`, `remote sessions` and `remote drain` support `--json` output for scripting. See the [Remote Commands reference](../../skills/agent-deck/references/cli-reference.md#remote-commands) for flags, security behavior, and examples.

Pressing `n` on a remote group or session opens the full new-session dialog in **remote mode**: path suggestions come from the remote host, the remote session's group is pre-filled, and the create routes over SSH with your chosen tool — sessions are never accidentally created on localhost.

## Web Mode

Open the left menu + browser terminal UI:

```bash
agent-deck web
```

Read-only browser mode (output only):

```bash
agent-deck web --read-only
```

Change the listen address (default: `127.0.0.1:8420`):

```bash
agent-deck web --listen 127.0.0.1:9000
```

Protect API + WebSocket access with a bearer token:

```bash
agent-deck web --token my-secret
# then open: http://127.0.0.1:8420/?token=my-secret
```

A valid tokened visit stores an HttpOnly, SameSite=Strict cookie (Secure when
reached over HTTPS, including through a proxy that sends
`X-Forwarded-Proto: https` such as Tailscale Serve) that authenticates the API, the event streams and
the terminal WebSocket, so refreshes and an app installed to the home screen
keep working. The cookie lasts 30 days and each app launch renews it; changing
the token signs every device out. A token containing `"`, `,`, `;`, a
backslash or non-ASCII characters gets no cookie, so use a hex token like the
one below. The server accepts requests addressed to
localhost, loopback IPs, its listen address, and its machine name when bound
to a network interface. A wildcard bind also accepts the machine's current
interface IPs. For a reverse proxy or Tailscale Serve hostname, add
the exact name with `--allowed-host machine.tailnet.ts.net` (repeatable) or
`[web] allowed_hosts`. An entry with a port permits only that port.

For headless deployments, read the token from a file instead so it never
appears in the process arguments, where any local user can read it from
`/proc`. The file must be a regular file that is not group- or world-readable,
and must hold the token on a single line:

```bash
install -m 600 /dev/null ~/.config/agent-deck/web-token
printf '%s' "$(openssl rand -hex 32)" > ~/.config/agent-deck/web-token
agent-deck web --no-tui --listen 0.0.0.0:8420 --token-file ~/.config/agent-deck/web-token
```

`--token` and `--token-file` are mutually exclusive. Binding a non-loopback
address without one of them is refused, because it would expose an
unauthenticated remote-code-execution surface. MCP administration over the
HTTP API is only available when a token is configured; without one those
routes stay unavailable.

Browser push notifications tell you when a session needs you while the tab is
in the background:

```bash
agent-deck web --push
```

Then open the Tweaks panel (gear icon, top right) and turn on
**Notifications**. The browser asks for permission once, subscribes with the
server's VAPID key, and keeps the subscription across reloads; notifications
only fire while the tab is unfocused. The switch is hidden when the server runs
without `--push` or the browser has no Push API. Push needs a secure context:
`http://127.0.0.1` or `localhost`, or HTTPS (for example Tailscale Serve).

The browser UI includes the live Command Center, session terminal, costs, archive, and settings views. See [Command Center](../../docs/COMMAND-CENTER.md) for the fleet view; use `--read-only` when browser clients should not mutate sessions.

## Fleet board: semantic status

The web UI's **Fleet** tab shows one card per group by default. Its
**Status | Groups** toggle switches to a kanban of what each session's *work*
is doing, taken from its `status` annotation; the browser remembers the
choice. The kanban is not grouped by whether the agent process is running: a
second row of tiles counts its columns, the Running / Waiting / Error / Idle
tiles stay, and runtime state appears on each card as a small dot (hover for
`process: running` etc.).

```bash
agent-deck session annotate <id> --hint status=needs-input \
  --hint goal="…" --hint state="…" --decision "…" --ticket ENG-123
```

| Column | `status` value | Also accepted |
|---|---|---|
| Needs input | `needs-input` | `needs-you`, `needs-human`, `blocked`, `waiting`, `waiting-on-you`, `waiting-for-input` |
| Ready for review | `ready-for-review` | `review`, `in-review`, `needs-review` |
| In progress | `in-progress` | `working`, `active`, `wip`, `in-flight` |
| Parked | `parked` | `paused`, `on-hold`, `hold`, `deferred`, `backlog` |
| Done | `done` | `complete`, `completed`, `finished`, `closed`, `merged`, `shipped` |
| Untriaged | *(unset or any other value)* | |

Values are case-insensitive, and spaces or underscores count as hyphens. A set
status always wins over runtime state. A parked or done session whose process
has stopped shows only a quiet "process not running" note. Cards show the
`goal`, `state` and `decision` hints as **Goal / Current state / Decision**
(`decision` is a decision already taken, as `--decision` records it), falling
back to `headline`. In the Status view, conductor sessions
are pinned above the board, and their `note` (or `summary`) hint is rendered
there as markdown.
