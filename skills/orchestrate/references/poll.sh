#!/usr/bin/env bash
# Delta heartbeat for the orchestrate conductor.
# Prints ONLY what changed since the last call. Run it from the conductor
# every heartbeat: bash "$RUN_DIR/poll.sh"
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"
SOFT="${SOFT:-200000}"
HARD="${HARD:-250000}"

# ${POLL_CMD} exists so the script is testable with a canned JSON file.
${POLL_CMD:-agent-deck session children --json} \
| jq --argjson soft "$SOFT" --argjson hard "$HARD" '
    [ .children[]
      | { id, title, status,
          done: (if .done_stale then "stale" else (.done_status // "-") end),
          ctx:  (if   (.context_tokens // 0) >= $hard then "HARD"
                 elif (.context_tokens // 0) >= $soft then "soft"
                 else "ok" end) } ]
    | sort_by(.id)' > "$D/.poll-now.json"

[ -f "$D/.poll-prev.json" ] || echo '[]' > "$D/.poll-prev.json"

jq -rn --slurpfile a "$D/.poll-prev.json" --slurpfile b "$D/.poll-now.json" '
  def key: {id, title, status, done};        # ctx is NOT a diff key — it is
  ($a[0] | INDEX(.id)) as $old               # reported in the tail instead, so
| ($b[0] | INDEX(.id)) as $cur               # a bucket crossing never fakes a
| $b[0] as $new                              # status change.
| ([ $new[]  | select((. | key) != (($old[.id] // null) | key))
             | "CHANGED \(.title): \(.status)/\(.done)" ]
 + [ $a[0][] | select($cur[.id] == null) | "GONE    \(.title)" ]) as $chg
| ($new | group_by(.status) | map("\(length) \(.[0].status)") | join(" ")) as $roll
| ([ $new[] | select(.ctx != "ok") | "\(.title)=\(.ctx)" ]) as $ctx
| (if ($chg | length) == 0
   then "\($new|length) children · \($roll) · no change"
   else ($chg | join("\n")) + "\n\($new|length) children · \($roll)"
   end)
+ (if ($ctx | length) > 0 then " · ctx " + ($ctx | join(" ")) else "" end)'

mv "$D/.poll-now.json" "$D/.poll-prev.json"
