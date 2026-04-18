#!/usr/bin/env bash
# verify-tui-eval-seam-c.sh — POC for Seam C of the TUI evaluator infra.
#
# SEAM C: headless tmux. Spawn the real agent-deck binary in a detached
# tmux window, drive it with `tmux send-keys`, and observe via
# `tmux capture-pane`. Assert on captured pane text + optional SQLite
# state.
#
# Why this seam: catches bugs invisible to Seam A (model) and Seam B
# (teatest) — real terminal resize handling, real signal delivery, real
# Bubble Tea alt-screen behavior, real tmux control-mode integration.
# Slow (~5s per scenario), Linux-friendly, CI-friendly if tmux is
# available.
#
# NOT a replacement for Seam A/B — use Seam C when a bug has been
# reported that touches the terminal itself, or when you need to prove
# an end-to-end user symptom before trusting a model-level fix.
#
# Usage: bash scripts/verify-tui-eval-seam-c.sh
# Env:
#   AGENT_DECK_BIN  — path to binary (default: ./agent-deck)
#   KEEP_SESSION=1  — leave the tmux session after success for manual inspection
set -euo pipefail

# ---------- colors ----------
C_RED='\033[31m'; C_GREEN='\033[32m'; C_YELLOW='\033[33m'; C_RESET='\033[0m'
pass() { printf "${C_GREEN}[PASS]${C_RESET} %s\n" "$*"; }
fail() { printf "${C_RED}[FAIL]${C_RESET} %s\n" "$*" >&2; FAILED=1; }
skip() { printf "${C_YELLOW}[SKIP]${C_RESET} %s\n" "$*"; }
log()  { printf "    %s\n" "$*"; }

FAILED=0
BIN="${AGENT_DECK_BIN:-./agent-deck}"
TSESS="adeck-seamc-$$"
TMPHOME="$(mktemp -d -t adeck-seamc.XXXXXX)"

cleanup() {
  set +e
  if [[ "${KEEP_SESSION:-0}" != "1" ]]; then
    tmux kill-session -t "$TSESS" 2>/dev/null || true
    [[ -d "$TMPHOME" && "$TMPHOME" == /tmp/adeck-seamc.* ]] && rm -rf "$TMPHOME"
  else
    echo "session preserved: tmux attach -t $TSESS"
    echo "fake HOME preserved: $TMPHOME"
  fi
}
trap cleanup EXIT INT TERM

# ---------- preflight ----------
command -v tmux >/dev/null || { skip "tmux not installed"; exit 0; }
[[ -x "$BIN" ]] || { fail "binary not found at $BIN (run: go build -o agent-deck ./cmd/agent-deck)"; exit 1; }

# Isolate state: fresh HOME so we don't touch the user's sessions / config.
mkdir -p "$TMPHOME/.agent-deck"
cat > "$TMPHOME/.agent-deck/config.toml" <<'EOF'
[tmux]
inject_status_line = false
EOF

# ---------- scenario: help overlay round-trip ----------
# Proves (a) agent-deck starts inside tmux, (b) '?' opens help, (c) 'q'
# dismisses it, (d) output is captured. Any of these failing indicates
# the terminal layer has regressed.

tmux new-session -d -s "$TSESS" -x 180 -y 50 \
  "env HOME='$TMPHOME' AGENT_DECK_ALLOW_OUTER_TMUX=1 '$BIN'"

# Wait for the splash/home to render. Cheap polling on pane content.
for _ in $(seq 1 30); do
  sleep 0.2
  out="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
  # agent-deck prints its title / splash early. Anything with "agent-deck" or "Agent Deck" is fine.
  if grep -qi "agent[- ]deck" <<<"$out"; then
    break
  fi
done

out="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
if ! grep -qi "agent[- ]deck" <<<"$out"; then
  fail "agent-deck did not render its home within 6s"
  log "captured pane:"
  echo "$out" | head -15 | sed 's/^/      /'
  exit 1
fi
pass "agent-deck started inside tmux"

# Dismiss any first-run prompts (hooks wizard, etc). 'n' skips hooks;
# Esc covers generic wizards. Two rounds is defensive.
tmux send-keys -t "$TSESS" "n"
sleep 0.3
tmux send-keys -t "$TSESS" "Escape"
sleep 0.3

# Drive '?' to open help.
tmux send-keys -t "$TSESS" "?"
sleep 0.4

out="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
# Help overlay contains well-known literal text. Check a couple of lines.
if grep -q "any other key to close\|WATCHERS\|WORKTREES" <<<"$out"; then
  pass "help overlay rendered after '?'"
else
  fail "help overlay not visible after '?'"
  log "captured pane (last 20 lines):"
  echo "$out" | tail -20 | sed 's/^/      /'
fi

# Dismiss (q).
tmux send-keys -t "$TSESS" "q"
sleep 0.4
out_after="$(tmux capture-pane -t "$TSESS" -p 2>/dev/null || true)"
if grep -q "any other key to close" <<<"$out_after"; then
  fail "help overlay stayed open after 'q'"
else
  pass "help overlay dismissed"
fi

# Quit cleanly (Ctrl+C is safer than q on root view which could prompt).
tmux send-keys -t "$TSESS" "q"
sleep 0.3

if [[ "$FAILED" -eq 0 ]]; then
  pass "Seam C smoke complete"
  exit 0
else
  exit 1
fi
