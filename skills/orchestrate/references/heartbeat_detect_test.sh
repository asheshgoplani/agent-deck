#!/usr/bin/env bash
# Tests for heartbeat.sh's watchdog detector: substate polling, watchdog
# nudges, debounce, legacy fallback, and the watchdog-dead backstop.
set -euo pipefail

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

cp "$(cd "$(dirname "$0")" && pwd)/heartbeat.sh" "$TMP/heartbeat.sh"
mkdir -p "$TMP/bin"

# Stub agent-deck: `session show` reports the substate in $TMP/substate;
# `session nudge` appends to calls.log and exits with the rc in
# $TMP/nudge_rc_<id> (default 0).
cat > "$TMP/bin/agent-deck" <<FIXTURE
#!/usr/bin/env bash
T="$TMP"
case "\$1 \$2" in
  "session show")
    printf '{"id":"%s","status":"running","substate":"%s"}\n' "\$3" "\$(cat "\$T/substate")"
    ;;
  "session nudge")
    printf 'nudge %s %s\n' "\$3" "\$4" >> "\$T/calls.log"
    out_file="\$T/nudge_out_\$3"
    [ -f "\$out_file" ] && cat "\$out_file"
    rc_file="\$T/nudge_rc_\$3"
    [ -f "\$rc_file" ] && exit "\$(cat "\$rc_file")"
    [ -f "\$out_file" ] || printf '{"delivery":"delivered"}\n'
    ;;
  *)
    printf 'unexpected agent-deck command: %s\n' "\$*" >&2
    exit 64
    ;;
esac
FIXTURE
chmod +x "$TMP/bin/agent-deck"

cat > "$TMP/bin/terminal-notifier" <<FIXTURE
#!/usr/bin/env bash
printf 'notify %s\n' "\$*" >> "$TMP/calls.log"
FIXTURE
chmod +x "$TMP/bin/terminal-notifier"

export PATH="$TMP/bin:$PATH"

assert_contains() {
  case "$1" in
    *"$2"*) ;;
    *) printf 'FAIL(%s): expected to contain %s:\n%s\n' "$3" "$2" "$1" >&2; exit 1 ;;
  esac
}

assert_absent() {
  case "$1" in
    *"$2"*) printf 'FAIL(%s): expected to omit %s:\n%s\n' "$3" "$2" "$1" >&2; exit 1 ;;
    *) ;;
  esac
}

# Run heartbeat.sh detached for $1 seconds with the current fixture state,
# then stop it cleanly and return calls.log's content.
run_heartbeat() {
  : > "$TMP/calls.log"
  rm -f "$TMP/.heartbeat-stop"
  HEARTBEAT_INTERVAL="${HEARTBEAT_INTERVAL:-30}" \
  WATCHDOG_DETECT_INTERVAL=1 \
    bash "$TMP/heartbeat.sh" > "$TMP/heartbeat.log" 2>&1 &
  local pid=$!
  sleep "$1"
  touch "$TMP/.heartbeat-stop"
  wait "$pid" || true
  cat "$TMP/calls.log" 2>/dev/null || true
}

printf 'cond-1\n' > "$TMP/.conductor-id"
printf 'wd-1\n' > "$TMP/.watchdog-id"

# 1. awaiting-choice -> the watchdog is nudged, not the conductor.
#    The detector used to match the SUBSTATE against `waiting`, but `waiting`
#    is a coarse STATUS value and is never a substate — so only `stalled` could
#    ever fire and a conductor sitting on a decision prompt was invisible.
printf 'awaiting-choice\n' > "$TMP/substate"
calls="$(run_heartbeat 3)"
assert_contains "$calls" "nudge wd-1" "awaiting-choice nudges watchdog"
assert_absent "$calls" "nudge cond-1" "awaiting-choice does not nudge conductor"

# 2. no .watchdog-id -> legacy behavior: conductor beats fire, no watchdog
#    nudge, and the substate never gets polled into a wake.
rm -f "$TMP/.watchdog-id"
calls="$(HEARTBEAT_INTERVAL=2 run_heartbeat 3)"
assert_contains "$calls" "nudge cond-1" "legacy conductor beat fires"
assert_absent "$calls" "nudge wd-1" "no watchdog id means no watchdog nudge"
printf 'wd-1\n' > "$TMP/.watchdog-id"

# 3. debounce: a persistent substate yields one watchdog nudge per heartbeat
#    window, but a substate CHANGE re-arms immediately.
printf 'awaiting-choice\n' > "$TMP/substate"
calls="$(run_heartbeat 4)"
n="$(printf '%s\n' "$calls" | grep -c 'nudge wd-1' || true)"
if [ "$n" -ne 1 ]; then
  printf 'FAIL(debounce): expected exactly 1 watchdog nudge, got %s:\n%s\n' "$n" "$calls" >&2
  exit 1
fi

printf 'awaiting-choice\n' > "$TMP/substate"
( sleep 2; printf 'stalled\n' > "$TMP/substate" ) &
calls="$(run_heartbeat 4)"
wait
assert_contains "$calls" "substate=awaiting-choice" "first state nudged"
assert_contains "$calls" "substate=stalled" "changed state re-arms debounce"

# 4. backstop: watchdog undeliverable for HEARTBEAT_MAX_MISSES detector ticks
#    -> exactly MAX_MISSES attempts, then one terminal-notifier banner and no
#    further attempts while the id is unchanged.
printf 'awaiting-choice\n' > "$TMP/substate"
printf '2\n' > "$TMP/nudge_rc_wd-1"
calls="$(HEARTBEAT_MAX_MISSES=2 run_heartbeat 5)"
rm -f "$TMP/nudge_rc_wd-1"
attempts="$(printf '%s\n' "$calls" | grep -c 'nudge wd-1' || true)"
notifies="$(printf '%s\n' "$calls" | grep -c '^notify ' || true)"
if [ "$attempts" -ne 2 ] || [ "$notifies" -ne 1 ]; then
  printf 'FAIL(backstop): expected 2 attempts + 1 notify, got %s/%s:\n%s\n' "$attempts" "$notifies" "$calls" >&2
  exit 1
fi

# 5. conductor beat rc=1 (reachable, not submitted = stalled composer) wakes
#    the watchdog even when the substate looks healthy.
printf 'running\n' > "$TMP/substate"
printf '1\n' > "$TMP/nudge_rc_cond-1"
calls="$(HEARTBEAT_INTERVAL=2 run_heartbeat 3)"
rm -f "$TMP/nudge_rc_cond-1"
assert_contains "$calls" "nudge wd-1 Watchdog check: conductor nudge not submitted" "rc=1 wakes watchdog"

# 6. a decision prompt the watchdog cannot answer must still reach the USER.
#    The banner fires from the script, so escalation does not depend on a
#    watchdog being alive — with no .watchdog-id at all it must still notify.
rm -f "$TMP/.watchdog-id"
printf 'awaiting-choice\n' > "$TMP/substate"
calls="$(HEARTBEAT_INTERVAL=30 WATCHDOG_CHOICE_ESCALATE=2 run_heartbeat 5)"
assert_contains "$calls" "notify" "awaiting-choice banners the user with no watchdog"
assert_contains "$calls" "waiting on YOUR answer" "banner names the user as the blocker"
printf 'wd-1\n' > "$TMP/.watchdog-id"

# 7. a beat REFUSED because the conductor is awaiting a human choice is not a
#    miss. Counting it would exhaust MAX_MISSES and kill the heartbeat of a
#    healthy run that is simply waiting on a person — the exact opposite of
#    what a supervisor should do.
printf 'running\n' > "$TMP/substate"
printf '1\n' > "$TMP/nudge_rc_cond-1"
printf '{"outcome":"refused_awaiting_choice","error_code":"SESSION_AWAITING_CHOICE"}\n' > "$TMP/nudge_out_cond-1"
calls="$(HEARTBEAT_INTERVAL=1 HEARTBEAT_MAX_MISSES=2 run_heartbeat 5)"
rm -f "$TMP/nudge_rc_cond-1" "$TMP/nudge_out_cond-1"
assert_contains "$(cat "$TMP/heartbeat.log")" "beat withheld" "awaiting-choice refusal is logged, not counted"
assert_absent "$(cat "$TMP/heartbeat.log")" "FATAL" "heartbeat survives a run blocked on its human"
assert_contains "$calls" "nudge wd-1 Watchdog check: conductor is showing a prompt" "refusal wakes the watchdog"

printf '%s\n' 'heartbeat detector fixture: ok'
