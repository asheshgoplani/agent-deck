#!/bin/bash
# Runs once on a new azure-vm sandbox, as root, from cloud-init (runcmd).
# It installs what a first agent session needs and holds no secret:
#   - git, tmux and curl (already in Ubuntu server images; installed if not),
#   - Claude Code for the login user (Anthropic's native installer, saved to
#     a file and run from there), linked into /usr/local/bin so a non-login
#     ssh command finds it,
#   - a /usr/local/bin/agent-deck link to ~/.local/bin/agent-deck, where
#     agent-deck installs itself, for the same reason,
#   - a git identity and Claude settings (auto-updater off).
# Last, it writes /var/lib/agent-deck-sandbox-ready; the provider waits for it.
#
# Usage: sandbox-init.sh <login user> <claude version: stable|latest|x.y.z>
set -euo pipefail
umask 022

u="${1:?login user}"
ver="${2:-stable}"
[[ "$u" =~ ^[a-z_][a-z0-9_-]{0,31}$ ]] || {
	echo "sandbox init: invalid user" >&2
	exit 1
}
[[ "$ver" =~ ^(stable|latest|[0-9]+\.[0-9]+\.[0-9]+)$ ]] || {
	echo "sandbox init: invalid claude version" >&2
	exit 1
}
home="$(getent passwd "$u" | cut -d: -f6)"
[ -n "$home" ] && [ -d "$home" ] || {
	echo "sandbox init: user $u has no home" >&2
	exit 1
}

if ! command -v git >/dev/null || ! command -v tmux >/dev/null || ! command -v curl >/dev/null; then
	export DEBIAN_FRONTEND=noninteractive
	apt-get update -q
	apt-get install -y -q --no-install-recommends git tmux curl ca-certificates
fi

install -d -o "$u" -g "$u" -m 0755 "$home/.local" "$home/.local/bin" "$home/.claude"
ln -sfn "$home/.local/bin/agent-deck" /usr/local/bin/agent-deck

# The version is checked above, so it is safe inside the quoted command.
su - "$u" -c "curl -fsSL -o ~/claude-install.sh https://claude.ai/install.sh && bash ~/claude-install.sh $ver && rm -f ~/claude-install.sh"
ln -sfn "$home/.local/bin/claude" /usr/local/bin/claude
claude --version >/dev/null

su - "$u" -c 'git config --global user.name "Sandbox Dev" &&
	git config --global user.email "dev@agent-deck-sandbox" &&
	git config --global init.defaultBranch main &&
	printf "{\"env\":{\"DISABLE_AUTOUPDATER\":\"1\"},\"skipDangerousModePermissionPrompt\":true}\n" >~/.claude/settings.json'

touch /var/lib/agent-deck-sandbox-ready
echo "sandbox init: ready ($(claude --version 2>/dev/null | head -n 1))"
