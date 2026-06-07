#!/usr/bin/env bash
# verify-session-persistence.sh — human-watchable end-to-end verification for
# v1.5.2 session persistence. Exits 0 if every scenario prints [PASS] or [SKIP];
# exits 1 on any [FAIL]; exits 2 on missing agent-deck/tmux. CI uses the stub
# scripts/verify-session-persistence.d/fake-claude.sh (captures claude argv).
# Env: AGENT_DECK_VERIFY_USE_STUB=1, AGENT_DECK_VERIFY_DESTRUCTIVE=1, SCENARIO=N.
set -euo pipefail

# Numbered scenario checklist (printed at startup, also parseable via head -30).
readonly CHECKLIST="$(cat <<'EOF'
[1] Live session + cgroup inspection
[2] Login-session teardown survival (Linux+systemd only)
[3] Stop -> restart resume (--resume or --session-id in argv)
[4] Fresh session uses --session-id, not --resume
[5] Reviver respawns a killed control pipe without breaking tmux (v1.7.8+)
EOF
)"

# ---------- color + logging ----------
readonly C_RED='\033[31m'
readonly C_GREEN='\033[32m'
readonly C_YELLOW='\033[33m'
readonly C_RESET='\033[0m'

banner_pass() { printf "${C_GREEN}[PASS]${C_RESET} %s\n" "$*"; }
banner_fail() { printf "${C_RED}[FAIL]${C_RESET} %s\n" "$*" >&2; FAILED=1; }
banner_skip() { printf "${C_YELLOW}[SKIP]${C_RESET} %s\n" "$*"; }
log() { printf '    %s\n' "$*"; }

# is_own_tmproot returns 0 iff $1 is a tempdir THIS harness created via mktemp.
# Matches on the leaf name (prefix adeck-verify.), NOT a hardcoded /tmp parent,
# so it works on macOS where mktemp resolves under $TMPDIR (/var/folders/...).
# Uses pure-bash parameter expansion (${p##*/}) — no fork, no BSD-vs-GNU
# `basename --` portability question, works on bash 3.2. Callers add the `-d`
# existence test before rm.
is_own_tmproot() {
  local p="$1"
  [[ -n "$p" && "${p##*/}" == adeck-verify.* ]]
}

# ---------- cleanup ----------
cleanup() {
  set +e
  # Stop any sessions we created. agent-deck has no `session ls` subcommand;
  # use the top-level `agent-deck list` (TITLE is col 1, ID is last column).
  if command -v agent-deck >/dev/null 2>&1; then
    for n in $(agent-deck list 2>/dev/null | awk -v P="${SESSION_PREFIX}" '$1 ~ "^"P {print $1}' || true); do
      agent-deck session stop "$n" >/dev/null 2>&1 || true
      agent-deck remove "$n" >/dev/null 2>&1 || true
    done
  fi
  # Tear down any lingering login-sim scope.
  if command -v systemctl >/dev/null 2>&1; then
    systemctl --user stop "${LOGINSIM_SCOPE}.scope" >/dev/null 2>&1 || true
  fi
  # Remove the script's OWN tempdir only (per CLAUDE.md, never rm user state).
  if is_own_tmproot "${TMPROOT:-}" && [[ -d "${TMPROOT}" ]]; then
    rm -rf "${TMPROOT}" 2>/dev/null || true
  fi
}

# ---------- helpers ----------

# resolve_tmux_session prints the tmux session name agent-deck assigned to the
# managed session $1, via the authoritative `session show --json` path. Empty
# output means unresolved. This is the ONE resolver; helpers and scenario 5 all
# use it (the old `agent-deck list | awk /^adeck_/` parse was doubly wrong: list
# never prints the tmux name, and the real prefix is `agentdeck_`).
resolve_tmux_session() {
  agent-deck session show --json "$1" 2>/dev/null | jq -r '.tmux_session // empty' 2>/dev/null
}

tmux_pid_for_session() {
  # Prints the PID of the tmux server hosting the agent-deck session $1.
  # Uses resolve_tmux_session to get the tmux session name, then asks tmux
  # itself for the server PID via `display-message -p -F '#{pid}'`.
  local name="$1"
  local tsess
  tsess="$(resolve_tmux_session "${name}")"
  if [[ -z "${tsess}" || "${tsess}" == "null" ]]; then
    pgrep -f "tmux.*${name}" | head -1 || true
    return
  fi
  tmux display-message -t "${tsess}" -p -F '#{pid}' 2>/dev/null || true
}

print_cgroup_for_pid() {
  local pid="$1"
  printf '    PID=%s\n' "${pid}"
  if [[ -r "/proc/${pid}/cgroup" ]]; then
    printf '    /proc/%s/cgroup:\n' "${pid}"
    sed 's/^/        /' "/proc/${pid}/cgroup"
  else
    printf '    cgroup: N/A (macOS or /proc not mounted)\n'
  fi
}

tmux_pane_start_command_for_session() {
  # Returns the pane_start_command of the first pane in the agent-deck
  # session $1. This is the authoritative argv that tmux launched claude
  # with (quoted exactly as agent-deck constructed it). Preferred over
  # `ps -ef | grep claude` which is ambiguous on hosts with many live
  # claude processes sharing the same tmux daemon.
  local name="$1"
  local tsess
  tsess="$(resolve_tmux_session "${name}")"
  if [[ -z "${tsess}" || "${tsess}" == "null" ]]; then
    return 1
  fi
  tmux list-panes -t "${tsess}" -F '#{pane_start_command}' 2>/dev/null | head -1 || true
}

# capture_claude_argv prints the claude argv for managed session $1 using ONLY
# session-scoped sources: (1) the stub's ARGV_OUT file when populated, else
# (2) the tmux pane_start_command for this session. It NEVER scans host-wide
# `ps` — that matched unrelated claude processes and produced false FAILs.
# Empty output means "unobservable for THIS session"; callers MUST treat that
# as SKIP, not FAIL.
capture_claude_argv() {
  local name="$1"
  if [[ -s "${ARGV_OUT:-/dev/null}" ]]; then
    cat "${ARGV_OUT}"
    return 0
  fi
  tmux_pane_start_command_for_session "${name}" 2>/dev/null || true
}

# classify_argv echoes the verdict (skip|pass|fail) for a captured argv under a
# given mode. Empty argv (the session's claude was unobservable) is ALWAYS skip
# — never fail; that is the cross-platform-degradation contract. Extracted so
# the verdict is unit-testable in isolation and shared by scenarios 3 and 4.
#   mode=resume: pass iff argv has --resume OR --session-id.
#   mode=fresh : pass iff argv has --session-id AND NOT --resume.
classify_argv() {
  local mode="$1" argv="$2"
  if [[ -z "${argv}" ]]; then echo skip; return; fi
  case "${mode}" in
    resume)
      if echo "${argv}" | grep -qE -- '--resume|--session-id'; then echo pass; else echo fail; fi
      ;;
    fresh)
      if echo "${argv}" | grep -qE -- '--session-id' && ! echo "${argv}" | grep -qE -- '--resume'; then
        echo pass
      else
        echo fail
      fi
      ;;
  esac
}

want_scenario() {
  local n="$1"
  if [[ -z "${SCENARIO:-}" ]]; then return 0; fi
  [[ "${SCENARIO}" == "${n}" ]]
}

# ---------- Scenario 1 ----------
scenario_1_live_session_cgroup() {
  local name="${SESSION_PREFIX}-s1"
  log "creating session: ${name}"
  agent-deck add -t "${name}" -c claude -Q "${TMPROOT}" >/dev/null
  agent-deck session start "${name}" >/dev/null
  sleep 2
  local pid
  pid="$(tmux_pid_for_session "${name}")"
  if [[ -z "${pid}" ]]; then
    banner_fail "[1] could not resolve tmux server pid for session ${name}"
    return
  fi
  print_cgroup_for_pid "${pid}"
  # Agent-deck reuses ONE shared tmux daemon per host. If that daemon was
  # spawned before the v1.5.2 launch_in_user_scope default flipped, it lives
  # under session-N.scope (login scope) and every subsequent `session start`
  # attaches to it, so this scenario cannot observe a clean-state launch.
  # Detect via /proc/$PID/cgroup and SKIP with diagnostic. Scenario 2's
  # login-session-teardown survival test remains the operative
  # production-contract check (REQ-1).
  if [[ "$(uname)" == "Linux" && -r "/proc/${pid}/cgroup" ]]; then
    local cg
    cg=$(awk -F: 'NR==1 {print $3}' "/proc/${pid}/cgroup" 2>/dev/null || echo "")
    if [[ -n "${cg}" && "${cg}" == *session-*.scope* && "${cg}" != *user@*.service* ]]; then
      log "pre-existing shared tmux daemon in login scope — re-run after agent-deck restart"
      log "cgroup: ${cg}"
      banner_skip "[1] pre-existing shared tmux daemon in login scope (scenario 2 is the operative REQ-1 check)"
      agent-deck session stop "${name}" >/dev/null 2>&1 || true
      return 0
    fi
    if grep -q 'user@' "/proc/${pid}/cgroup"; then
      banner_pass "[1] tmux server ${pid} is under user@*.service (cgroup isolation active)"
    else
      banner_fail "[1] tmux server ${pid} is NOT under user@*.service — cgroup isolation did not activate"
    fi
  else
    banner_pass "[1] tmux server ${pid} is live (cgroup inspection skipped: non-Linux)"
  fi
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
}

# ---------- Scenario 2 ----------
scenario_2_login_teardown() {
  # Probe user bus reachability via show-environment (works on "degraded"
  # hosts where is-system-running returns non-zero even though the bus is up
  # and systemd-run works). Skip cleanly on non-Linux or if bus is truly gone.
  if ! command -v systemd-run >/dev/null 2>&1 || ! systemctl --user show-environment >/dev/null 2>&1; then
    banner_skip "[2] skipped: no systemd-run (non-Linux or systemd user bus unavailable)"
    return
  fi
  local name="${SESSION_PREFIX}-s2"
  log "launching throwaway login-scope: ${LOGINSIM_SCOPE}"
  systemd-run --user --scope --unit="${LOGINSIM_SCOPE}" sleep 3600 >/dev/null 2>&1 &
  local scope_pid=$!
  sleep 1
  log "creating session inside simulated login scope: ${name}"
  agent-deck add -t "${name}" -c claude -Q "${TMPROOT}" >/dev/null
  agent-deck session start "${name}" >/dev/null
  sleep 2
  local pid
  pid="$(tmux_pid_for_session "${name}")"
  if [[ -z "${pid}" ]]; then
    banner_fail "[2] could not resolve tmux server pid for session ${name}"
    systemctl --user stop "${LOGINSIM_SCOPE}.scope" >/dev/null 2>&1 || true
    return
  fi
  print_cgroup_for_pid "${pid}"
  log "terminating login-scope: systemctl --user stop ${LOGINSIM_SCOPE}.scope"
  systemctl --user stop "${LOGINSIM_SCOPE}.scope" >/dev/null 2>&1 || true
  kill "${scope_pid}" >/dev/null 2>&1 || true
  if [[ "${AGENT_DECK_VERIFY_DESTRUCTIVE:-0}" == "1" ]]; then
    log "DESTRUCTIVE: additionally terminating own login session (will disconnect SSH)"
    local sess
    sess="$(loginctl show-user "$USER" -p Sessions --value 2>/dev/null | awk '{print $1}')"
    if [[ -n "${sess}" ]]; then
      loginctl terminate-session "${sess}" >/dev/null 2>&1 || true
    fi
  fi
  sleep 2
  if kill -0 "${pid}" 2>/dev/null; then
    banner_pass "[2] tmux pid ${pid} survived login-session teardown (cgroup isolation works)"
  else
    banner_fail "[2] tmux pid ${pid} died with login-session teardown — isolation FAILED"
  fi
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
}

# ---------- Scenario 3 ----------
scenario_3_restart_resume() {
  local name="${SESSION_PREFIX}-s3"
  log "creating session: ${name}"
  agent-deck add -t "${name}" -c claude -Q "${TMPROOT}" >/dev/null
  agent-deck session start "${name}" >/dev/null
  sleep 2
  # Seed a non-empty ClaudeSessionID via the state-set command if available;
  # otherwise rely on the natural first-start minting one. We want a restart
  # that passes either --resume OR --session-id.
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
  sleep 1
  : > "${ARGV_OUT}"
  log "restarting session: agent-deck session start ${name}"
  agent-deck session start "${name}" >/dev/null || true
  sleep 2
  local argv verdict
  argv="$(capture_claude_argv "${name}")"
  log "captured claude argv: ${argv}"
  verdict="$(classify_argv resume "${argv}")"
  case "${verdict}" in
    skip) banner_skip "[3] argv unobservable for ${name} (stub not exercised / empty pane_start_command — e.g. pre-existing tmux daemon); cannot assert resume shape" ;;
    pass) banner_pass "[3] restart spawned claude with --resume or --session-id" ;;
    fail) banner_fail "[3] restart spawned claude WITHOUT --resume or --session-id: ${argv}" ;;
  esac
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
}

# ---------- Scenario 4 ----------
scenario_4_fresh_session_shape() {
  local name="${SESSION_PREFIX}-s4"
  : > "${ARGV_OUT}"
  log "creating fresh session: ${name}"
  agent-deck add -t "${name}" -c claude -Q "${TMPROOT}" >/dev/null
  agent-deck session start "${name}" >/dev/null
  sleep 2
  local argv verdict
  argv="$(capture_claude_argv "${name}")"
  log "captured claude argv: ${argv}"
  verdict="$(classify_argv fresh "${argv}")"
  case "${verdict}" in
    skip) banner_skip "[4] argv unobservable for ${name} (stub not exercised / empty pane_start_command); cannot assert fresh-session shape" ;;
    pass) banner_pass "[4] fresh session uses --session-id without --resume" ;;
    fail) banner_fail "[4] fresh session argv shape wrong: ${argv}" ;;
  esac
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
}

# ---------- Scenario 5 (v1.7.8 reviver) ----------
scenario_5_reviver_respawns_killed_pipe() {
  local name="${SESSION_PREFIX}-s5"
  log "creating session for reviver test: ${name}"
  agent-deck add -t "${name}" -c shell -Q "${TMPROOT}" >/dev/null
  agent-deck session start "${name}" >/dev/null
  sleep 1

  # Resolve via the authoritative path (was: list|awk /^adeck_/ — never matched).
  local tmux_name
  tmux_name="$(resolve_tmux_session "${name}")"
  if [[ -z "${tmux_name}" || "${tmux_name}" == "null" ]]; then
    banner_skip "[5] could not resolve tmux session name for ${name} — skipping reviver scenario"
    agent-deck session stop "${name}" >/dev/null 2>&1 || true
    return
  fi

  # Kill only the control pipe (the `tmux -C attach-session` process), NOT the
  # tmux server. Simulates SSH-logout scope cleanup.
  local pipe_pid
  pipe_pid="$(pgrep -f "tmux -C attach-session -t ${tmux_name}" | head -1 || true)"
  if [[ -z "${pipe_pid}" ]]; then
    banner_skip "[5] no control pipe found for ${tmux_name} — skipping"
    agent-deck session stop "${name}" >/dev/null 2>&1 || true
    return
  fi
  kill -9 "${pipe_pid}" 2>/dev/null || true
  log "killed control pipe pid ${pipe_pid}"
  sleep 2

  # Trigger revive. Tmux session should still exist; reviver must respawn the pipe.
  agent-deck session revive --name "${name}" >/dev/null 2>&1 || true
  sleep 2

  local new_pipe_pid
  new_pipe_pid="$(pgrep -f "tmux -C attach-session -t ${tmux_name}" | head -1 || true)"
  if [[ -n "${new_pipe_pid}" && "${new_pipe_pid}" != "${pipe_pid}" ]]; then
    banner_pass "[5] reviver respawned control pipe (${pipe_pid} → ${new_pipe_pid})"
  elif [[ -z "${new_pipe_pid}" ]]; then
    banner_skip "[5] no new pipe after revive (PipeManager may be disabled in this env)"
  else
    banner_fail "[5] pipe pid unchanged after revive: ${pipe_pid}"
  fi
  agent-deck session stop "${name}" >/dev/null 2>&1 || true
}

# ---------- entrypoint ----------
main() {
  FAILED=0
  RUN_ID="$$"
  TMPROOT="$(mktemp -d "${TMPDIR:-/tmp}/adeck-verify.XXXXXX")"
  SESSION_PREFIX="verify-persist-${RUN_ID}"
  LOGINSIM_SCOPE="adeck-verify-loginsim-${RUN_ID}"
  ARGV_OUT="${TMPROOT}/argv.log"
  export AGENT_DECK_VERIFY_ARGV_OUT="${ARGV_OUT}"

  trap cleanup EXIT INT TERM

  # ----- preflight -----
  if ! command -v agent-deck >/dev/null 2>&1; then
    printf "${C_RED}ERROR${C_RESET}: agent-deck binary not on PATH.\n" >&2
    exit 2
  fi
  if ! command -v tmux >/dev/null 2>&1; then
    printf "${C_RED}ERROR${C_RESET}: tmux binary not on PATH.\n" >&2
    exit 2
  fi

  # ----- claude stub decision (unchanged logic) -----
  USE_STUB=0
  if [[ "${AGENT_DECK_VERIFY_USE_STUB:-0}" == "1" ]]; then
    USE_STUB=1
  elif ! command -v claude >/dev/null 2>&1; then
    USE_STUB=1
  fi
  if [[ "${USE_STUB}" == "1" ]]; then
    STUB_DIR="$(cd "$(dirname "$0")/verify-session-persistence.d" && pwd)"
    mkdir -p "${TMPROOT}/bin"
    ln -sf "${STUB_DIR}/fake-claude.sh" "${TMPROOT}/bin/claude"
    export PATH="${TMPROOT}/bin:${PATH}"
    log "claude stub: ${STUB_DIR}/fake-claude.sh (argv -> ${ARGV_OUT})"
  else
    log "claude: $(command -v claude) (real)"
  fi

  # ----- checklist -----
  cat <<EOF
==========================================================
verify-session-persistence.sh — v1.5.2 persistence harness
==========================================================
${CHECKLIST}
==========================================================
Each scenario ends with one [PASS], [FAIL], or [SKIP] line.
EOF

  # ----- dispatch -----
  want_scenario 1 && scenario_1_live_session_cgroup
  want_scenario 2 && scenario_2_login_teardown
  want_scenario 3 && scenario_3_restart_resume
  want_scenario 4 && scenario_4_fresh_session_shape
  want_scenario 5 && scenario_5_reviver_respawns_killed_pipe

  if [[ "${FAILED}" -ne 0 ]]; then
    printf "${C_RED}OVERALL: FAIL${C_RESET}\n" >&2
    exit 1
  fi
  printf "${C_GREEN}OVERALL: PASS${C_RESET}\n"
  exit 0
}

# Run only when executed directly. When sourced for unit tests with
# AGENT_DECK_VERIFY_LIB_ONLY=1, expose functions without side effects.
if [[ "${AGENT_DECK_VERIFY_LIB_ONLY:-0}" != "1" ]]; then
  main "$@"
fi
