#!/bin/sh
# Fake sandbox provider (protocol 1) for agent-deck tests. Never used outside
# tests. Driven by the environment:
#   FAKE_PROVIDER_STATE    directory for logs and sandbox records (required)
#   FAKE_PROVIDER_MODE     ok (default) | slow | badjson | badhost | traversal |
#                          protocol2 | stderr | error | huge
#   FAKE_PROVIDER_DESTROY  ok (default) | fail
# It records argc and argv, the request on stdin and the provider env vars so
# tests can check what agent-deck sent.
set -u
action="${1:-}"
state="${FAKE_PROVIDER_STATE:?FAKE_PROVIDER_STATE is required}"
mode="${FAKE_PROVIDER_MODE:-ok}"

{ printf 'argc=%s' "$#"; for a in "$@"; do printf ' [%s]' "$a"; done; printf '\n'; } >> "$state/argv.log"
req=$(cat)
printf '%s\n' "$req" >> "$state/requests.log"
printf '%s protocol=%s name=%s\n' "$action" "${AGENTDECK_PROVIDER_PROTOCOL:-}" "${AGENTDECK_PROVIDER_NAME:-}" >> "$state/env.log"

key="$state/id_ed25519"

sandbox_json() {
	# $1 id, $2 host, $3 key path
	printf '{"id":"%s","host":"%s","port":2222,"user":"dev","auth":{"type":"key","key_path":"%s"},"labels":{"provider":"fake"},"state":"running"}' "$1" "$2" "$3"
}

case "$mode" in
slow)
	sleep 60 &
	printf '%s\n' "$!" > "$state/child.pid"
	sleep 60
	exit 0
	;;
badjson)
	printf '{"protocol":1,"sandbox":'
	exit 0
	;;
protocol2)
	printf '{"protocol":2,"ok":true}\n'
	exit 0
	;;
stderr)
	printf 'quota exceeded for project\033[31m red\n' >&2
	exit 3
	;;
error)
	printf '{"protocol":1,"error":"no capacity in region"}\n'
	exit 0
	;;
huge)
	awk 'BEGIN { printf "{\"protocol\":1,\"x\":\""; for (i = 0; i < 40000; i++) printf "%050d", 0; printf "\"}\n" }'
	exit 0
	;;
esac

case "$action" in
create)
	n=$(wc -l < "$state/argv.log" | tr -d ' ')
	id="sb-$n"
	: > "$key"
	chmod 600 "$key"
	host=127.0.0.1
	keypath="$key"
	[ "$mode" = badhost ] && host="-oProxyCommand=touch$state/pwned"
	[ "$mode" = traversal ] && keypath="$state/../$(basename "$state")/id_ed25519"
	printf '%s\n' "$id" >> "$state/created.log"
	printf '{"protocol":1,"sandbox":%s}\n' "$(sandbox_json "$id" "$host" "$keypath")"
	;;
status)
	id=$(printf '%s' "$req" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
	printf '{"protocol":1,"sandbox":%s}\n' "$(sandbox_json "$id" 127.0.0.1 "$key")"
	;;
list)
	printf '{"protocol":1,"sandboxes":['
	sep=
	if [ -f "$state/created.log" ]; then
		while read -r id; do
			grep -qx "$id" "$state/destroyed.log" 2>/dev/null && continue
			printf '%s%s' "$sep" "$(sandbox_json "$id" 127.0.0.1 "$key")"
			sep=,
		done < "$state/created.log"
	fi
	printf ']}\n'
	;;
destroy)
	if [ "${FAKE_PROVIDER_DESTROY:-ok}" = fail ]; then
		printf 'destroy refused by fake\n' >&2
		exit 4
	fi
	id=$(printf '%s' "$req" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
	printf '%s\n' "$id" >> "$state/destroyed.log"
	printf '{"protocol":1,"ok":true}\n'
	;;
*)
	printf 'unknown action %s\n' "$action" >&2
	exit 2
	;;
esac
