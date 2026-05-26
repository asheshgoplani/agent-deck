#!/usr/bin/env bash
#
# echobot: a deterministic stand-in agent for the capability E2E suite.
#
# It plays the role a real coding agent (claude/codex/gemini) plays in a tmux
# pane, minus the non-determinism, the network, and the API key:
#
#   1. On start it prints a fixed ready marker, "ECHOBOT READY", so
#      agent-deck's PromptDetector (configured via prompt_patterns in
#      config.toml) sees a ready prompt and the readiness gate in
#      `waitForAgentReady` opens.
#   2. It then loops: read one line from stdin (the literal keystrokes that
#      `session send` delivers via tmux send-keys + Enter) and echo it back as
#      "ECHO:<line>", then reprint the ready marker so the next send is gated
#      the same way.
#
# The round-trip test asserts the pane contains "ECHO:PING-<uuid>", which
# proves the full production path: readiness detection -> send-keys -> Enter ->
# capture-pane read-back. The only thing made deterministic is the brain on the
# far end. See docs/testing/2026-05-26-capability-e2e-strategy.md.
set -u

ready() { printf 'ECHOBOT READY > '; }

# Braille spinner frames. These are agent-deck's default spinner characters
# (internal/tmux/patterns.go defaultSpinnerChars). A real agent animates a
# spinner like this while it is thinking, and agent-deck's status detector
# reads that as the 'active' state.
frames="⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏"

ready
while IFS= read -r line; do
  # Present a visible, sustained "busy" state after receiving a line. This is
  # not cosmetic: agent-deck's send-delivery verifier (issue #876) only treats
  # a send as delivered once the agent transitions to 'active'. Animating the
  # spinner is the authoritative active signal (the spinner fallback in
  # hasBusyIndicatorResolved marks any spinner char busy for non-claude tools),
  # and it keeps tmux window-activity changing across several status-check
  # cycles (the verifier polls every ~300ms and needs roughly two active
  # reads). Without it the verifier exhausts its budget and fires a Ctrl+C
  # resend that would kill this stand-in. ~2s of animation covers the window.
  for _ in 1 2 3 4; do
    for f in $frames; do
      printf '\r%s working' "$f"
      sleep 0.05
    done
  done
  printf '\nECHO:%s\n' "$line"
  ready
done
