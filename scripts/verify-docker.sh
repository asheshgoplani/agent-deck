#!/usr/bin/env bash
#
# verify-docker.sh: reproduce the CI Go checks locally, inside Docker.
#
# Stages (default: all, in this order):
#   gofmt  gofmt -l over the tree must be empty
#   build  go build ./...
#   vet    go vet ./...
#   lint   golangci-lint run --timeout=5m, official golangci/golangci-lint image,
#          the repo's .golangci.yml (same as the golangci-lint workflow)
#   test   gotestsum --rerun-fails=2 ... -- -race -timeout 20m, as a non-root
#          uid with tmux and zoxide installed (same as go-test.yml)
#
# Versions are read from the repo, never pinned here:
#   Go         go.mod toolchain line, else the go line (image golang:<ver>-bookworm,
#              GOTOOLCHAIN=local so the image's Go is the one that runs)
#   golangci   Makefile GOLANGCI_LINT_VERSION
#   gotestsum  .github/workflows/go-test.yml
#
# Every stage is a throwaway `docker run --rm`: the tree is mounted read-only
# and copied inside the container, HOME and XDG dirs are fresh temp dirs, and
# no named volume is used, so the host's agent-deck state is never touched.
#
# Usage: scripts/verify-docker.sh [--stages gofmt,build,vet,lint,test] [--pkgs '<go package patterns>']
# Output: one 'STAGE <name> OK|FAIL' line per stage, then 'VERIFY_RC=<n>'.
# Exit: 0 when every stage passed, 1 when any failed, 2 on bad input or no docker.
#
# Test hooks: VERIFY_DOCKER_ROOT (tree to verify), DOCKER (docker binary).
set -uo pipefail

ALL_STAGES="gofmt,build,vet,lint,test"
STAGES=$ALL_STAGES
PKGS="./..."
ROOT=${VERIFY_DOCKER_ROOT:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}
DOCKER=${DOCKER:-docker}

die() {
  echo "verify-docker: $*" >&2
  exit 2
}
usage() { sed -n '3,28p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case $1 in
    --stages) [ $# -ge 2 ] || die "--stages needs a value"; STAGES=$2; shift 2 ;;
    --pkgs) [ $# -ge 2 ] || die "--pkgs needs a value"; PKGS=$2; shift 2 ;;
    -h | --help) usage; exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

IFS=, read -r -a SELECTED <<<"$STAGES"
[ ${#SELECTED[@]} -gt 0 ] || die "--stages is empty"
for st in "${SELECTED[@]}"; do
  case ",$ALL_STAGES," in *",$st,"*) ;; *) die "unknown stage '$st' (stages: $ALL_STAGES)" ;; esac
done

# Pins, all from the repo.
GOVER=$(awk '$1 == "toolchain" { sub(/^go/, "", $2); print $2; exit }' "$ROOT/go.mod" 2>/dev/null)
[ -n "$GOVER" ] || GOVER=$(awk '$1 == "go" { print $2; exit }' "$ROOT/go.mod" 2>/dev/null)
LINTVER=$(sed -nE 's/^GOLANGCI_LINT_VERSION[[:space:]]*:?=[[:space:]]*([^[:space:]]+).*/\1/p' "$ROOT/Makefile" 2>/dev/null | head -1)
GOTESTSUM=$(grep -oE 'gotest\.tools/gotestsum@v[0-9][0-9.]*' "$ROOT/.github/workflows/go-test.yml" 2>/dev/null | head -1)
[ -n "$GOVER" ] || die "cannot read the Go version from $ROOT/go.mod (toolchain or go line)"
[ -n "$LINTVER" ] || die "cannot read GOLANGCI_LINT_VERSION from $ROOT/Makefile"
[ -n "$GOTESTSUM" ] || die "cannot read the gotestsum version from $ROOT/.github/workflows/go-test.yml"

command -v "$DOCKER" >/dev/null 2>&1 || die "docker not found ('$DOCKER'); install Docker to reproduce CI, or run 'make lint' and 'make test' on the host"

GO_IMAGE="golang:${GOVER}-bookworm"
LINT_IMAGE="golangci/golangci-lint:${LINTVER}"
echo "verify-docker: root=$ROOT go=$GOVER golangci=$LINTVER ${GOTESTSUM#gotest.tools/} stages=$STAGES"

# Copy the read-only tree to /work, skipping VCS data and nested worktrees.
COPY='mkdir -p /work && tar -C /src --exclude=./.git --exclude=./.worktrees --exclude=./.claude/worktrees -cf - . | tar -C /work -xf -'
# shellcheck disable=SC2016 # expanded inside the container, not here
FRESH_DIRS='export HOME=$(mktemp -d) XDG_CONFIG_HOME=$(mktemp -d) XDG_DATA_HOME=$(mktemp -d) XDG_STATE_HOME=$(mktemp -d) XDG_CACHE_HOME=$(mktemp -d)'
# shellcheck disable=SC2016 # expanded inside the container, not here
SCRIPT_GOFMT='out=$(gofmt -l .); if [ -n "$out" ]; then echo "$out"; echo "gofmt: run go fmt ./... to fix the files above"; exit 1; fi'
SCRIPT_TEST="apt-get update -qq >/dev/null && apt-get install -y -qq tmux zoxide >/dev/null
useradd --create-home --uid 10001 verify
$COPY && chown -R verify:verify /work
exec setpriv --reuid=10001 --regid=10001 --clear-groups env HOME=/home/verify bash -c '
set -euo pipefail
$FRESH_DIRS
cd /work
go install gotest.tools/${GOTESTSUM#gotest.tools/}
\"\$(go env GOPATH)/bin/gotestsum\" --rerun-fails=2 --rerun-fails-abort-on-data-race --packages=\"\$VERIFY_PKGS\" -- -race -timeout 20m'"

rc=0
stage() { # stage <name> <image> <script> [extra docker args...]
  local name=$1 image=$2 script=$3
  shift 3
  echo "== $name ($image)"
  if "$DOCKER" run --rm -v "$ROOT":/src:ro \
    -e VERIFY_STAGE="$name" -e GOFLAGS=-buildvcs=false -e AGENTDECK_SKIP_UPDATE_CHECK=1 \
    "$@" "$image" bash -c "set -euo pipefail; $script"; then
    echo "STAGE $name OK"
  else
    echo "STAGE $name FAIL"
    rc=1
  fi
}

go_stage() { # go_stage <name> <commands>
  stage "$1" "$GO_IMAGE" "$FRESH_DIRS; $COPY; cd /work; go version; $2" -e GOTOOLCHAIN=local
}

for st in "${SELECTED[@]}"; do
  case $st in
    gofmt) go_stage gofmt "$SCRIPT_GOFMT" ;;
    build) go_stage build 'go build ./...' ;;
    vet) go_stage vet 'go vet ./...' ;;
    lint) stage lint "$LINT_IMAGE" "$FRESH_DIRS; $COPY; cd /work; go version; golangci-lint version; golangci-lint run --timeout=5m" \
      -e "GOTOOLCHAIN=go$GOVER" ;;
    # --init: without a reaping PID 1 (gotestsum ends up as PID 1 after the
    # exec chain) killed tmux servers stay zombies and the testutil cleanup
    # tests see them as alive. CI runners reap through systemd.
    test) stage test "$GO_IMAGE" "$SCRIPT_TEST" --init \
      -e GOTOOLCHAIN=local -e PERF_BUDGET_MULTIPLIER=2.0 -e "VERIFY_PKGS=$PKGS" ;;
  esac
done

echo "VERIFY_RC=$rc"
exit "$rc"
