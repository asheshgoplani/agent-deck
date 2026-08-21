#!/usr/bin/env bash
# run-gates.sh — run SIXGATE end to end for one feature, under the sandbox preamble.
#
# usage: run-gates.sh <slug> [repo]
#
# Every command below runs with a throwaway HOME and every XDG_* cleared. That is not
# belt-and-braces: this repository IS the maintainer's live data directory, and an
# un-sandboxed run has destroyed the profile index three times. The preamble is applied
# once, here, so no individual invocation can forget it.
#
# The ordering is load-bearing in one non-obvious place. G4 emits the list of figures
# that have no source of truth, and G2 has to assert that each of them is labelled an
# estimate on screen — so G2 runs TWICE: once to check the journey, and again after G4
# has written the contract it must obey. `sixgate verdict --check` refuses if the
# second run never happened.
#
# G5 is deliberately NOT run here. A cold-eye review needs a human or an agent that has
# never seen this code, and a script that pretended to do it would be the exact kind of
# claim this framework exists to replace with an artifact. See coldeye-review.sh.
set -euo pipefail

SLUG="${1:?usage: run-gates.sh <slug> [repo]}"
REPO="${2:-$(git rev-parse --show-toplevel)}"
SIXGATE="${SIXGATE_BIN:-$REPO/build/sixgate}"

sandboxed() {
  HOME="$(mktemp -d)" \
  XDG_CONFIG_HOME= XDG_DATA_HOME= XDG_CACHE_HOME= \
  CLAUDE_CONFIG_DIR= AGENTDECK_PROFILE= \
  GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}" \
  GOCACHE="${GOCACHE:-$HOME/Library/Caches/go-build}" \
  "$@"
}

step() { printf '\n\033[1m== %s\033[0m\n' "$*"; }

step "building the gate runner"
sandboxed go build -o "$SIXGATE" ./tools/sixgate

step "selfcheck — the gate on the gates"
sandboxed "$SIXGATE" selfcheck -repo "$REPO"

step "G0 — the journey, as written before the code"
sandboxed "$SIXGATE" validate "$SLUG" -repo "$REPO"

step "G1 — drive the journey (in-process model: no tmux, no PTY)"
sandboxed "$SIXGATE" drive "$SLUG" -repo "$REPO"

step "G1/G3 — drive the SHIPPED BINARY in a real pane, and prove the socket was given back"
sandboxed "$SIXGATE" drive-b "$SLUG" -repo "$REPO"

step "G2 — assert on the recorded frames"
sandboxed "$SIXGATE" assert "$SLUG" -repo "$REPO"

step "G3 — every declared world, each in its own sandbox"
sandboxed "$SIXGATE" matrix "$SLUG" -repo "$REPO"

step "G4 — compare every figure against an oracle; emit the must-label contract"
sandboxed "$SIXGATE" oracle compare "$SLUG" -repo "$REPO"

step "G2 again — now bound by G4's contract: every unoracled figure must say so on screen"
sandboxed "$SIXGATE" assert "$SLUG" -repo "$REPO"

step "G5 — grade whatever the cold-eye reviewer returned"
sandboxed "$SIXGATE" coldeye outcome "$SLUG" -repo "$REPO"

step "VERDICT"
sandboxed "$SIXGATE" verdict "$SLUG" -repo "$REPO"

step "THE GATE"
sandboxed "$SIXGATE" verdict "$SLUG" -repo "$REPO" --check
