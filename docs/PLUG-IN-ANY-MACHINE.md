# Plug in any machine

agent-deck can make a fresh machine for a session, use it, and throw it away.
It does not need to know what the machine is. A small program you configure,
a **provider**, makes it. The machine can be a Docker container, a cloud VM, a
VM from your own controller, or anything else that ends up with SSH.

## What a provider is

A provider is any executable. agent-deck runs it with one action, writes one
JSON request to its stdin, and reads one JSON reply from its stdout.

There are four actions:

- `create`: make a machine and return how to reach it over SSH.
- `list`: return the machines this provider made.
- `status`: return one machine.
- `destroy`: remove one machine.

After `create`, agent-deck registers the machine as a remote with its own SSH
config. From then on it is a normal remote: you launch, attach and stop
sessions there as on any other remote. See [REMOTE-COMMANDS.md](REMOTE-COMMANDS.md).

## Quick start with Docker

The repo ships a working Docker provider in
[examples/providers/docker/](../examples/providers/docker/).

1. Add it to `~/.config/agent-deck/config.toml`:

   ```toml
   [providers.docker]
   command = "/abs/path/to/agent-deck/examples/providers/docker/provider"
   ```

2. Check it:

   ```bash
   agent-deck provider list
   agent-deck provider test docker --create
   ```

3. Start a session in a new sandbox:

   ```bash
   agent-deck launch --provider docker /home/dev
   ```

4. When you are done, remove it:

   ```bash
   agent-deck remote destroy docker-<id>
   ```

The first `create` builds the image, which takes about a minute. Later ones
take a few seconds.

## Configuration

Providers are global, like `[remotes]`:

```toml
[providers.docker]
command = "/path/to/provider"   # absolute path, or a name on PATH
create_timeout_seconds = 600    # create, default 600 (cold VMs can take ~7 min)
timeout_seconds = 60            # list, status, destroy, default 60
agent_deck_path = ""            # agent-deck path on the sandbox, default "agent-deck"
```

The table name (`docker` here) is the provider name you use on the CLI.

## The JSON contract (protocol 1)

### How agent-deck runs the provider

- argv is exactly `[command, <action>]`. Nothing else goes in argv: no names,
  ids, labels or tokens. Everything else is in the request on stdin.
- The command is run directly, never through a shell.
- The env is agent-deck's normal child env plus
  `AGENTDECK_PROVIDER_PROTOCOL=1` and `AGENTDECK_PROVIDER_NAME=<name>`.
- The provider runs in its own process group. On timeout the whole group is
  killed.
- stdout is capped at 1 MiB. stderr is capped at 64 KiB.

### Requests (stdin, one JSON object)

```json
{"protocol":1,"action":"create","name":"<remote name>",
 "labels":{"agent-deck/remote":"<name>","agent-deck/profile":"<profile>"}}
{"protocol":1,"action":"list"}
{"protocol":1,"action":"status","id":"<sandbox id>"}
{"protocol":1,"action":"destroy","id":"<sandbox id>"}
```

### Replies (stdout, one JSON object, exit 0)

```json
{"protocol":1,"sandbox":{...}}           // create, status
{"protocol":1,"sandboxes":[{...},...]}   // list
{"protocol":1,"ok":true}                 // destroy
```

### The sandbox object

```json
{"id":"sb-123",
 "host":"127.0.0.1",
 "port":2222,
 "user":"dev",
 "auth":{"type":"key","key_path":"/tmp/agent-deck-docker-x/id_ed25519"},
 "labels":{"provider":"docker"},
 "state":"running",
 "expires_at":"2026-10-26T00:00:00Z",
 "host_keys":["ssh-ed25519 AAAA..."]}
```

- `auth` is `{"type":"key","key_path":...}` or `{"type":"agent"}` (use your
  ssh-agent).
- `port` defaults to 22 when it is 0 or missing.
- `state` is optional: `running`, `starting`, `stopped` or `unknown`.
- `expires_at` is optional, RFC3339.
- `host_keys` is optional. See the security notes.

### Errors

A provider fails in one of two ways:

- Exit non-zero. agent-deck shows stderr as the message.
- Exit 0 with `{"protocol":1,"error":"message"}`.

stderr is shown only when the command fails. It is stripped of control
characters and cut short. It is never logged at info level.

Make `destroy` succeed when the machine is already gone. agent-deck uses it to
roll back a failed create.

### Protocol versioning

Every request and reply carries `"protocol":1`. A reply with another value is
rejected: "provider speaks protocol N, agent-deck speaks 1". A provider should
reject a request whose protocol it does not speak.

### Timeouts

`create` gets `create_timeout_seconds` (default 600). The other actions get
`timeout_seconds` (default 60). If your API returns before the machine boots,
poll inside `create` until it has an address.

### Validation rules

agent-deck treats the reply as untrusted and checks every field:

| Field | Rule |
|-------|------|
| `id` | `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` |
| `host` | DNS name, IPv4 or IPv6 literal, max 253. No leading `-`, whitespace, `@`, `%` or `/` |
| `port` | 1 to 65535 (0 or missing means 22) |
| `user` | `^[A-Za-z_][A-Za-z0-9_.-]{0,31}$` |
| `auth.type` | `key` or `agent` |
| `auth.key_path` | absolute, already clean, no `..`, exists, a regular file (not a symlink), no group or other mode bits (0600 or 0400), owned by you |
| `labels` | at most 32. Key `^[A-Za-z0-9][A-Za-z0-9._/-]{0,62}$`. Value printable, max 256 |
| `host_keys` | each one line, `<type> <base64>` |
| `expires_at` | RFC3339 |

A reply that fails any rule is rejected. After a failed `create`, agent-deck
destroys the sandbox.

## CLI

```bash
agent-deck provider list [--json]
```

Lists configured providers: name, command, and whether it answers.

```bash
agent-deck provider test <name> [--create]
```

Runs `list` and validates the reply. With `--create` it runs create, status and
destroy and reports each step.

```bash
agent-deck remote add --provider <name> [--name x]
```

Makes a sandbox and registers it as a remote. The default name is
`<provider>-<6 random hex digits>`, for example `docker-5e9bff`. It waits
for SSH, installs agent-deck there, and rolls everything back if a step fails.

```bash
agent-deck launch --provider <name> [--name x] <path> [launch flags...]
```

Makes a sandbox, then launches a session there like
`agent-deck remote <name> launch <path>`. The path is on the sandbox.

```bash
agent-deck remote destroy <name> [--yes] [--force]
```

Stops the sessions on the sandbox, destroys it through the provider, and
removes the remote. It works only on provider remotes; for a plain remote use
`remote remove`.

- If sessions are running there, it asks: "Stop N sessions on <name> and
  destroy the sandbox? [y/N]".
- Without a terminal it refuses unless you pass `--yes`, and changes nothing.
- If it cannot list the sessions, it refuses unless you pass `--force`.

`agent-deck remote list` shows `sandbox (provider <name>)` for these remotes.

## In the TUI

The new-session dialog has a **Where** choice:

- **this machine** (the default)
- each configured remote
- **new sandbox (<provider>)** for each configured provider

Picking a new sandbox makes one with that provider, registers it as a remote,
and starts the session there.

## Security notes

- Keys stay on disk. agent-deck reads only the key path, never the key. The
  key must be mode 0600 or 0400 and owned by you.
- Provider commands run as you, with your permissions. Only configure
  providers you trust.
- No secrets in argv or logs. agent-deck puts nothing but the action in argv.
  Providers should read tokens from env or files, not argv (the ssh-vm
  example uses a 0600 curl config file).
- Provider output is untrusted. agent-deck validates every field before it
  writes an SSH config or runs ssh.
- Host keys are pinned or trusted on first use, per sandbox. Each sandbox has
  its own `known_hosts` (0600) in the agent-deck data dir, under
  `sandboxes/<remote name>/`. When the reply has `host_keys`, they are written
  there and ssh uses `StrictHostKeyChecking yes`. Without them, ssh uses
  `accept-new` for that sandbox only.
- Every ssh call to a sandbox passes `-F <its ssh_config>`, so your own
  `~/.ssh/config` is not changed.
- Each sandbox gets its own SSH control socket, named from a hash of its
  ssh_config. A sandbox never reuses a connection opened for another remote
  with the same address, which would skip its own host key and key checks.

## Example providers

- [docker](../examples/providers/docker/): a local container, works as is.
- [ssh-vm](../examples/providers/ssh-vm/): a template for a VM controller with
  an HTTP API.

See [examples/providers/README.md](../examples/providers/README.md).

## Later (v2 notes)

These are not in protocol 1. They are ideas for later:

- **HTTP provider adapter**: point agent-deck at a URL that speaks the same
  JSON, with no local command.
- **Warm pools and claim**: keep ready machines and hand one out at once,
  with a `claim` action, instead of booting on demand.
- **Attribution per person**: tag each sandbox with who asked for it, for
  shared controllers and cost reports.
- **Expiry reaping**: use `expires_at` to warn before a sandbox expires and to
  clean up remotes whose sandbox is gone.
