#!/usr/bin/env bash
# Delta heartbeat for the orchestrate conductor.
# Prints ONLY what changed since the last call, plus the conductor's OWN
# context size on every beat. Run it from the conductor every heartbeat:
#   bash "$RUN_DIR/poll.sh"
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"
SOFT="${SOFT:-200000}"
HARD="${HARD:-250000}"
# The conductor's thresholds match a child's. Its loss is worse when it happens
# — a child that compacts loses one task, the conductor loses supervision state
# for every task at once — but it also has a cheaper remedy (/compact at an
# inter-task boundary, no rotation), so it is not made to hand off earlier.
SELF_SOFT="${SELF_SOFT:-200000}"
SELF_HARD="${SELF_HARD:-250000}"

RAW="$D/.poll-raw.json"
# ${POLL_CMD} exists so the script is testable with a canned JSON file.
${POLL_CMD:-agent-deck session children --json} > "$RAW"

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
if [ "$SELF" -ge 0 ]; then
  self_note="$(printf ' · self=%dk' $((SELF / 1000)))"
  if [ "$SELF" -ge "$SELF_HARD" ]; then
    banner="$(printf '!! SELF-CONTEXT %dk >= hard %dk — HAND OFF NOW: write $RUN_DIR/conductor-handoff.md, launch a fresh conductor on manifest.md, re-parent every live child, archive yourself.' $((SELF / 1000)) $((SELF_HARD / 1000)))"
  elif [ "$SELF" -ge "$SELF_SOFT" ]; then
    banner="$(printf '!! SELF-CONTEXT %dk >= soft %dk — flush everything unwritten into manifest.md and /compact at the next inter-task boundary.' $((SELF / 1000)) $((SELF_SOFT / 1000)))"
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
