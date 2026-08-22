#!/usr/bin/env bash
# coldeye-review.sh — G5: dispatch a reviewer who has never seen this software.
#
# usage: coldeye-review.sh <slug> [seed-case] [repo]
#
# THE DEPRIVATION IS THE MECHANISM. Everyone who built the thing knows what the screen
# means, so nobody who built it can see it arriving. This script creates a directory
# holding exactly two files — the built binary and BRIEF.md — and sends a reviewer into
# it who has no access to this repository, these gates, the design conversation, or even
# the name of the feature under review.
#
# The reviewer is given a machine that already HAS sessions on it (the seed case), which
# is not a hint: an empty program tells a reviewer about its empty state and nothing
# about the screen anybody actually uses. Context is withheld; data is not.
#
# WHAT COUNTS AS A COLD REVIEWER. Anything with no history of this work: a colleague, a
# different model, a fresh agent. NOT you, and not an agent you have already briefed —
# a spoiled cold eye produces a report that looks exactly like a real one and is worth
# nothing, which is why BRIEF.md asks the reviewer to self-report contamination and why
# `sixgate coldeye outcome` fails the gate when they do.
#
# The dispatch below uses the codex CLI because it is a genuinely separate agent with
# no access to this conversation. Swap it for whatever you have; the only requirements
# are that the reviewer starts in the world directory, is told nothing beyond "read
# BRIEF.md", and can run a terminal program.
set -euo pipefail

SLUG="${1:?usage: coldeye-review.sh <slug> [seed-case] [repo]}"
SEED="${2:-claude-cold}"
REPO="${3:-$(git rev-parse --show-toplevel)}"
SIXGATE="${SIXGATE_BIN:-$REPO/build/sixgate}"

ptys() { ls /dev/ttys* 2>/dev/null | wc -l | tr -d ' '; }

PTY_BEFORE="$(ptys)"
echo "PTY census before: $PTY_BEFORE"

WORLD_LINE="$(
  HOME="$(mktemp -d)" XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= CLAUDE_CONFIG_DIR= \
  "$SIXGATE" coldeye brief "$SLUG" -repo "$REPO" -parent /tmp -seed "$SEED" |
  grep "reviewer's world:"
)"
WORLD="${WORLD_LINE##*: }"
echo "reviewer's world: $WORLD"
ls -1 "$WORLD"

# The prompt is four sentences and names nothing. Anything more is contamination.
( cd "$WORLD" && codex exec --skip-git-repo-check \
    -s workspace-write -c "sandbox_workspace_write.writable_roots=[\"${WORLD}-machine\"]" \
    "Read BRIEF.md in this directory and do exactly what it says. Write your finished report to report.md in this directory. Nothing else about this software has been or will be explained to you." )

cp "$WORLD/report.md" "$REPO/docs/gates/$SLUG/G5-coldeye/report.md"
echo "saved report to docs/gates/$SLUG/G5-coldeye/report.md"

# TEARDOWN PROOF, not a promise. The reviewer may have started a pane; the brief pins it
# to a socket under their own TMUX_TMPDIR, and this identifies it by PATH — never by
# process name or argv, which is what killed this machine's whole session fleet once.
echo "--- sockets left under the reviewer's machine ---"
find "$WORLD" "${WORLD}-machine" -type s 2>/dev/null || true
RUNID="${WORLD##*coldeye-}"
if TMUX_TMPDIR="${WORLD}-machine/home/t" tmux -L "coldeye-$RUNID" ls >/dev/null 2>&1; then
  echo "LEAK: a server is still answering on coldeye-$RUNID"
  TMUX_TMPDIR="${WORLD}-machine/home/t" tmux -L "coldeye-$RUNID" kill-server
  exit 1
fi
echo "postcondition OK: tmux -L coldeye-$RUNID ls fails (no server)"

PTY_AFTER="$(ptys)"
echo "PTY census after:  $PTY_AFTER"
[ "$PTY_BEFORE" = "$PTY_AFTER" ] || { echo "LEAK: PTY count moved $PTY_BEFORE -> $PTY_AFTER"; exit 1; }

cat <<EOF

Now answer EVERY item the reviewer listed under "What looked broken" in
  $REPO/docs/gates/$SLUG/G5-coldeye/resolutions.yaml
Each needs status: fixed|accepted and a real reason. "Fixed" without saying how is not
reviewable; "accepted" without saying why is not a decision. Then:
  $SIXGATE coldeye outcome $SLUG
EOF
