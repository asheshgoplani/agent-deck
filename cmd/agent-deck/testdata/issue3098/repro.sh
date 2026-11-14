#!/bin/sh
# Reproduction for agent-deck issue #3098 follow-up: a [tools.pi] prompt_patterns_extra
# entry is ignored, so `launch -m` never delivers its initial task to a pi whose
# footer only that extra pattern matches.
#
# $1: agent-deck binary under test. Fixtures: /fixtures/bin/pi (fake pi that draws a
# custom footer and prints ACCEPTED_READY_PROBE for each line it reads) and
# /fixtures/pi.toml (the [tools.pi] extra pattern that matches that footer).
#
# Exit 0: launch succeeded and the fake pi acknowledged the task (healthy).
# Exit 1: launch timed out with the custom footer on screen and no task delivered (the defect).
# Exit 2: anything else (harness or setup error).
set -u
bin="$1"
root=/work/probe
sock=pi-repro

mkdir -p "$root/project" "$XDG_CONFIG_HOME/agent-deck" || exit 2
{ printf '[tmux]\nsocket_name = "%s"\n\n' "$sock"; cat /fixtures/pi.toml; } \
  > "$XDG_CONFIG_HOME/agent-deck/config.toml" || exit 2
export PATH="/fixtures/bin:/usr/local/bin:/usr/bin:/bin" AGENTDECK_SKIP_UPDATE_CHECK=1
command -v pi >/dev/null || { echo 'HARNESS: fake pi not on PATH'; exit 2; }
command -v tmux >/dev/null || { echo 'HARNESS: tmux missing'; exit 2; }
command -v jq >/dev/null || { echo 'HARNESS: jq missing'; exit 2; }

echo '--- config.toml'
cat "$XDG_CONFIG_HOME/agent-deck/config.toml"

rc=0
timeout 25 "$bin" launch -c pi -t footer-probe -m READY_PROBE --json "$root/project" \
  > "$root/launch.json" 2> "$root/launch.stderr" || rc=$?
tmux -L "$sock" capture-pane -p > "$root/pane.txt" 2>/dev/null || : > "$root/pane.txt"
tmux -L "$sock" kill-server 2>/dev/null

echo "--- launch exit: $rc"
echo '--- launch.json'; cat "$root/launch.json"
echo '--- launch.stderr'; cat "$root/launch.stderr"
echo '--- pane'; cat "$root/pane.txt"

delivered=$(grep -c ACCEPTED_READY_PROBE "$root/pane.txt")
footer=$(grep -c 'ctx:0%' "$root/pane.txt")

if [ "$rc" -eq 0 ] && jq -e '.success == true' "$root/launch.json" >/dev/null 2>&1 && [ "$delivered" -eq 1 ]; then
  echo 'HEALTHY: custom footer recognized, initial task delivered once'
  exit 0
fi
if [ "$rc" -eq 124 ] && [ "$footer" -ge 1 ] && [ "$delivered" -eq 0 ]; then
  echo 'DEFECT: custom footer visible, launch timed out, initial task never delivered'
  exit 1
fi
echo "HARNESS: unexpected outcome (rc=$rc footer=$footer delivered=$delivered)"
exit 2
