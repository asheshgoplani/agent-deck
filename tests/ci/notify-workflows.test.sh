#!/usr/bin/env bash
# Guards pr-notify.yml and issue-notify.yml at the repo's pull_request_target
# standard (same as pr-intake.yml and ci-stalled-notify.yml): every action is
# pinned to a full commit SHA, the token is read-only, the job has a timeout,
# and a title carrying CR or LF can never split the ntfy Title header.
#
# The header check runs the real "Send to ntfy" step script (extracted with
# Ruby's YAML parser) against a fake curl, so it tests behavior, not text.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
WF_DIR="$ROOT/.github/workflows"
ERRORS=0

fail() {
  echo "FAIL: $1" >&2
  ERRORS=$((ERRORS + 1))
}

pass() {
  echo "PASS: $1"
}

TMP="$(mktemp -d)"
trap 'trash "$TMP" 2>/dev/null || command rm -rf "$TMP"' EXIT

# Fake curl: records each argument on its own NUL-separated field so an
# embedded newline inside one header value stays visible to the checks.
mkdir -p "$TMP/bin"
cat >"$TMP/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '%s\0' "$@" >"$FAKE_CURL_LOG"
EOF
chmod +x "$TMP/bin/curl"

# check_workflow <file> <expected permissions as key=value,...> <context env var>
check_workflow() {
  local name="$1" perms="$2" ctx_var="$3"
  local wf="$WF_DIR/$name"

  if [[ ! -f "$wf" ]]; then
    fail "$name: workflow file not found"
    return
  fi

  # (1) Every uses: is pinned to a 40-char commit SHA.
  local unpinned
  unpinned=$(grep -nE '^\s*(- )?uses:' "$wf" | grep -vE 'uses:\s*[^@[:space:]]+@[0-9a-f]{40}(\s|$)' || true)
  if [[ -z "$unpinned" ]]; then
    pass "$name: every uses: is pinned to a full commit SHA"
  else
    fail "$name: unpinned action reference(s): $unpinned"
  fi

  # (2) Top-level permissions match exactly (read-only token).
  local got
  got=$(ruby -ryaml -e 'p = YAML.load_file(ARGV[0])["permissions"]; puts(p.is_a?(Hash) ? p.sort.map { |k, v| "#{k}=#{v}" }.join(",") : p.inspect)' "$wf")
  if [[ "$got" == "$perms" ]]; then
    pass "$name: top-level permissions are $perms"
  else
    fail "$name: top-level permissions must be $perms, got $got"
  fi

  # (3) The notify job has a timeout.
  local timeout
  timeout=$(ruby -ryaml -e 'puts YAML.load_file(ARGV[0])["jobs"]["notify"]["timeout-minutes"].to_s' "$wf")
  if [[ -n "$timeout" ]]; then
    pass "$name: notify job has timeout-minutes: $timeout"
  else
    fail "$name: notify job must set timeout-minutes"
  fi

  # (4) A title with CR/LF stays inside one Title header.
  local script="$TMP/$name.sh"
  ruby -ryaml -e 'step = YAML.load_file(ARGV[0])["jobs"]["notify"]["steps"].find { |s| s["name"] == "Send to ntfy" }; print step["run"]' "$wf" >"$script"
  local ctx
  ctx=$(jq -cn '{number: 7, title: "evil\r\nPriority: min\nX-Injected: 1", detected_type: "bug"}' | base64 | tr -d '\n')
  local log="$TMP/$name.curl"
  : >"$log"
  env PATH="$TMP/bin:$PATH" FAKE_CURL_LOG="$log" NTFY_TOPIC=topic "$ctx_var=\"$ctx\"" bash -e "$script"

  local title_args
  title_args=$(tr '\0' '\n' <"$log" | grep -c '^Title: ' || true)
  local injected
  injected=$(tr '\0' '\n' <"$log" | grep -cE '^(Priority: min|X-Injected: 1)$' || true)
  if [[ "$title_args" == "1" && "$injected" == "0" ]] && ! tr -d '\0' <"$log" | grep -q $'\r'; then
    pass "$name: CR/LF in the title cannot split the ntfy Title header"
  else
    fail "$name: title CR/LF leaks into curl headers ($(tr '\0' '|' <"$log" | cat -v))"
  fi
}

check_workflow pr-notify.yml "contents=read,pull-requests=read" PR_CONTEXT_B64
check_workflow issue-notify.yml "contents=read,issues=read" ISSUE_CONTEXT_B64

if [[ "$ERRORS" -ne 0 ]]; then
  exit 1
fi
