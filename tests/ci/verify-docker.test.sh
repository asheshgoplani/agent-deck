#!/usr/bin/env bash
# shellcheck disable=SC2015,SC2016 # pass/fail chains and literal grep patterns
#
# verify-docker.test.sh: tests for scripts/verify-docker.sh without Docker.
#
# The script is pointed at a throwaway repo tree (VERIFY_DOCKER_ROOT) whose
# go.mod, Makefile and go-test.yml carry the pins, and at a fake docker binary
# (DOCKER) that records every invocation and fails on demand. So this checks
# pin resolution, stage selection, the STAGE/VERIFY_RC report and the shape
# of each docker run (--rm, no named volumes, read-only source, non-root
# tests) while starting no container at all.
#
# Usage: tests/ci/verify-docker.test.sh
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/scripts/verify-docker.sh"

failures=0
pass() { echo "PASS: $1"; }
fail() {
  echo "FAIL: $1" >&2
  failures=$((failures + 1))
}

SANDBOX=$(mktemp -d)
cleanup() { if command -v trash >/dev/null 2>&1; then trash "$SANDBOX"; else rm -rf "$SANDBOX"; fi; }
trap cleanup EXIT

TREE="$SANDBOX/tree"
mkdir -p "$TREE/.github/workflows"
write_pins() { # $1: optional toolchain line for go.mod
  printf 'module example.invalid/x\n\ngo 1.25.13\n%s\n' "${1:-}" >"$TREE/go.mod"
  printf 'GOLANGCI_LINT_VERSION=v2.14.0\nlint:\n\techo lint\n' >"$TREE/Makefile"
  printf '      - run: |\n          go install gotest.tools/gotestsum@v1.13.0\n' >"$TREE/.github/workflows/go-test.yml"
}

LOG="$SANDBOX/docker.log"
FAKE="$SANDBOX/docker"
cat >"$FAKE" <<'EOF'
#!/usr/bin/env bash
# One line per invocation (newlines folded); exit 1 when the run carries VERIFY_STAGE=$FAKE_FAIL.
{ printf '%s' "$*" | tr '\n' ' '; echo; } >>"$FAKE_DOCKER_LOG"
case " $* " in *" VERIFY_STAGE=${FAKE_FAIL:-none} "*) exit 1;; esac
exit 0
EOF
chmod +x "$FAKE"

OUT="" RC=0
run() {
  : >"$LOG"
  OUT=$(VERIFY_DOCKER_ROOT="$TREE" DOCKER="${DOCKER_BIN:-$FAKE}" FAKE_DOCKER_LOG="$LOG" FAKE_FAIL="${FAKE_FAIL:-}" bash "$SCRIPT" "$@" 2>&1)
  RC=$?
}
has() { printf '%s\n' "$OUT" | grep -qE -- "$1"; }
logged() { grep -qE -- "$1" "$LOG"; }

write_pins

# ---- 1. docker missing: clear refusal, nothing runs
DOCKER_BIN="$SANDBOX/no-such-docker" run
[ "$RC" = 2 ] && pass "no docker: exit 2" || fail "no docker: exit $RC, want 2"
has 'docker not found' && pass "no docker: clear message" || fail "no docker: message missing: $OUT"
has '^STAGE ' && fail "no docker: a stage ran" || pass "no docker: no stage ran"

# ---- 2. default: every stage, all green
run
[ "$RC" = 0 ] && pass "all stages: exit 0" || fail "all stages: exit $RC, want 0"
for st in gofmt build vet lint test; do
  has "^STAGE $st OK$" && pass "all stages: STAGE $st OK" || fail "all stages: STAGE $st OK missing"
done
has '^VERIFY_RC=0$' && pass "all stages: VERIFY_RC=0" || fail "all stages: VERIFY_RC=0 missing"
[ "$(wc -l <"$LOG" | tr -d ' ')" = 5 ] && pass "all stages: five docker runs" || fail "all stages: $(wc -l <"$LOG") docker runs, want 5"

# Pins come from the repo, not the script.
logged 'golang:1\.25\.13-bookworm' && pass "go image from go.mod go line" || fail "go image not golang:1.25.13-bookworm"
logged 'golangci/golangci-lint:v2\.14\.0' && pass "lint image from Makefile" || fail "lint image not golangci/golangci-lint:v2.14.0"
logged 'gotest\.tools/gotestsum@v1\.13\.0' && pass "gotestsum from go-test.yml" || fail "gotestsum@v1.13.0 not installed"

# Every run is throwaway, read-only on the tree, and uses no named volume.
[ "$(grep -c '^run --rm ' "$LOG")" = 5 ] && pass "every run uses --rm" || fail "a run lacks --rm"
grep -qE -- "-v $TREE:/src:ro " "$LOG" && pass "tree mounted read-only" || fail "tree not mounted read-only"
grep -oE -- '-v [^ ]+' "$LOG" | grep -vE -- '-v /' >/dev/null && fail "a named volume is mounted" || pass "no named volumes"
[ "$(grep -c 'GOTOOLCHAIN=local' "$LOG")" -ge 4 ] && pass "Go stages pin GOTOOLCHAIN=local" || fail "GOTOOLCHAIN=local missing"
grep 'VERIFY_STAGE=test ' "$LOG" | grep -q 'setpriv --reuid=' && pass "tests run as a non-root uid" || fail "tests not dropped to a non-root uid"
grep 'VERIFY_STAGE=test ' "$LOG" | grep -q -- 'VERIFY_PKGS=\./\.\.\. ' && pass "default packages ./..." || fail "default packages not ./..."
grep 'VERIFY_STAGE=test ' "$LOG" | grep -q -- '--packages="$VERIFY_PKGS"' && pass "gotestsum gets the packages" || fail "gotestsum does not get the packages"
grep 'VERIFY_STAGE=test ' "$LOG" | grep -q -- '-race -timeout 20m' && pass "test flags mirror go-test.yml" || fail "test flags differ from go-test.yml"
grep 'VERIFY_STAGE=lint ' "$LOG" | grep -q 'golangci-lint run' && pass "lint runs golangci-lint run" || fail "lint does not run golangci-lint run"

# ---- 3. toolchain line wins over the go line
write_pins 'toolchain go1.26.9'
run --stages build
logged 'golang:1\.26\.9-bookworm' && pass "go image from toolchain line" || fail "toolchain line ignored: $(cat "$LOG")"
write_pins

# ---- 4. stage selection and package patterns
run --stages build
[ "$(wc -l <"$LOG" | tr -d ' ')" = 1 ] && pass "--stages build: one docker run" || fail "--stages build: $(wc -l <"$LOG") runs"
has '^STAGE build OK$' && pass "--stages build: STAGE build OK" || fail "--stages build: STAGE build OK missing"
has '^STAGE (gofmt|vet|lint|test) ' && fail "--stages build: another stage ran" || pass "--stages build: only build ran"

run --stages test --pkgs './internal/testutil/... ./internal/git/...'
grep -q -- 'VERIFY_PKGS=\./internal/testutil/\.\.\. \./internal/git/\.\.\. ' "$LOG" && pass "--pkgs reaches gotestsum" || fail "--pkgs not passed: $(cat "$LOG")"

# ---- 5. one failing stage: reported, others still run, nonzero exit
FAKE_FAIL=lint run
[ "$RC" = 1 ] && pass "lint fails: exit 1" || fail "lint fails: exit $RC, want 1"
has '^STAGE lint FAIL$' && pass "lint fails: STAGE lint FAIL" || fail "lint fails: STAGE lint FAIL missing"
has '^STAGE test OK$' && pass "lint fails: later stages still run" || fail "lint fails: test stage did not run"
has '^VERIFY_RC=1$' && pass "lint fails: VERIFY_RC=1" || fail "lint fails: VERIFY_RC=1 missing"

# ---- 6. bad input is refused before any docker run
run --stages build,bogus
[ "$RC" = 2 ] && [ ! -s "$LOG" ] && pass "unknown stage: exit 2, nothing ran" || fail "unknown stage: exit $RC"
printf 'lint:\n' >"$TREE/Makefile"
run
[ "$RC" = 2 ] && has 'GOLANGCI_LINT_VERSION' && [ ! -s "$LOG" ] && pass "missing pin: exit 2 naming it" || fail "missing pin: exit $RC: $OUT"

if [ "$failures" -gt 0 ]; then
  echo "$failures failure(s)" >&2
  printf '%s\n' "--- last output ---" "$OUT" >&2
  exit 1
fi
echo "all checks passed"
