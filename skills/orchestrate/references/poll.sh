#!/usr/bin/env bash
# Delta heartbeat for the orchestrate conductor.
# Prints ONLY what changed since the last call, plus the conductor's OWN
# context size on every beat. Run it from the conductor every heartbeat:
#   bash "$RUN_DIR/poll.sh"
#
# Two lookups share this script because measurement said they had to. Across
# August 2026 runs the conductor ran `session children --json` raw 282 times
# against 87 poll.sh calls, and 213 of those 282 were not laziness — they were
# the two questions poll.sh could not answer:
#   133x  the exact context number for one child (the heartbeat buckets it to
#         ok/soft/HARD on purpose, so the bucket can't churn the diff)
#    80x  the id for a title, needed by every send/output/archive
# Each raw dump re-imports every field of every child. These subcommands answer
# the same questions in one line, so there is no longer a reason to reach past
# the heartbeat:
#   bash "$RUN_DIR/poll.sh" ctx            # exact context_tokens, all children
#   bash "$RUN_DIR/poll.sh" ctx <title>    # exact context_tokens, one child
#   bash "$RUN_DIR/poll.sh" id  <title>    # bare id, for $(...) substitution
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"
SOFT="${SOFT:-200000}"
HARD="${HARD:-250000}"
# The conductor's thresholds match a child's. Its loss is worse when it happens
# — a child that compacts loses one task, the conductor loses supervision state
# for every task at once — but its soft remedy is cheaper (flush state to disk,
# no rotation), so it is not made to hand off earlier.
#
# Neither banner below says "/compact". That instruction stood here until it
# was measured: a conductor told to run a slash command flushes its state, ends
# its turn, and waits for a human who is not watching. Compaction itself is
# fine and is still the soft remedy — it just has to be issued as
# `agent-deck session compact`, which a conductor can actually run.
SELF_SOFT="${SELF_SOFT:-200000}"
SELF_HARD="${SELF_HARD:-250000}"

RAW="$D/.poll-raw.json"
# ${POLL_CMD} exists so the script is testable with a canned JSON file.
${POLL_CMD:-agent-deck session children --json} > "$RAW"

# Lookup subcommands. They deliberately do NOT touch .poll-prev.json: asking
# "how big is impl-foo" must never consume a status transition the next
# heartbeat still has to report.
case "${1:-}" in
  ctx)
    if [ -n "${2:-}" ]; then
      jq -r --arg t "$2" '
        .children[] | select(.title == $t)
        | "\(.title) \((.context_tokens // 0) / 1000 | floor)k"' "$RAW"
    else
      jq -r '
        [ .children[] | {t: .title, c: (.context_tokens // 0)} ]
        | sort_by(-.c)[]
        | "\(.t) \((.c / 1000) | floor)k"' "$RAW"
    fi
    exit 0
    ;;
  id)
    [ -n "${2:-}" ] || { echo "usage: poll.sh id <title>" >&2; exit 2; }
    # Exit 1 on no match rather than printing an empty string: a bare id is
    # almost always consumed as $(poll.sh id X), and an empty substitution
    # turns `session send "$ID" ...` into a command that targets nothing.
    jq -er --arg t "$2" '
      [ .children[] | select(.title == $t) | .id ] | first
      // ("poll.sh: no child titled \($t)\n" | halt_error(1))' "$RAW"
    exit 0
    ;;
  "") ;;
  *) echo "poll.sh: unknown subcommand '${1}' (want: ctx | id)" >&2; exit 2 ;;
esac

jq --argjson soft "$SOFT" --argjson hard "$HARD" '
    [ .children[]
      | { id, title, status,
          done: (if .done_stale then "stale" else (.done_status // "-") end),
          ctx:  (if   (.context_tokens // 0) >= $hard then "HARD"
                 elif (.context_tokens // 0) >= $soft then "soft"
                 else "ok" end) } ]
    | sort_by(.id)' "$RAW" > "$D/.poll-now.json"

# -1 means "the field was absent" — an agent-deck too old to report the
# parent's own size. That is reported as n/a rather than silently omitted: a
# missing self-context signal is the exact condition that lets a conductor
# drift to a million tokens unnoticed, so it must be visible, not assumed low.
SELF="$(jq -r '.parent_context_tokens // -1' "$RAW")"

self_note=" · self=n/a (upgrade agent-deck: no parent_context_tokens)"
banner=""
# Exit status carries the hard threshold, because the banner alone did not.
# Measured over 12 real conductors: median peak 348k, p90 645k, worst 839k —
# 6 of 12 ran past this same hard line while the banner repeated every beat.
# A warning printed to a session that is already overloaded is exactly the
# thing that gets skimmed, so at hard the heartbeat FAILS instead: the
# conductor's own routine command stops returning success until it hands off.
poll_rc=0
if [ "$SELF" -ge 0 ]; then
  self_note="$(printf ' · self=%dk' $((SELF / 1000)))"
  if [ "$SELF" -ge "$SELF_HARD" ]; then
    banner="$(printf '!! SELF-CONTEXT %dk >= hard %dk — ROTATE NOW, unattended, do not ask the user:\n     bash "%s/rotate-conductor.sh"\n   It refuses to run until conductor-handoff.md is non-empty, then launches your successor, re-parents every live child and archives you. (poll.sh keeps exiting 3 until you go)' $((SELF / 1000)) $((SELF_HARD / 1000)) "$D")"
    poll_rc=3
  elif [ "$SELF" -ge "$SELF_SOFT" ]; then
    banner="$(printf '!! SELF-CONTEXT %dk >= soft %dk — this turn, without asking anyone:\n   1. write everything unwritten into %s/manifest.md, and bring %s/conductor-handoff.md up to date (live tasks + their stage, open questions, anything in flight)\n   2. then, last thing in the turn:\n      agent-deck session compact --instructions "Keep: run dir, every live task and its stage, open questions, PR urls, HEAD shas." --resume '"'"'bash "%s/poll.sh"'"'"'\n   It returns queued/unverified — that is correct for a self-compact, not a failure. Do NOT send yourself anything else afterwards: a message arriving mid-compaction cancels it.' $((SELF / 1000)) $((SELF_SOFT / 1000)) "$D" "$D" "$D")"
  fi
fi
# Re-printed on every beat while over threshold, never once on the crossing: a
# single warning scrolls away behind the next four heartbeats.
[ -n "$banner" ] && printf '%s\n' "$banner"

[ -f "$D/.poll-prev.json" ] || echo '[]' > "$D/.poll-prev.json"

jq -rn --slurpfile a "$D/.poll-prev.json" --slurpfile b "$D/.poll-now.json" \
       --arg self "$self_note" '
  def key: {id, title, status, done};        # ctx is NOT a diff key — it is
  ($a[0] | INDEX(.id)) as $old               # reported in the tail instead, so
| ($b[0] | INDEX(.id)) as $cur               # a bucket crossing never fakes a
| $b[0] as $new                              # status change.
| ([ $new[]  | select((. | key) != (($old[.id] // null) | key))
             | "CHANGED \(.title): \(.status)/\(.done)" ]
 + [ $a[0][] | select($cur[.id] == null) | "GONE    \(.title)" ]) as $chg
| (($new | group_by(.status) | map("\(length) \(.[0].status)") | join(" "))
   | if . == "" then "none live" else . end) as $roll
| ([ $new[] | select(.ctx != "ok") | "\(.title)=\(.ctx)" ]) as $ctx
| (if ($chg | length) == 0
   then "\($new|length) children · \($roll) · no change"
   else ($chg | join("\n")) + "\n\($new|length) children · \($roll)"
   end)
+ (if ($ctx | length) > 0 then " · ctx " + ($ctx | join(" ")) else "" end)
+ $self'

mv "$D/.poll-now.json" "$D/.poll-prev.json"

# Last line, after the diff state is committed: a failing heartbeat must still
# have advanced the baseline, or the next beat re-reports every row as CHANGED
# and the failure costs more context than it saves.
exit "$poll_rc"
