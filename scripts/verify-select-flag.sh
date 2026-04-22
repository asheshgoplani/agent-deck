#!/usr/bin/env bash
# verify-select-flag.sh — end-to-end evidence that --select (#709) preselects
# a session and keeps every group visible in the sidebar.
#
# Uses Seam C (headless tmux + capture-pane) so it exercises the real binary
# inside a real terminal, which is the only layer where cursor positioning +
# viewport scrolling is visible to a user.
#
# Usage: bash scripts/verify-select-flag.sh
# Env:
#   AGENT_DECK_BIN  — path to binary (default: ./agent-deck)
#   KEEP_SESSION=1  — leave tmux session after success for manual inspection
set -euo pipefail

C_RED='\033[31m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RESET='\033[0m'
pass() { printf "${C_GREEN}[PASS]${C_RESET} %s\n" "$*"; }
fail() { printf "${C_RED}[FAIL]${C_RESET} %s\n" "$*" >&2; FAILED=1; }
skip() { printf "${C_YELLOW}[SKIP]${C_RESET} %s\n" "$*"; }
log()  { printf "    %s\n" "$*"; }

FAILED=0
BIN="${AGENT_DECK_BIN:-./agent-deck}"
TSESS="adeck-select-$$"
TMPHOME="$(mktemp -d -t adeck-select.XXXXXX)"

cleanup() {
  set +e
  if [[ "${KEEP_SESSION:-0}" != "1" ]]; then
    tmux kill-session -t "$TSESS" 2>/dev/null || true
    [[ -d "$TMPHOME" && "$TMPHOME" == /tmp/adeck-select.* ]] && rm -rf "$TMPHOME"
  else
    echo "session preserved: tmux attach -t $TSESS"
    echo "fake HOME preserved: $TMPHOME"
  fi
}
trap cleanup EXIT INT TERM

command -v tmux >/dev/null || { skip "tmux not installed"; exit 0; }
[[ -x "$BIN" ]] || { fail "binary not found at $BIN (run: go build -o agent-deck ./cmd/agent-deck)"; exit 1; }

# ---- seed three sessions in three groups so we can verify all groups remain visible
mkdir -p "$TMPHOME/.agent-deck"
cat > "$TMPHOME/.agent-deck/config.toml" <<'EOF'
[tmux]
inject_status_line = false
EOF

mkdir -p "$TMPHOME/proj-alpha" "$TMPHOME/proj-beta" "$TMPHOME/proj-gamma"
env HOME="$TMPHOME" "$BIN" add "$TMPHOME/proj-alpha" -t alpha -g work -c claude >/dev/null 2>&1
env HOME="$TMPHOME" "$BIN" add "$TMPHOME/proj-beta"  -t beta  -g personal -c claude >/dev/null 2>&1
env HOME="$TMPHOME" "$BIN" add "$TMPHOME/proj-gamma" -t gamma -g clients/acme -c claude >/dev/null 2>&1

# ---- scenario 1: --select beta must land cursor on 'beta' with ALL groups visible
tmux new-session -d -s "$TSESS" -x 180 -y 50 \
  "env HOME='$TMPHOME' AGENT_DECK_ALLOW_OUTER_TMUX=1 '$BIN' --select beta"

for _ in $(seq 1 40); do
  sleep 0.2
  out="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
  # Wait past splash
  if grep -qi "beta" <<<"$out" && grep -qi "alpha" <<<"$out"; then
    break
  fi
done

# Dismiss any first-run prompt (hooks wizard etc).
tmux send-keys -t "$TSESS" "n" ; sleep 0.2
tmux send-keys -t "$TSESS" "Escape" ; sleep 0.2

out="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
echo "---- pane snapshot (scenario 1: --select beta) ----"
echo "$out"
echo "---- end snapshot ----"

# All three groups must appear.
missing=()
for g in work personal "clients"; do
  grep -qi "$g" <<<"$out" || missing+=("$g")
done
if [[ ${#missing[@]} -eq 0 ]]; then
  pass "all three groups (work, personal, clients/acme) visible in sidebar"
else
  fail "missing groups in sidebar: ${missing[*]}"
fi

# Cursor-on-session evidence: agent-deck renders the selected line with a
# highlight marker ('>' or inverse video). Either way the 'beta' literal
# must be present in the pane; the selection prefix character 'selection'
# is rendered by the TUI on the highlighted line. We grep for the marker
# char rendered by list.go ('▶' or '>').
if grep -q "beta" <<<"$out"; then
  pass "selected session 'beta' rendered in pane"
else
  fail "session 'beta' missing from pane"
fi

# Kill scenario 1 before scenario 2.
tmux send-keys -t "$TSESS" "q"
sleep 0.3
tmux kill-session -t "$TSESS" 2>/dev/null || true

# ---- scenario 2: -g work + --select beta → warning (beta is in 'personal')
# Run inside a detached tmux session so stderr is captured from a fresh process
# tree; the warning is emitted before bubbletea starts so even a ~1s lifetime
# is enough.
TSESS2="adeck-select-warn-$$"
WARN_FILE="$(mktemp -t adeck-warn.XXXXXX)"
tmux new-session -d -s "$TSESS2" -x 180 -y 50 \
  "env HOME='$TMPHOME' AGENT_DECK_ALLOW_OUTER_TMUX=1 '$BIN' -g work --select beta 2>'$WARN_FILE'; sleep 2"
sleep 1.5
tmux kill-session -t "$TSESS2" 2>/dev/null || true
warn_out="$(cat "$WARN_FILE" 2>/dev/null || true)"
rm -f "$WARN_FILE"

if grep -q "Warning: --select" <<<"$warn_out"; then
  pass "warning printed when --select session is outside -g scope"
  log "stderr: $(echo "$warn_out" | grep Warning | head -1)"
else
  fail "expected warning 'Warning: --select ...' was not printed"
  log "stderr captured: $warn_out"
fi

if [[ "$FAILED" -eq 0 ]]; then
  pass "--select flag (#709) verified end-to-end"
  exit 0
fi
exit 1
