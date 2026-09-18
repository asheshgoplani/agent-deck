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
multiplier=${PERF_BUDGET_MULTIPLIER:-2}
work=$(mktemp -d /tmp/health-perf.XXXXXX)
mkdir "$work/red" "$work/green"
git_repo archive "$head" | tar -x -C "$work/green"
git_repo archive "$head" | tar -x -C "$work/red"
# Reverse the original-main status and ownership algorithm only, anchored by
# content rather than by a historical diff so the recipe survives unrelated
# edits around it. Health sampling and tmux command instrumentation stay
# identical on both sides. Two pieces make the red side:
#  1. codex_exclusion_cache.go is replaced by the uncached overlay, so every
#     ownership lookup enumerates all peers with fresh tmux subprocesses.
#  2. The status sweep collects peer exclusions eagerly for every active
#     Codex session (the pre-dependency call), instead of only on a bootstrap
#     disk scan. Both anchors must match exactly once; a miss is a recipe
#     error, never silently green.
overlay=scripts/runtime-health/red/codex_exclusion_cache.go.txt
cp "$work/green/$overlay" "$work/red/internal/session/codex_exclusion_cache.go"
status_anchor='i.updateCodexSessionForPass(nil, false, pass, true)'
status_red='i.updateCodexSession(i.collectOtherCodexSessionIDs(), false)'
instance=internal/session/instance.go
test "$(grep -cF -- "$status_anchor" "$work/red/$instance")" -eq 1
sed -i "s|$status_anchor|$status_red|" "$work/red/$instance"
test "$(grep -cF -- "$status_red" "$work/red/$instance")" -eq 1
git -C "$work" diff --no-index --stat "green/$instance" "red/$instance" > "$out/red-recipe.txt" || true
harness=internal/ui/runtime_health_perf_test.go
cmp "$work/red/$harness" "$work/green/$harness"
{
  printf 'head_sha=%s\n' "$head"
  printf 'red_recipe=head archive with %s in place of codex_exclusion_cache.go and the status sweep calling %s\n' "$overlay" "$status_red"
  printf 'retained_overlay=head runtime-health production instrumentation and final harness\n'
  printf 'PERF_BUDGET_MULTIPLIER=%s\n' "$multiplier"
  printf 'command=go test -tags runtimehealthperf ./internal/ui -run ^TestPerf_RuntimeHealthCodex$ -count=1 -v -timeout 120s\n'
  sha256sum "$work/red/$harness" "$work/green/$harness" "$work/green/$overlay"
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
