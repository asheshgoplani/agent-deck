#!/usr/bin/env bash
#
# self-check-container-guard.test.sh: regression test for the container guard in
# .github/skills/agent-deck-contributor/scripts/self-check.sh.
#
# agent-deck tests start real tmux servers and processes, so the contributor
# self-check must never run go test on a host. Outside a container it has to
# report the sandboxed tests and the revert-check as WARN (never PASS), skip
# go test entirely, and print a Docker command that reruns it in a container.
#
# The test builds a throwaway git repo with a tiny Go module whose only test
# writes a probe file, so "go test ran" is observable. Container detection is
# steered through SELF_CHECK_CONTAINER_MARKERS (the script's test hook), never
# by touching real marker files.
#
# The in-container cases run go test on that tiny module, so this test runs
# them only when it is itself inside a container (or AGENTDECK_TEST_CONTAINER=1,
# as in CI on a disposable runner). Elsewhere they are reported as skipped.
#
# Usage: tests/ci/self-check-container-guard.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/.github/skills/agent-deck-contributor/scripts/self-check.sh"

failures=0
pass() { echo "PASS: $1"; }
fail() {
  echo "FAIL: $1" >&2
  failures=$((failures + 1))
}

if ! command -v go >/dev/null 2>&1; then
  echo "SKIP: go not installed; the guarded checks only exist on the Go path"
  exit 0
fi

SANDBOX=$(mktemp -d)
cleanup() { rm -rf "$SANDBOX"; }
trap cleanup EXIT

REPO="$SANDBOX/repo"
PROBE="$SANDBOX/go-test-ran"
mkdir -p "$REPO"
g() { git -C "$REPO" -c user.name=t -c user.email=t@example.invalid -c commit.gpgsign=false "$@"; }

g init -q -b base
printf 'module example.invalid/guard\n\ngo 1.22\n' >"$REPO/go.mod"
printf 'package guard\n\nfunc F() int { return 1 }\n' >"$REPO/f.go"
g add -A && g commit -qm base
g checkout -qb change
printf 'package guard\n\nfunc F() int { return 2 }\n' >"$REPO/f.go"
cat >"$REPO/f_test.go" <<'EOF'
package guard

import (
	"os"
	"testing"
)

func TestF(t *testing.T) {
	if p := os.Getenv("GUARD_PROBE"); p != "" {
		f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.WriteString("ran\n")
			_ = f.Close()
		}
	}
	if F() != 2 {
		t.Fatal("F() != 2")
	}
}
EOF
g add -A && g commit -qm change

# run_check <markers> <container-env> : runs the self-check in the temp repo.
run_check() {
  rm -f "$PROBE"
  OUT=$(cd "$REPO" && env -u AGENTDECK_TEST_CONTAINER \
    ${2:+AGENTDECK_TEST_CONTAINER=$2} \
    SELF_CHECK_CONTAINER_MARKERS="$1" BASE_REF=base GOTOOLCHAIN=local \
    GUARD_PROBE="$PROBE" bash "$SCRIPT" "$SANDBOX/no-body.md" 2>&1)
  RC=$?
}

has() { printf '%s\n' "$OUT" | grep -qE -- "$1"; }

# ---- 1. no container: go test must not run
run_check "$SANDBOX/no-such-marker" ""
[ "$RC" = 0 ] && pass "no container: exit 0 (WARN only)" || fail "no container: exit $RC, want 0"
[ ! -e "$PROBE" ] && pass "no container: go test did not run" || fail "no container: go test ran (probe written)"
has '^WARN  sandboxed-tests +not run: no container detected' && pass "no container: sandboxed-tests is WARN" || fail "no container: sandboxed-tests not reported as WARN"
has '^WARN  revert-check +not run: no container detected' && pass "no container: revert-check is WARN" || fail "no container: revert-check not reported as WARN"
has '^PASS  (sandboxed-tests|revert-check)' && fail "no container: a gated check reported PASS" || pass "no container: no gated check reported PASS"
has '^PASS  go-build' && pass "no container: go build still runs" || fail "no container: go build did not run"
has '^Not ready yet: go test has not run' && pass "no container: not reported ready" || fail "no container: summary still says ready"
has '^  docker run --rm' && pass "no container: Docker command printed" || fail "no container: Docker command missing"
for want in 'dst=/src,readonly' '-e AGENTDECK_TEST_CONTAINER=1' '-e HOME=/tmp/home' '-e BASE_REF=base' 'golang:1\.22' 'cp -R /src /tmp/src' 'scripts/self-check\.sh'; do
  has "$want" && pass "Docker command has $want" || fail "Docker command lacks $want"
done

# ---- 2. inside a container: go test runs as before
if [ "${AGENTDECK_TEST_CONTAINER:-0}" = 1 ] || [ -e /.dockerenv ] || [ -e /run/.containerenv ]; then
  touch "$SANDBOX/marker"
  for mode in marker env; do
    if [ "$mode" = marker ]; then run_check "$SANDBOX/marker" ""; else run_check "$SANDBOX/no-such-marker" 1; fi
    [ -s "$PROBE" ] && pass "$mode: go test ran" || fail "$mode: go test did not run"
    has '^PASS  sandboxed-tests' && pass "$mode: sandboxed-tests PASS" || fail "$mode: sandboxed-tests not PASS"
    has '^PASS  revert-check' && pass "$mode: revert-check PASS" || fail "$mode: revert-check not PASS"
    has '^Ready to open' && pass "$mode: reported ready" || fail "$mode: not reported ready"
    has 'docker run' && fail "$mode: Docker command printed inside a container" || pass "$mode: no Docker command"
  done
else
  echo "SKIP: in-container cases (not in a container; they run go test)"
fi

if [ "$failures" -gt 0 ]; then
  echo "$failures failure(s)" >&2
  printf '%s\n' "--- last self-check output ---" "$OUT" >&2
  exit 1
fi
echo "all checks passed"
