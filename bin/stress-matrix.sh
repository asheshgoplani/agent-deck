#!/usr/bin/env bash
# Reproduce the mixed-fleet diagnostic slice of the requested stress matrix.
# Usage: stress-matrix.sh BENCH BEFORE_CLI AFTER_CLI BEFORE_UI AFTER_UI OUTDIR
set -euo pipefail

if [ "$#" -ne 6 ]; then
  echo "usage: $0 BENCH BEFORE_CLI AFTER_CLI BEFORE_UI AFTER_UI OUTDIR" >&2
  exit 2
fi
case "$(hostname -s)" in
  g14|buildmac) ;;
  *) echo "run only on g14 or buildmac, inside a sandbox" >&2; exit 2 ;;
esac

bench=$1
before_cli=$2
after_cli=$3
before_ui=$4
after_ui=$5
outdir=$6
case "$outdir" in
  /tmp/*|/private/tmp/*|/Users/ashesh/stress-*) ;;
  *) echo "OUTDIR must be a throwaway G14 or build Mac sandbox" >&2; exit 2 ;;
esac
mkdir -p "$outdir/tmp"
export TMPDIR="$outdir/tmp"
export PATH="$(dirname "$(command -v tmux)"):$PATH"
ulimit -Sn 4096 2>/dev/null || true

failed=0
for size in 50 150 300 600; do
  for phase in before after; do
    if [ "$phase" = before ]; then cli=$before_cli; ui=$before_ui; else cli=$after_cli; ui=$after_ui; fi
    if ! "$bench" -binary "$cli" -ui-harness "$ui" \
      -out "$outdir/$phase-$size.json" -sizes "$size" -runs 3 \
      -machine "$(hostname -s)" -revision "$phase" \
      >"$outdir/$phase-$size.log" 2>&1; then
      failed=1
    fi
  done
done

python3 - "$outdir" <<'PY'
import json
import pathlib
import sys

root = pathlib.Path(sys.argv[1])
names = ("tui_key_repeat_frame_ms", "status_pass_ms", "tmux_calls", "rss_bytes")
lines = [
    "# Mixed fleet stress diagnostic",
    "",
    "This is one reproducible slice of the requested matrix: live local tmux panes, "
    "the bench tool's mixed shell/Claude/Codex state, nested groups, and three fake remotes. "
    "It does not cover the stopped/half-running/hook-firing, flat/20-group/nested, "
    "or no-remote/100-cached-remote Cartesian cases. CPU and daemon metrics are not collected.",
    "",
    "| Sessions | Metric | Before p50 | Before p95 | After p50 | After p95 |",
    "|---:|---|---:|---:|---:|---:|",
]
for size in (50, 150, 300, 600):
    data = {}
    for phase in ("before", "after"):
        path = root / f"{phase}-{size}.json"
        if path.exists():
            report = json.loads(path.read_text())
            data[phase] = {m["name"]: m for m in report["metrics"]}
    for name in names:
        values = []
        for phase in ("before", "after"):
            metric = data.get(phase, {}).get(name)
            values.extend([f'{metric[key]:.3f}' if metric else "unmeasured" for key in ("p50", "p95")])
        lines.append(f"| {size} | {name} | {' | '.join(values)} |")
    for phase in ("before", "after"):
        if phase not in data:
            lines.append(f"\n{size} sessions, {phase}: failed. See `{phase}-{size}.log` for the exact error.")
lines.append("")
(root / "STRESS-MATRIX-20260924.md").write_text("\n".join(lines))
PY

echo "Wrote $outdir/STRESS-MATRIX-20260924.md"
exit "$failed"
