#!/usr/bin/env bash
# Run the BenchmarkOp_* operation benchmarks on a git ref and record them.
#
#   bench/ops/run.sh <label> <ref-or-sha> [--count N] [--benchtime D] [--publish DIR]
#
# <ref> "." benchmarks this checkout as it is (uncommitted files included).
# Any other ref gets a detached worktree under $BENCH_WT_DIR/<label>
# (created, or reset to the ref when it exists), with the files listed in
# bench/ops/files.txt copied in from this checkout, so old releases run the
# same benchmarks. Tests run in deck-test when it exists, else plain go test.
#
# Writes bench/ops/results/<label>.txt (raw) and <label>.json, then rewrites
# the generated blocks of docs/performance.md. --publish DIR also copies
# results/*.json and catalogue.json to DIR.
#
# Env: BENCH_WT_DIR (worktree root), BENCH_RESULTS_DIR (default
# bench/ops/results), BENCH_DOC (default docs/performance.md), BENCH_HOST
# (host text in the results), BENCH_TIMEOUT (go test -timeout, default 30m).
set -euo pipefail

usage() { sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'; exit 2; }
[ $# -ge 2 ] || usage
label=$1 ref=$2; shift 2
count=5 benchtime=1s publish=""
while [ $# -gt 0 ]; do
  case $1 in
    --count) count=$2; shift 2 ;;
    --benchtime) benchtime=$2; shift 2 ;;
    --publish) publish=$2; shift 2 ;;
    *) usage ;;
  esac
done
[[ $label =~ ^[A-Za-z0-9._-]+$ && $label != . && $label != .. ]] || { echo "run.sh: label must be [A-Za-z0-9._-]+ (not . or ..)" >&2; exit 2; }

root=$(cd "$(dirname "$0")/../.." && pwd -P)
wtroot=${BENCH_WT_DIR:-${XDG_CACHE_HOME:-$HOME/.cache}/agent-deck-bench/wt}
results=${BENCH_RESULTS_DIR:-$root/bench/ops/results}
doc=${BENCH_DOC:-$root/docs/performance.md}
export PATH=/usr/local/go/bin:$PATH

if [ "$ref" = "." ]; then
  dir=$root
  sha=$(git -C "$root" rev-parse HEAD)
  refname=$(git -C "$root" rev-parse --abbrev-ref HEAD)
  [ -n "$(git -C "$root" status --porcelain)" ] && refname="$refname (uncommitted)"
else
  sha=$(git -C "$root" rev-parse --verify "$ref^{commit}")
  refname=$ref
  dir=$wtroot/$label
  if [ -e "$dir/.git" ]; then
    # Only ever reset a worktree of this repo that run.sh can own: never a
    # separate checkout that happens to sit at the same path.
    if ! git -C "$root" worktree list --porcelain | grep -qxF "worktree $(cd "$dir" && pwd -P)"; then
      echo "run.sh: $dir is not a worktree of $root; refusing to reset it" >&2
      exit 1
    fi
    git -C "$dir" checkout --quiet --force --detach "$sha"
  else
    mkdir -p "$wtroot"
    git -C "$root" worktree add --quiet --detach "$dir" "$sha"
  fi
fi
commit_date=$(git -C "$root" show -s --format=%cI "$sha")

pkgs=()
while read -r line; do
  case $line in ''|'#'*) continue ;; esac
  if [ "${line%% *}" = pkg ]; then
    p=${line#pkg }
    [ -d "$dir/$p" ] && pkgs+=("./$p/")
    continue
  fi
  [ -f "$root/$line" ] || { echo "run.sh: $line listed in files.txt is missing" >&2; exit 1; }
  [ "$dir" = "$root" ] || cp "$root/$line" "$dir/$line"
  p=./$(dirname "$line")/
  [[ " ${pkgs[*]-} " == *" $p "* ]] || pkgs+=("$p")
done < "$root/bench/ops/files.txt"

mkdir -p "$results"
raw=$results/$label.txt
args=(-p 1 -tags opbench -run '^$' -bench '^BenchmarkOp_' -benchmem -benchtime="$benchtime" -count="$count" -timeout "${BENCH_TIMEOUT:-30m}" "${pkgs[@]}")
echo "run.sh: $label = $refname @ ${sha:0:8} in $dir: ${pkgs[*]}" >&2
set +e
if command -v deck-test >/dev/null 2>&1; then
  host=${BENCH_HOST:-"$(hostname -s) deck-test container (6 CPU, shared host)"}
  deck-test -C "$dir" "${args[@]}" 2>&1 | tee "$raw"
else
  host=${BENCH_HOST:-"$(hostname -s) ($(nproc 2>/dev/null || sysctl -n hw.ncpu) CPU, go test)"}
  (cd "$dir" && go test "${args[@]}") 2>&1 | tee "$raw"
fi
rc=${PIPESTATUS[0]}
set -e

cd "$root"
go run ./tools/benchops parse -in "$raw" -out "$results/$label.json" -catalogue bench/ops/catalogue.json \
  -label "$label" -ref "$refname" -sha "$sha" -commit-date "$commit_date" -host "$host"
go run ./tools/benchops render -results "$results" -catalogue bench/ops/catalogue.json -doc "$doc"
echo "run.sh: wrote $results/$label.json and updated $doc" >&2

if [ -n "$publish" ]; then
  mkdir -p "$publish"
  cp "$results"/*.json bench/ops/catalogue.json "$publish"/
  echo "run.sh: published to $publish" >&2
fi
[ "$rc" = 0 ] || echo "run.sh: go test exited $rc (results kept)" >&2
exit "$rc"
