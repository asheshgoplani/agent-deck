#!/bin/sh
# Fake ssh for the example azure-vm provider tests: records each call's
# argv to $FAKE_AZ_STATE/ssh.log and appends its stdin to ssh.stdin. Always
# succeeds; nothing is contacted.
s="${FAKE_AZ_STATE:?FAKE_AZ_STATE not set}"
printf '%s\n' "$*" >>"$s/ssh.log"
cat >>"$s/ssh.stdin"
exit 0
