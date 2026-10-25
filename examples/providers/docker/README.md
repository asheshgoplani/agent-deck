# Docker provider (example)

Makes one local Docker container per sandbox. Each container runs sshd
(key auth only) with a user `dev`, tmux, git and curl.

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
(seconds to wait for sshd, default 60).

## Try it by hand

```bash
P=./provider
echo '{"protocol":1,"action":"create","name":"try","labels":{}}' | $P create
echo '{"protocol":1,"action":"list"}' | $P list
echo '{"protocol":1,"action":"status","id":"<id>"}' | $P status
ssh -F /dev/null -i <key_path> -p <port> dev@127.0.0.1 true
echo '{"protocol":1,"action":"destroy","id":"<id>"}' | $P destroy
```

The first `create` builds the image and takes a minute. Later ones take a few
seconds.

## Notes

- Key dirs live under `$TMPDIR`. If it is cleared (for example on reboot),
  `list` still shows the container, with auth `agent` and the label
  `key-missing=true`, so you can destroy it.
- To change the image, edit the Dockerfile. The next `create` builds the new
  tag; remove old ones with `docker image rm agent-deck-sandbox:p1-<old hash>`.
- agent-deck installs itself on the sandbox into `~/.local/bin`. The image links
  `/usr/local/bin/agent-deck` there, so a non-login ssh command finds it.
