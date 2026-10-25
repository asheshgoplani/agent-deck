# Example sandbox providers

A provider is a small program that makes a machine for agent-deck.
agent-deck runs it, gets back an SSH address and a key, and registers the
machine as a remote. Sessions then run there like on any other remote.

The full contract is in [docs/PLUG-IN-ANY-MACHINE.md](../../docs/PLUG-IN-ANY-MACHINE.md).

| Example | What it makes | Needs |
|---------|---------------|-------|
| [docker/](docker/) | A local Docker container with sshd, tmux and git | docker, ssh-keygen, python3 |
| [ssh-vm/](ssh-vm/) | A VM from your own controller's HTTP API (template) | curl, python3, your API |

## Configure

Add a `[providers.<name>]` table to `~/.config/agent-deck/config.toml`:

```toml
[providers.docker]
command = "/abs/path/to/agent-deck/examples/providers/docker/provider"
create_timeout_seconds = 600   # create, default 600
timeout_seconds = 60           # list, status, destroy, default 60
agent_deck_path = ""           # agent-deck path on the sandbox, default "agent-deck"
```

`command` is an absolute path or a name on `PATH`. It is run directly, never
through a shell.

## Try

```bash
agent-deck provider list
agent-deck provider test docker            # runs list
agent-deck provider test docker --create   # create, status, destroy round trip
agent-deck launch --provider docker /home/dev
agent-deck remote destroy docker-<id>
```

## Write your own

Copy one of these. A provider reads one JSON request on stdin, takes the
action from `argv[1]`, and prints one JSON response on stdout. Any language
works. Test it by hand first:

```bash
echo '{"protocol":1,"action":"list"}' | ./provider list
```
