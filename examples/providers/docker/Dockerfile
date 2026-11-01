# Sandbox image for the example docker provider (agent-deck provider protocol 1).
# Small Debian with sshd (key auth only), tmux, git, curl and Claude Code.
# User "dev" has no password and passwordless sudo.
# No credential is ever built into this image: a Claude login token, when
# configured, is copied into the running container at create time.
FROM debian:bookworm-slim

# Claude Code release to install: stable, latest or a version like 2.1.30.
ARG CLAUDE_CODE_VERSION=stable

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server tmux git curl ca-certificates bash sudo procps less \
 && rm -rf /var/lib/apt/lists/* \
 && rm -f /etc/ssh/ssh_host_*

RUN useradd --create-home --shell /bin/bash dev \
 && usermod -p '*' dev \
 && echo 'dev ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/dev \
 && chmod 0440 /etc/sudoers.d/dev \
 && mkdir -p /run/sshd /etc/agent-deck \
 && chmod 0755 /etc/agent-deck \
 && mkdir -p /home/dev/.local/bin \
 && chown -R dev:dev /home/dev/.local \
 && ln -s /home/dev/.local/bin/agent-deck /usr/local/bin/agent-deck

# agent-deck installs itself into ~/.local/bin, which a non-login ssh command
# does not have on PATH; the /usr/local/bin link above makes it found. The
# same goes for claude below.

# Claude Code, with the native installer (checksum-verified), as user dev.
USER dev
RUN curl -fsSL https://claude.ai/install.sh | bash -s -- "$CLAUDE_CODE_VERSION" \
 && /home/dev/.local/bin/claude --version \
 && git config --global user.name "Sandbox Dev" \
 && git config --global user.email "dev@agent-deck-sandbox" \
 && git config --global init.defaultBranch main \
 && mkdir -p /home/dev/.claude \
 && printf '{"env":{"DISABLE_AUTOUPDATER":"1"}}\n' > /home/dev/.claude/settings.json
USER root
RUN ln -s /home/dev/.local/bin/claude /usr/local/bin/claude

# The demo project every sandbox gets when no project is configured.
COPY --chown=dev:dev demo-project /opt/agent-deck-sandbox/demo-project
RUN cd /opt/agent-deck-sandbox/demo-project \
 && su dev -c 'git init --quiet && git add . && git commit --quiet -m "demo project"'

COPY sshd_config /etc/ssh/sshd_config.d/agent-deck.conf
COPY entrypoint.sh /usr/local/bin/agent-deck-sandbox-entrypoint
COPY sandbox-setup.sh /usr/local/bin/agent-deck-sandbox-setup
RUN chmod 0644 /etc/ssh/sshd_config.d/agent-deck.conf \
 && chmod 0755 /usr/local/bin/agent-deck-sandbox-entrypoint /usr/local/bin/agent-deck-sandbox-setup

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/agent-deck-sandbox-entrypoint"]
