#!/usr/bin/env bash
set -euo pipefail

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

cp "$(cd "$(dirname "$0")" && pwd)/poll.sh" "$TMP/poll.sh"
mkdir -p "$TMP/bin"

cat > "$TMP/bin/agent-deck" <<'FIXTURE'
#!/usr/bin/env bash
set -euo pipefail

case "$*" in
  "session children --json")
    archived=true
    if [ "${FIXTURE_PHASE:-archived}" = "active" ]; then
      archived=false
    fi
    cat <<JSON
{"parent_context_tokens":1000,"children":[{"id":"archived-id","title":"archived-child","status":"running","context_tokens":900000,"archived":${archived}},{"id":"false-id","title":"false-child","status":"waiting","context_tokens":8000,"archived":false},{"id":"legacy-id","title":"legacy-child","status":"archived","context_tokens":7000}]}
JSON
    ;;
  "session show --json")
    printf '%s\n' '{"id":"fixture-parent","status":"running"}'
    ;;
  *)
    printf 'unexpected agent-deck command: %s\n' "$*" >&2
    exit 64
    ;;
esac
FIXTURE
chmod +x "$TMP/bin/agent-deck"

export PATH="$TMP/bin:$PATH"

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *) printf 'expected output to contain %s:\n%s\n' "$2" "$1" >&2; exit 1 ;;
  esac
}

assert_absent() {
  case "$1" in
    *"$2"*) printf 'expected output to omit %s:\n%s\n' "$2" "$1" >&2; exit 1 ;;
    *) ;;
  esac
}

ctx="$(bash "$TMP/poll.sh" ctx)"
assert_absent "$ctx" "archived-child"
assert_contains "$ctx" "false-child 8k"
assert_contains "$ctx" "legacy-child 7k"

[ "$(bash "$TMP/poll.sh" id false-child)" = "false-id" ]
[ "$(bash "$TMP/poll.sh" id legacy-child)" = "legacy-id" ]
if archived_id="$(bash "$TMP/poll.sh" id archived-child 2>/dev/null)"; then
  printf 'expected archived child id lookup to fail, got: %s\n' "$archived_id" >&2
  exit 1
fi

main="$(bash "$TMP/poll.sh")"
assert_absent "$main" "archived-child"
assert_contains "$main" "false-child"
assert_contains "$main" "legacy-child: archived/"
assert_contains "$main" "2 children"

active="$(FIXTURE_PHASE=active bash "$TMP/poll.sh")"
assert_contains "$active" "archived-child=HARD"

gone="$(FIXTURE_PHASE=archived bash "$TMP/poll.sh")"
[ "$(printf '%s\n' "$gone" | awk '$0 == "GONE    archived-child" { n++ } END { print n+0 }')" -eq 1 ]
assert_absent "$gone" "archived-child=HARD"

stable="$(FIXTURE_PHASE=archived bash "$TMP/poll.sh")"
assert_absent "$stable" "GONE    archived-child"
assert_absent "$stable" "archived-child=HARD"

printf '%s\n' 'poll archived filter fixture: ok'
