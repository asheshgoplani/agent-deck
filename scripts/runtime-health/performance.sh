#!/usr/bin/env bash
# Run inside Docker only. The caller supplies a full-history checkout and output directory.
set -euo pipefail
if [[ ! -f /.dockerenv ]]; then
  echo 'ERROR: performance acceptance must run inside Docker' >&2
  exit 2
fi
repo=$(cd "${1:?usage: script REPO OUTPUT [HEAD_SHA]}" && pwd)
mkdir -p "${2:?output directory required}"
out=$(cd "$2" && pwd)
case "$out/" in
  "$repo/"*) echo "ERROR: output must be outside the source checkout" >&2; exit 2 ;;
esac
git_repo() { git -c safe.directory="$repo" -C "$repo" "$@"; }
head=$(git_repo rev-parse "${3:-HEAD}^{commit}")
baseline=7d2302fb8a41c5fcab7a441d56729b4b6547885a
dependency=49d69b468a63529ca69611e3d8fc5ee6a5e3dde5
multiplier=${PERF_BUDGET_MULTIPLIER:-2}
work=$(mktemp -d /tmp/health-perf.XXXXXX)
mkdir "$work/red" "$work/green"
git_repo archive "$head" | tar -x -C "$work/green"
git_repo archive "$head" | tar -x -C "$work/red"
# Reverse the dependency's original-main status and ownership algorithm only.
# Keep health sampling and tmux command instrumentation identical on both sides.
git_repo diff "$baseline" "$dependency" -- \
  internal/session/instance.go internal/ui/home.go cmd/agent-deck/main.go \
  internal/web/session_data_service.go > "$out/dependency.patch"
git -C "$work/red" apply --reverse "$out/dependency.patch"
mv "$work/red/internal/session/codex_exclusion_cache.go" "$work/excluded-codex_exclusion_cache.go"
harness=internal/ui/runtime_health_perf_test.go
cmp "$work/red/$harness" "$work/green/$harness"
{
  printf 'head_sha=%s\nbaseline_original_main_sha=%s\ndependency_sha=%s\n' "$head" "$baseline" "$dependency"
  printf 'baseline_recipe=head archive minus dependency.patch minus added codex_exclusion_cache.go\n'
  printf 'retained_overlay=head runtime-health production instrumentation and final harness; unused tmux dependency helpers remain\n'
  printf 'PERF_BUDGET_MULTIPLIER=%s\n' "$multiplier"
  printf 'command=go test -tags runtimehealthperf ./internal/ui -run ^TestPerf_RuntimeHealthCodex$ -count=1 -v -timeout 120s\n'
  sha256sum "$work/red/$harness" "$work/green/$harness" "$out/dependency.patch"
  go version
} > "$out/manifest.txt"
# Exit statuses are captured directly, including compile/setup failure. The
# acceptance conditions below reject those failures as performance evidence.
for side in red green; do
  set +e
  (cd "$work/$side" && PERF_BUDGET_MULTIPLIER="$multiplier" \
    go test -tags runtimehealthperf ./internal/ui -run '^TestPerf_RuntimeHealthCodex$' \
    -count=1 -v -timeout 120s) 2>&1 | tee "$out/$side.txt"
  result=${PIPESTATUS[0]}
  set -e
  printf '%s\n' "$result" > "$out/$side.exit"
  printf '%s_exit=%s\n' "$side" "$result" >> "$out/manifest.txt"
done
[[ $(cat "$out/red.exit") -ne 0 ]]
grep -q '^--- FAIL: TestPerf_RuntimeHealthCodex' "$out/red.txt"
grep -q 'tmux calls=.*outside budget' "$out/red.txt"
grep -q '100 fake sessions: codex=true' "$out/red.txt"
[[ $(cat "$out/green.exit") -eq 0 ]]
grep -q '^--- PASS: TestPerf_RuntimeHealthCodex' "$out/green.txt"
grep -q '100 fake sessions: codex=true' "$out/green.txt"
printf 'acceptance=PASS\n' | tee -a "$out/manifest.txt"
