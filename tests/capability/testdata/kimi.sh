#!/usr/bin/env bash
set -eu

record_root="$HOME/.kimi-test-launches"
record_tmp="$record_root/.tmp-$(date +%s%N)-$$"
record_dir="$record_root/$(date +%s%N)-$$"
mkdir -p "$record_tmp"
: >"$record_tmp/args"
for arg in "$@"; do
  printf '%s\n' "$arg" >>"$record_tmp/args"
done
{
  printf 'HOME=%s\n' "$HOME"
  printf 'XDG_CONFIG_HOME=%s\n' "${XDG_CONFIG_HOME-}"
  printf 'XDG_STATE_HOME=%s\n' "${XDG_STATE_HOME-}"
  printf 'KIMI_CODE_HOME=%s\n' "${KIMI_CODE_HOME-}"
  printf 'AGENTDECK_INSTANCE_ID=%s\n' "${AGENTDECK_INSTANCE_ID-}"
  printf 'AGENTDECK_TITLE=%s\n' "${AGENTDECK_TITLE-}"
  printf 'AGENTDECK_TOOL=%s\n' "${AGENTDECK_TOOL-}"
  printf 'AGENTDECK_PROFILE=%s\n' "${AGENTDECK_PROFILE-}"
  printf 'AGENTDECK_HOOK_GENERATION=%s\n' "${AGENTDECK_HOOK_GENERATION-}"
} >"$record_tmp/environment"
mv "$record_tmp" "$record_dir"

emit_hook() {
  printf '{"hook_event_name":"%s","session_id":"native-kimi-original","client_type":"kimi_code_cli"}\n' "$1" |
    agent-deck hook-handler
}

emit_hook SessionStart
printf 'KIMI READY\n'
printf ' │ > \n'

while IFS= read -r prompt; do
  printf 'PROMPT:%s\n' "$prompt"
  emit_hook UserPromptSubmit
  emit_hook PostToolUse
  printf 'KIMI BUSY\n'

  for ((attempt = 0; attempt < 200; attempt++)); do
    if [[ -f "$HOME/.kimi-test-finish-turn" ]]; then
      break
    fi
    sleep 0.05
  done
  if [[ ! -f "$HOME/.kimi-test-finish-turn" ]]; then
    printf 'KIMI TEST ERROR: turn was not released\n' >&2
    exit 1
  fi

  printf 'ECHO:%s\n' "$prompt"
  emit_hook Stop
  printf 'KIMI READY\n'
  printf ' │ > \n'
done
