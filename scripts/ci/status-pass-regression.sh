#!/usr/bin/env bash
# Reproduce #2290's behavioural red/green evidence in identical Docker fixtures.
set -euo pipefail
repo=$(git rev-parse --show-toplevel)
work=${1:?usage: status-pass-regression.sh OUTPUT_DIRECTORY}
mkdir -p "$work"
work=$(cd "$work" && pwd)
base=7d2302fb8a41c5fcab7a441d56729b4b6547885a
reviewed=49d69b468a63529ca69611e3d8fc5ee6a5e3dde5
head=$(git rev-parse HEAD)
image=${STATUS_PASS_GO_IMAGE:-golang:1.25}
modules="$work/modules"
cache="$work/cache"
mkdir -p "$modules" "$cache"
chmod 777 "$modules" "$cache"
for revision in base reviewed; do
  git worktree add --detach "$work/$revision" "${!revision}"
  cp "$repo/internal/session/codex_status_pass_regression_test.go" "$work/$revision/internal/session/"
  cp "$repo/internal/ui/status_pass_sweep_test.go" "$work/$revision/internal/ui/"
done
cp "$repo/scripts/ci/status-pass-baseline-adapter.go.txt" "$work/base/internal/session/status_pass_baseline_adapter_test.go"
cp "$repo/cmd/agent-deck/list_perf_test.go" "$work/base/cmd/agent-deck/"
printf 'base=%s\nreviewed=%s\nhead=%s\n' "$base" "$reviewed" "$head" | tee "$work/revisions.log"
# Only dependency preparation has network access. Actual tests use --network none.
docker pull "$image"
docker image inspect "$image" --format '{{json .RepoDigests}}' | tee "$work/image.log"
docker run --rm --init -u 1000:1000 --cap-drop ALL \
  -v "$repo":/src:ro -w /src -v "$modules":/tmp/gomod \
  -e HOME=/tmp/h -e GOMODCACHE=/tmp/gomod "$image" go mod download
run_tests() {
  local label=$1 source=$2 pattern=$3 expectation=$4
  shift 4
  local result=0
  printf 'go test -p 2 %s -run %s -count=1 -v -timeout=10m\n' "$*" "$pattern" > "$work/$label.log"
  docker run --rm --init -u 1000:1000 --network none --cap-drop ALL \
    -v "$source":/src -w /src -v "$modules":/tmp/gomod -v "$cache":/tmp/h/.cache \
    -e HOME=/tmp/h -e GOMODCACHE=/tmp/gomod -e GOCACHE=/tmp/h/.cache \
    -e 'GOFLAGS=-mod=mod -buildvcs=false' -e PERF_BUDGET_MULTIPLIER=2 -e AGENTDECK_SKIP_UPDATE_CHECK=1 \
    "$image" go test -p 2 "$@" -run "$pattern" -count=1 -v -timeout=10m >> "$work/$label.log" 2>&1 || result=$?
  printf '\nexit_code=%d\n' "$result" >> "$work/$label.log"
  cat "$work/$label.log"
  if [[ "$expectation" == red ]]; then
    [[ $result -ne 0 ]]
    case "$label" in
      base-session) grep -Eq 'ownership sweep scans=.*want 1 and 24' "$work/$label.log" ;;
      base-ui) grep -Eq 'environment reads=.*want 80' "$work/$label.log" ;;
      base-cli) grep -Eq 'show-environment subprocesses=.*want <=500' "$work/$label.log" ;;
      reviewed)
        grep -q 'instance status readers blocked behind peer ownership refresh' "$work/$label.log"
        grep -q 'authoritative hook rotation blocked behind peer ownership refresh' "$work/$label.log" ;;
      *) return 1 ;;
    esac
  else
    [[ $result -eq 0 ]]
  fi
}
run_tests base-session "$work/base" TestStatusPassSweepPinsOwnershipBeyondTTL red ./internal/session
run_tests base-ui "$work/base" TestBackgroundStatusPassOwnershipLinear red ./internal/ui
run_tests base-cli "$work/base" TestListJSON_CodexProbeCount red ./cmd/agent-deck
run_tests reviewed "$work/reviewed" TestStatusPassRefreshDoesNotBlockReadersOrRotation red ./internal/session
run_tests head "$repo" 'TestStatusPass|TestBackgroundStatusPass|TestCodexExclusion|TestListJSON_CodexProbeCount|TestPerf_ColdStart_List100' green ./internal/session ./internal/ui ./cmd/agent-deck
run_tests race "$repo" 'TestStatusPass|TestBackgroundStatusPass|TestCodexExclusion' green -race ./internal/session ./internal/ui
