# Sandbox image for the example docker provider (agent-deck provider protocol 1).
# Small Debian with sshd (key auth only), tmux, git and curl.
# User "dev" has no password and passwordless sudo.
FROM debian:bookworm-slim

RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      openssh-server tmux git curl ca-certificates bash sudo procps \
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
# does not have on PATH; the /usr/local/bin link above makes it found.

COPY sshd_config /etc/ssh/sshd_config.d/agent-deck.conf
COPY entrypoint.sh /usr/local/bin/agent-deck-sandbox-entrypoint
RUN chmod 0644 /etc/ssh/sshd_config.d/agent-deck.conf \
 && chmod 0755 /usr/local/bin/agent-deck-sandbox-entrypoint

EXPOSE 22
ENTRYPOINT ["/usr/local/bin/agent-deck-sandbox-entrypoint"]
