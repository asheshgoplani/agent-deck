#!/bin/bash
# Run only inside an expendable Docker container. Never target host tmux.
set -euo pipefail
[[ -f /.dockerenv ]] || { echo 'Docker container required' >&2; exit 2; }
source_dir=${1:-/src}
receipts=${2:-/evidence}
reviewed=6574d29ef262f12648fc0233f9bc962779abfe87
mkdir -p "$receipts"
work=$(mktemp -d /tmp/health-acceptance.XXXXXX)
head=$(git -C "$source_dir" -c safe.directory="$source_dir" rev-parse HEAD)
printf 'head=%s\nreviewed_baseline=%s\n' "$head" "$reviewed" | tee "$receipts/acceptance-source.txt"
for variant in red green; do
 mkdir -p "$work/$variant"
 git -C "$source_dir" -c safe.directory="$source_dir" archive "$head" | tar -x -C "$work/$variant"
done
# Keep the final missing-client regression test; restore only the reviewed
# acceptance implementation, so a compile failure cannot masquerade as red.
git -C "$source_dir" -c safe.directory="$source_dir" show "$reviewed:cmd/agent-deck/health_integration_test.go" > "$work/red/cmd/agent-deck/health_integration_test.go"
sha256sum "$work/"{red,green}/cmd/agent-deck/health_acceptance_test.go | tee "$receipts/ssh-harness.sha256"
export TMUX_TMPDIR=/tmp
unset TMUX TMUX_PANE
sentinel_socket="/tmp/tmux-$(id -u)/default"
install -d -m 700 "$(dirname "$sentinel_socket")"
tmux -S "$sentinel_socket" new-session -d -s health-review-sentinel 'sleep 600'
trap 'tmux -S "$sentinel_socket" kill-server || true' EXIT
tmux -S "$sentinel_socket" bind-key -n MouseDown1StatusRight display-message runtime-health-sentinel
snapshot() {
 tmux -S "$sentinel_socket" list-keys > "$receipts/$1.bindings"
 tmux -S "$sentinel_socket" list-sessions -F '#{session_id}:#{session_name}:#{session_windows}' > "$receipts/$1.sessions"
}
snapshot before
set +e
(cd "$work/red" && go test ./cmd/agent-deck -run '^TestHealthRemoteExecRequiresSSH$' -count=1 -v -timeout 120s) > "$receipts/ssh-red.log" 2>&1
ssh_red=$?
(cd "$work/red" && go test ./cmd/agent-deck -run '^TestRuntimeHealthHeadlessWebStartup$' -count=1 -v -timeout 120s) > "$receipts/web-red.log" 2>&1
web_red=$?
set -e
snapshot after-red
set +e
cmp "$receipts/before.bindings" "$receipts/after-red.bindings" >> "$receipts/web-red.log" 2>&1
sentinel_red=$?
set -e
printf 'ssh_red_exit=%s\nweb_baseline_test_exit=%s\nsentinel_red_exit=%s\n' "$ssh_red" "$web_red" "$sentinel_red" | tee "$receipts/red-exits.txt"
cat "$receipts/ssh-red.log" "$receipts/web-red.log"
[[ "$ssh_red" == 1 && "$web_red" == 0 && "$sentinel_red" == 1 ]]
grep -q 'missing SSH must fail acceptance explicitly' "$receipts/ssh-red.log"
grep -q -- '--- SKIP:' "$receipts/ssh-red.log"
# Reset just our sentinel binding before checking the fixed tests.
tmux -S "$sentinel_socket" bind-key -n MouseDown1StatusRight display-message runtime-health-sentinel
snapshot before-green
(cd "$work/green" && go test ./cmd/agent-deck -run '^Test(Health|RuntimeHealth|DoctorUnreadableHealth)' -count=1 -v -timeout 180s) 2>&1 | tee "$receipts/acceptance-green.log"
snapshot after-green
cmp "$receipts/before-green.bindings" "$receipts/after-green.bindings"
cmp "$receipts/before-green.sessions" "$receipts/after-green.sessions"
printf 'acceptance_green_exit=0\nsentinel_bindings_exit=0\nsentinel_sessions_exit=0\n' | tee "$receipts/green-exits.txt"
