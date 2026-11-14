#!/bin/sh
# Runs as PID 1 in the sandbox container.
# Fresh host keys per container, then install the public key the provider
# copied to /etc/agent-deck/authorized_keys, then run sshd in the foreground.
set -eu

ssh-keygen -A >/dev/null

if [ -f /etc/agent-deck/authorized_keys ]; then
	install -d -m 0700 -o dev -g dev /home/dev/.ssh
	install -m 0600 -o dev -g dev /etc/agent-deck/authorized_keys /home/dev/.ssh/authorized_keys
fi

exec /usr/sbin/sshd -D -e
