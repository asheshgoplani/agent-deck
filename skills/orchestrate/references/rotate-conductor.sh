#!/usr/bin/env bash
# Automatic conductor rotation at the hard self-context threshold.
#
# Run it from the conductor, unattended, when poll.sh starts exiting 3:
#   bash "$RUN_DIR/rotate-conductor.sh"
#
# It exists because the hard threshold's remedy used to be a five-step prose
# recipe ending in "archive yourself" — and measured over 12 real conductors, 6
# sailed straight past that line. A single command has no steps to skip and no
# step that needs a human.
#
# Rotation is the hard-threshold remedy, not compaction. At the soft threshold
# the conductor compacts in place with `agent-deck session compact`; by the time
# it gets here compaction is no longer enough and the run needs a successor with
# a fresh window.
set -euo pipefail
D="$(cd "$(dirname "$0")" && pwd)"          # = $RUN_DIR
HANDOFF="$D/conductor-handoff.md"
MANIFEST="$D/manifest.md"
GEN_FILE="$D/.conductor-generation"
MAX_GEN="${MAX_CONDUCTOR_GEN:-5}"

# 1. Refuse to rotate into an empty handoff. A rotation that produces an empty
#    handoff has been observed in the field, and the successor then inherits the
#    manifest with nothing about what was in flight. Failing here is cheap — the
#    conductor is still alive and can write the file and re-run.
for f in "$MANIFEST" "$HANDOFF"; do
  if [ ! -s "$f" ]; then
    echo "rotate-conductor: $f is missing or empty. Write it, then re-run this script." >&2
    exit 2
  fi
done

# 2. Resolve self. `session show` with no id auto-detects the calling session.
SELF_JSON="$(agent-deck session show --json)"
SELF_ID="$(jq -r '(.data // .) | .id // empty' <<<"$SELF_JSON")"
SELF_TITLE="$(jq -r '(.data // .) | .title // empty' <<<"$SELF_JSON")"
REPO="$(jq -r '(.data // .) | .path // empty' <<<"$SELF_JSON")"
GROUP="$(jq -r '(.data // .) | .group_path // empty' <<<"$SELF_JSON")"
if [ -z "$SELF_ID" ] || [ -z "$REPO" ]; then
  echo "rotate-conductor: could not resolve this session (id=$SELF_ID path=$REPO)." >&2
  exit 2
fi

# 3. Bound the chain. A conductor that rotates straight back into its own
#    ceiling would otherwise spawn successors forever. The cap is checked before
#    the counter is written, so a failed rotation does not burn a generation.
PREV_GEN=1
[ -f "$GEN_FILE" ] && PREV_GEN="$(cat "$GEN_FILE")"
GEN=$((PREV_GEN + 1))
if [ "$GEN" -gt "$MAX_GEN" ]; then
  echo "rotate-conductor: generation $GEN exceeds MAX_CONDUCTOR_GEN=$MAX_GEN." >&2
  echo "  A run rotating this many times is mis-scoped. Stop and tell the user." >&2
  exit 3
fi
NEXT_TITLE="$(basename "$D")-c$GEN"

# 4. Render the successor's prompt to a file rather than a shell argument, so
#    no part of it needs quoting and the run keeps the artifact.
PROMPT_FILE="$D/conductor-c$GEN-prompt.md"
cat > "$PROMPT_FILE" <<EOF
You are the continuation conductor for this orchestrate run, generation $GEN.
Your predecessor reached its context ceiling and rotated out. This is a
handoff, not a restart: the run is mid-flight and its children are live.

Read these two files before you do anything else:

  $MANIFEST
      The run's state — every task and the stage it reached.
  $HANDOFF
      What was in flight at the instant of rotation: live tasks and their
      stage, open questions, anything awaiting a decision from you.

Then load the orchestrate skill and resume supervision from the heartbeat:

  RUN_DIR="$D"
  bash "\$RUN_DIR/poll.sh"

Every live child has already been re-parented to you and will appear in that
output. Do not re-launch them. Do not redo work the manifest records as done.
Your first action is the heartbeat, not a status sweep.
EOF

# 5. Launch the successor. --no-parent: a conductor is the root of its own tree.
LAUNCH=(launch "$REPO" -t "$NEXT_TITLE" -c claude --no-parent
        --message-file "$PROMPT_FILE")
[ -n "$GROUP" ] && LAUNCH+=(-g "$GROUP")
NEW_JSON="$(agent-deck "${LAUNCH[@]}" --json)"
NEW_ID="$(jq -r '(.data // .) | (.id // .session_id // empty)' <<<"$NEW_JSON")"
if [ -z "$NEW_ID" ]; then
  echo "rotate-conductor: successor did not come up; NOT archiving self." >&2
  echo "$NEW_JSON" >&2
  exit 4
fi
echo "$GEN" > "$GEN_FILE"
# Hand the wall-clock watchdog its new target in the same breath. heartbeat.sh
# re-reads this file every beat, so the running watchdog follows the rotation
# with no restart; skip this and it keeps nudging a session about to be
# archived and logs healthy-looking "not found" beats forever.
echo "$NEW_ID" > "$D/.conductor-id"

# 6. Re-parent every live child, so waiting/done notifications route to the
#    successor instead of a session that is about to be archived.
MOVED=0
while read -r cid; do
  [ -n "$cid" ] || continue
  if agent-deck session set-parent "$cid" "$NEW_ID" >/dev/null 2>&1; then
    MOVED=$((MOVED + 1))
  else
    echo "rotate-conductor: WARNING could not re-parent child $cid" >&2
  fi
done < <(agent-deck session children "$SELF_ID" --json \
         | jq -r '.children[]? | select(.status != "archived") | .id')

# 7. Archive self last, and detached: archiving tears down the pane this script
#    is running in, so the message above would otherwise never reach the
#    transcript. The sleep buys the flush.
echo "rotate-conductor: successor $NEXT_TITLE ($NEW_ID) is live; $MOVED child(ren) re-parented."
echo "rotate-conductor: archiving self ($SELF_TITLE) now. This session ends here."
( sleep 2; agent-deck session archive "$SELF_ID" >/dev/null 2>&1 ) &
exit 0
