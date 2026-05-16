#!/usr/bin/env bash
# verify-command-override.sh — End-to-end verification for [tool].command and
# [tool].env_file config overrides across all builtin agents.
#
# Tests that:
#   1. Bare tool name (no config) → tmux runs the bare binary
#   2. [tool].command override → tmux runs the overridden command
#   3. CLI -c "tool --flags" → passthrough wins over config override
#   4. [tool].yolo_mode (hermes) → --yolo appears in argv
#   5. [tool].env_file → source prefix appears before command
#
# Usage:
#   ./scripts/verify-command-override.sh           # Run all scenarios
#   SCENARIO=3 ./scripts/verify-command-override.sh  # Run only scenario 3
#
# Requires: agent-deck binary on PATH (or built at ./agent-deck), tmux.
# Uses fake tool stubs — no real AI binaries needed.
set -euo pipefail

# ---------- color + logging ----------
readonly C_RED='\033[31m'
readonly C_GREEN='\033[32m'
readonly C_YELLOW='\033[33m'
readonly C_RESET='\033[0m'

banner_pass() { printf "${C_GREEN}[PASS]${C_RESET} %s\n" "$*"; }
banner_fail() { printf "${C_RED}[FAIL]${C_RESET} %s\n" "$*" >&2; FAILED=1; }
banner_skip() { printf "${C_YELLOW}[SKIP]${C_RESET} %s\n" "$*"; }
log() { printf '    %s\n' "$*"; }

FAILED=0
RUN_ID="$$"
TMPROOT="$(mktemp -d -t adeck-cmdoverride.XXXXXX)"
TEST_HOME="${TMPROOT}/home"
STUB_DIR="$(cd "$(dirname "$0")/verify-command-override.d" && pwd)"
ARGV_OUT="${TMPROOT}/argv.log"
SESSION_PREFIX="verify-cmd-${RUN_ID}"
TMUX_SOCKET="adeck-verify-cmd-${RUN_ID}"

export AGENT_DECK_VERIFY_ARGV_OUT="${ARGV_OUT}"

# ---------- cleanup ----------
cleanup() {
  set +e
  # Kill the isolated tmux server
  tmux -L "${TMUX_SOCKET}" kill-server >/dev/null 2>&1 || true
  # Remove tempdir
  if [[ -n "${TMPROOT}" && "${TMPROOT}" == /tmp/adeck-cmdoverride.* ]]; then
    rm -rf "${TMPROOT}" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

# ---------- preflight ----------
AGENT_DECK_BIN=""
if [[ -x "./agent-deck" ]]; then
  AGENT_DECK_BIN="$(pwd)/agent-deck"
elif command -v agent-deck >/dev/null 2>&1; then
  AGENT_DECK_BIN="$(command -v agent-deck)"
else
  printf "${C_RED}ERROR${C_RESET}: agent-deck binary not found (./agent-deck or PATH).\n" >&2
  exit 2
fi
if ! command -v tmux >/dev/null 2>&1; then
  printf "${C_RED}ERROR${C_RESET}: tmux binary not on PATH.\n" >&2
  exit 2
fi

log "agent-deck: ${AGENT_DECK_BIN}"
log "tmux socket: ${TMUX_SOCKET}"
log "test home: ${TEST_HOME}"

# ---------- setup isolated environment ----------
mkdir -p "${TEST_HOME}/.agent-deck"
mkdir -p "${TMPROOT}/bin"
mkdir -p "${TMPROOT}/project"

# Create symlinks for all tool stubs (bare names + common override names)
for tool in hermes gemini opencode codex copilot claude gemini-nightly codex-experimental gh; do
  ln -sf "${STUB_DIR}/fake-tool.sh" "${TMPROOT}/bin/${tool}"
done
# Also symlink agent-deck into our bin dir
ln -sf "${AGENT_DECK_BIN}" "${TMPROOT}/bin/agent-deck"

# ---------- helpers ----------
write_config() {
  cat > "${TEST_HOME}/.agent-deck/config.toml" <<EOF
[tmux]
socket_name = "${TMUX_SOCKET}"

$1
EOF
}

# Run agent-deck with isolated HOME
ad() {
  HOME="${TEST_HOME}" PATH="${TMPROOT}/bin:${PATH}" \
    TERM=dumb NO_COLOR=1 AGENTDECK_COLOR=none \
    agent-deck "$@"
}

# Create a session, start it, wait, capture argv.
# Outputs TWO lines: line 1 = stub argv (tool-level), line 2 = pane_start_command (full bash -c wrapper).
# Callers use get_stub_argv / get_pane_cmd to extract.
run_session() {
  local name="$1"
  local tool_or_cmd="$2"
  shift 2
  local extra_args=("$@")

  : > "${ARGV_OUT}"

  # Create session
  ad add -t "${name}" -c "${tool_or_cmd}" "${extra_args[@]}" "${TMPROOT}/project" >/dev/null 2>&1

  # Start session
  ad session start "${name}" >/dev/null 2>&1
  sleep 2

  # Capture stub argv (what the fake tool binary received)
  local stub_argv=""
  if [[ -s "${ARGV_OUT}" ]]; then
    stub_argv="$(tail -1 "${ARGV_OUT}")"
  fi

  # Capture pane_start_command (full bash -c wrapper including source prefix)
  local pane_cmd=""
  local tmux_sess
  tmux_sess="$(ad session show --json "${name}" 2>/dev/null | jq -r '.tmux_session // empty' 2>/dev/null || true)"
  if [[ -n "${tmux_sess}" && "${tmux_sess}" != "null" ]]; then
    pane_cmd="$(tmux -L "${TMUX_SOCKET}" list-panes -t "${tmux_sess}" -F '#{pane_start_command}' 2>/dev/null | head -1 || true)"
  fi

  # Stop session
  ad session stop "${name}" >/dev/null 2>&1 || true
  sleep 1

  # Return both (newline-separated). Use LAST_STUB_ARGV / LAST_PANE_CMD globals.
  LAST_STUB_ARGV="${stub_argv}"
  LAST_PANE_CMD="${pane_cmd}"
}

# Convenience: run session and return stub argv (for command/flag assertions)
get_argv() {
  echo "${LAST_STUB_ARGV}"
}

# Convenience: return pane_start_command (for env_file/source assertions)
get_pane_cmd() {
  echo "${LAST_PANE_CMD}"
}

want_scenario() {
  local n="$1"
  if [[ -z "${SCENARIO:-}" ]]; then return 0; fi
  [[ "${SCENARIO}" == "${n}" ]]
}

# ---------- checklist ----------
cat <<'EOF'
==========================================================
verify-command-override.sh — command/env_file override e2e
==========================================================
[ 1] hermes: bare name (no config override)
[ 2] hermes: [hermes].command override
[ 3] hermes: [hermes].yolo_mode = true
[ 4] hermes: CLI extra args combine with config override
[ 5] hermes: [hermes].env_file
[ 6] gemini: bare name
[ 7] gemini: [gemini].command override
[ 8] gemini: [gemini].env_file
[ 9] opencode: bare name
[10] opencode: [opencode].command override
[11] opencode: [opencode].env_file
[12] codex: bare name
[13] codex: [codex].command override
[14] codex: [codex].env_file
[15] copilot: bare name
[16] copilot: [copilot].command override
[17] copilot: [copilot].env_file
==========================================================
EOF

# ---------- Scenario 1: hermes bare ----------
if want_scenario 1; then
  write_config ''
  run_session "${SESSION_PREFIX}-s1" "hermes"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "^hermes" || echo "${LAST_PANE_CMD}" | grep -q "hermes"; then
    banner_pass "[1] hermes bare name → runs 'hermes'"
  else
    banner_fail "[1] hermes bare name: expected 'hermes' in output"
  fi
fi

# ---------- Scenario 2: hermes command override ----------
if want_scenario 2; then
  write_config '[hermes]
command = "hermes --model gpt-5.5-pro --provider openai"'
  run_session "${SESSION_PREFIX}-s2" "hermes"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q -- "--model gpt-5.5-pro" || echo "${LAST_PANE_CMD}" | grep -q -- "--model gpt-5.5-pro"; then
    banner_pass "[2] hermes command override → runs overridden command"
  else
    banner_fail "[2] hermes command override: expected '--model gpt-5.5-pro' in output"
  fi
fi

# ---------- Scenario 3: hermes yolo_mode ----------
if want_scenario 3; then
  write_config '[hermes]
yolo_mode = true'
  run_session "${SESSION_PREFIX}-s3" "hermes"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q -- "--yolo" || echo "${LAST_PANE_CMD}" | grep -q -- "--yolo"; then
    banner_pass "[3] hermes yolo_mode = true → --yolo in argv"
  else
    banner_fail "[3] hermes yolo_mode: expected '--yolo' in output"
  fi
fi

# ---------- Scenario 4: hermes CLI extra args via wrapper ----------
if want_scenario 4; then
  write_config '[hermes]
command = "hermes --model gpt-5.5-pro"'
  run_session "${SESSION_PREFIX}-s4" "hermes --special-flag"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  # CLI "-c hermes --special-flag" splits into tool=hermes + wrapper="{command} --special-flag"
  # Config override applies (--model gpt-5.5-pro), then wrapper appends --special-flag
  combined="${LAST_STUB_ARGV} ${LAST_PANE_CMD}"
  if echo "${combined}" | grep -q -- "--special-flag" && echo "${combined}" | grep -q -- "--model gpt-5.5-pro"; then
    banner_pass "[4] hermes CLI extra args: config override + wrapper both applied"
  else
    banner_fail "[4] hermes CLI extra args: expected both '--model gpt-5.5-pro' and '--special-flag' in output"
  fi
fi

# ---------- Scenario 5: hermes env_file ----------
if want_scenario 5; then
  echo 'HERMES_TEST_VAR=1' > "${TMPROOT}/hermes.env"
  write_config "[hermes]
env_file = \"${TMPROOT}/hermes.env\""
  run_session "${SESSION_PREFIX}-s5" "hermes"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_PANE_CMD}" | grep -q "hermes.env"; then
    banner_pass "[5] hermes env_file → source prefix in pane command"
  else
    banner_fail "[5] hermes env_file: no 'hermes.env' in pane_start_command"
  fi
fi

# ---------- Scenario 6: gemini bare ----------
if want_scenario 6; then
  write_config ''
  run_session "${SESSION_PREFIX}-s6" "gemini"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "^gemini" || echo "${LAST_PANE_CMD}" | grep -q "gemini"; then
    banner_pass "[6] gemini bare name → runs 'gemini'"
  else
    banner_fail "[6] gemini bare name: expected 'gemini' in output"
  fi
fi

# ---------- Scenario 7: gemini command override ----------
if want_scenario 7; then
  write_config '[gemini]
command = "gemini-nightly --experimental"'
  run_session "${SESSION_PREFIX}-s7" "gemini"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "gemini-nightly" || echo "${LAST_PANE_CMD}" | grep -q "gemini-nightly"; then
    banner_pass "[7] gemini command override → runs overridden command"
  else
    banner_fail "[7] gemini command override: expected 'gemini-nightly' in output"
  fi
fi

# ---------- Scenario 8: gemini env_file ----------
if want_scenario 8; then
  echo 'GEMINI_KEY=test' > "${TMPROOT}/gemini.env"
  write_config "[gemini]
env_file = \"${TMPROOT}/gemini.env\""
  run_session "${SESSION_PREFIX}-s8" "gemini"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_PANE_CMD}" | grep -q "gemini.env"; then
    banner_pass "[8] gemini env_file → source prefix in pane command"
  else
    banner_fail "[8] gemini env_file: no 'gemini.env' in pane_start_command"
  fi
fi

# ---------- Scenario 9: opencode bare ----------
if want_scenario 9; then
  write_config ''
  run_session "${SESSION_PREFIX}-s9" "opencode"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "opencode" || echo "${LAST_PANE_CMD}" | grep -q "opencode"; then
    banner_pass "[9] opencode bare name → runs 'opencode'"
  else
    banner_fail "[9] opencode bare name: expected 'opencode' in output"
  fi
fi

# ---------- Scenario 10: opencode command override ----------
if want_scenario 10; then
  write_config '[opencode]
command = "opencode --custom-flag"'
  run_session "${SESSION_PREFIX}-s10" "opencode"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q -- "--custom-flag" || echo "${LAST_PANE_CMD}" | grep -q -- "--custom-flag"; then
    banner_pass "[10] opencode command override → runs overridden command"
  else
    banner_fail "[10] opencode command override: expected '--custom-flag' in output"
  fi
fi

# ---------- Scenario 11: opencode env_file ----------
if want_scenario 11; then
  echo 'OC_KEY=test' > "${TMPROOT}/opencode.env"
  write_config "[opencode]
env_file = \"${TMPROOT}/opencode.env\""
  run_session "${SESSION_PREFIX}-s11" "opencode"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_PANE_CMD}" | grep -q "opencode.env"; then
    banner_pass "[11] opencode env_file → source prefix in pane command"
  else
    banner_fail "[11] opencode env_file: no 'opencode.env' in pane_start_command"
  fi
fi

# ---------- Scenario 12: codex bare ----------
if want_scenario 12; then
  write_config ''
  run_session "${SESSION_PREFIX}-s12" "codex"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "codex" || echo "${LAST_PANE_CMD}" | grep -q "codex"; then
    banner_pass "[12] codex bare name → runs 'codex'"
  else
    banner_fail "[12] codex bare name: expected 'codex' in output"
  fi
fi

# ---------- Scenario 13: codex command override ----------
if want_scenario 13; then
  write_config '[codex]
command = "codex-experimental"'
  run_session "${SESSION_PREFIX}-s13" "codex"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "codex-experimental" || echo "${LAST_PANE_CMD}" | grep -q "codex-experimental"; then
    banner_pass "[13] codex command override → runs overridden command"
  else
    banner_fail "[13] codex command override: expected 'codex-experimental' in output"
  fi
fi

# ---------- Scenario 14: codex env_file ----------
if want_scenario 14; then
  echo 'CODEX_KEY=test' > "${TMPROOT}/codex.env"
  write_config "[codex]
env_file = \"${TMPROOT}/codex.env\""
  run_session "${SESSION_PREFIX}-s14" "codex"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_PANE_CMD}" | grep -q "codex.env"; then
    banner_pass "[14] codex env_file → source prefix in pane command"
  else
    banner_fail "[14] codex env_file: no 'codex.env' in pane_start_command"
  fi
fi

# ---------- Scenario 15: copilot bare ----------
if want_scenario 15; then
  write_config ''
  run_session "${SESSION_PREFIX}-s15" "copilot"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "copilot" || echo "${LAST_PANE_CMD}" | grep -q "copilot"; then
    banner_pass "[15] copilot bare name → runs 'copilot'"
  else
    banner_fail "[15] copilot bare name: expected 'copilot' in output"
  fi
fi

# ---------- Scenario 16: copilot command override ----------
if want_scenario 16; then
  write_config '[copilot]
command = "gh copilot-chat"'
  run_session "${SESSION_PREFIX}-s16" "copilot"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_STUB_ARGV}" | grep -q "copilot-chat" || echo "${LAST_PANE_CMD}" | grep -q "copilot-chat"; then
    banner_pass "[16] copilot command override → runs overridden command"
  else
    banner_fail "[16] copilot command override: expected 'copilot-chat' in output"
  fi
fi

# ---------- Scenario 17: copilot env_file ----------
if want_scenario 17; then
  echo 'GH_TOKEN=test' > "${TMPROOT}/copilot.env"
  write_config "[copilot]
env_file = \"${TMPROOT}/copilot.env\""
  run_session "${SESSION_PREFIX}-s17" "copilot"
  log "stub_argv: ${LAST_STUB_ARGV}"
  log "pane_cmd:  ${LAST_PANE_CMD}"
  if echo "${LAST_PANE_CMD}" | grep -q "copilot.env"; then
    banner_pass "[17] copilot env_file → source prefix in pane command"
  else
    banner_fail "[17] copilot env_file: no 'copilot.env' in pane_start_command"
  fi
fi

# ---------- summary ----------
echo "=========================================================="
if [[ "${FAILED}" -ne 0 ]]; then
  printf "${C_RED}OVERALL: FAIL${C_RESET}\n" >&2
  exit 1
fi
printf "${C_GREEN}OVERALL: PASS${C_RESET}\n"
exit 0
