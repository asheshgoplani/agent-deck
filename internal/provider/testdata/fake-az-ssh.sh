#!/bin/sh
# Fake ssh for the example azure-vm provider tests: records each call's
# argv to $FAKE_AZ_STATE/ssh.log and appends its stdin to ssh.stdin. Always
# succeeds; nothing is contacted.
#
# With FAKE_SSH_SETUP=<path to sandbox-setup.sh> and FAKE_SSH_HOME=<dir>, a
# call that runs /usr/local/bin/agent-deck-sandbox-setup runs that script
# locally instead, with HOME=$FAKE_SSH_HOME and the call's stdin, and passes
# on its stderr and exit code (as real ssh does).
s="${FAKE_AZ_STATE:?FAKE_AZ_STATE not set}"
printf '%s\n' "$*" >>"$s/ssh.log"
for last in "$@"; do :; done
if [ -n "${FAKE_SSH_SETUP:-}" ] && [ "${last:-}" = /usr/local/bin/agent-deck-sandbox-setup ]; then
	exec env HOME="${FAKE_SSH_HOME:?FAKE_SSH_HOME not set}" "$FAKE_SSH_SETUP"
fi
cat >>"$s/ssh.stdin"
exit 0
