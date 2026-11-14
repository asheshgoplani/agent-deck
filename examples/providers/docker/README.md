# Docker provider (example)

Makes one local Docker container per sandbox. Each container runs sshd
(key auth only) with a user `dev`, tmux, git, curl and Claude Code, and
comes up ready to work: the project folder is there and Claude's first-run
screens are done, so a claude session on it shows Claude's prompt at once.

## What a new sandbox has

- `claude` (Claude Code, native installer, `stable` by default; build arg
  `CLAUDE_CODE_VERSION`) and `agent-deck`, both linked into `/usr/local/bin`
  so a non-login ssh command finds them. Claude's auto-updater is off.
- git, with a commit identity for `dev`.
- The project at `/home/dev/<project>`: a clone of `project_git`, a copy of
  `project_dir`, or, when neither is set, the small demo project at
  `/home/dev/demo` (from [demo-project](demo-project)).
- `~/.claude.json` with onboarding done and the project (and `/home/dev`)
  trusted, so no theme or trust prompt blocks the first session. The
  one-time "Bypass Permissions mode" screen is accepted as well, because
  agent-deck on the sandbox starts claude with `--dangerously-skip-permissions`
  by default and this container is the disposable machine that mode is for.
- Optional: a Claude login token, see below. Without one, the first claude
  session shows Claude's login screen.

The sandbox's labels say what it got: `project=/home/dev/<name>` and
`claude-login=token|none`.

## How it works

- `create` builds the image `agent-deck-sandbox:p1-<hash>` once from the
  [Dockerfile](Dockerfile) here. The hash covers the build files, so an edited
  Dockerfile gets a new image on the next `create`.
- It makes a fresh key dir with `mktemp -d` under `$TMPDIR` (default `/tmp`),
  mode 0700, and an ed25519 key in it, mode 0600. The private key never leaves
  that dir. Only the public key is copied into the container.
- The container is labelled `agent-deck.provider=docker` and publishes port 22
  on `127.0.0.1` at a random free port.
- Each container makes its own host keys at start. The provider reads the
  ed25519 host key and returns it, so agent-deck pins it.
- `create` waits up to 60 seconds for the SSH banner, then prints the sandbox.
- `list` shows only containers with the label. `status` and `destroy` refuse a
  container without it.
- `destroy` runs `docker rm -f` and deletes the key dir. Destroying a container
  that is already gone succeeds.
- Ids are checked against `^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$` before they
  reach docker.

## Configure

```toml
[providers.docker]
command = "/abs/path/to/agent-deck/examples/providers/docker/provider"
```

Optional env: `AGENTDECK_SANDBOX_IMAGE` (image tag), `AGENTDECK_SANDBOX_WAIT`
(seconds to wait for sshd, default 60), `AGENTDECK_SANDBOX_CONFIG` (the
provider config file below).

### Provider config: project and Claude login

The provider reads `~/.config/agent-deck/docker-sandbox.conf` (or
`$XDG_CONFIG_HOME/agent-deck/docker-sandbox.conf`, or the file named by
`AGENTDECK_SANDBOX_CONFIG`). Every key is optional; the file is parsed, never
sourced. See [docker-sandbox.conf.example](docker-sandbox.conf.example).

```ini
# One of these two (neither = the demo project):
project_git = https://github.com/you/repo.git   # cloned with --depth 1 (public, or reachable without a prompt)
# project_dir = /home/you/src/repo              # copied in at create
project_name = repo                             # folder under /home/dev (default: from the URL or folder)

claude_token_file = /home/you/.config/agent-deck/claude-token
agent_deck_binary = /home/you/bin/agent-deck     # copied in, so no release download at create
```

Each key can also come from env (`AGENTDECK_SANDBOX_PROJECT_GIT`,
`AGENTDECK_SANDBOX_PROJECT_DIR`, `AGENTDECK_SANDBOX_PROJECT_NAME`,
`AGENTDECK_SANDBOX_CLAUDE_TOKEN_FILE`, `AGENTDECK_SANDBOX_AGENT_DECK_BINARY`);
env wins over the file.

`agent_deck_binary` copies a local agent-deck (built for linux and the
sandbox's CPU) into `~/.local/bin` at create. Without it, agent-deck downloads
its release onto the sandbox from GitHub, which fails when the anonymous
GitHub API limit (60 requests an hour per IP) is used up.

`claude_token_file` holds a token from `claude setup-token` (a bare token or a
`CLAUDE_CODE_OAUTH_TOKEN=...` line). The provider refuses the file unless it
is a regular file you own with mode 0600 or 0400 that holds exactly one line,
the token: no comment, no blank line. Its errors never show the file's
content, and neither do the setup script's. At create it pipes the
token into the running container (never argv, never the image, never a log),
where the setup script writes it to `~/.config/agent-deck-sandbox/claude.env`
(0600) and sets `[claude] env_file` in the sandbox's agent-deck config, so
every claude session agent-deck starts there gets `CLAUDE_CODE_OAUTH_TOKEN`.
Anyone who can run commands in the sandbox can read the token: use a token
made for sandboxes, not your own login.

## Try it by hand

```bash
P=./provider
echo '{"protocol":1,"action":"create","name":"try","labels":{}}' | $P create
echo '{"protocol":1,"action":"list"}' | $P list
echo '{"protocol":1,"action":"status","id":"<id>"}' | $P status
ssh -F /dev/null -i <key_path> -p <port> dev@127.0.0.1 true
echo '{"protocol":1,"action":"destroy","id":"<id>"}' | $P destroy
```

The first `create` builds the image (Claude Code is a ~200 MB download) and
takes about a minute. Later ones take a few seconds.

## Notes

- Key dirs live under `$TMPDIR`. If it is cleared (for example on reboot),
  `list` still shows the container, with auth `agent` and the label
  `key-missing=true`, so you can destroy it.
- Setup runs `agent-deck-sandbox-setup` (from [sandbox-setup.sh](sandbox-setup.sh))
  in the new container as `dev`, with its input on stdin.
- To change the image, edit the Dockerfile. The next `create` builds the new
  tag; remove old ones with `docker image rm agent-deck-sandbox:p1-<old hash>`.
- agent-deck installs itself on the sandbox into `~/.local/bin`. The image links
  `/usr/local/bin/agent-deck` there, so a non-login ssh command finds it.
