# Lighthouse CI

Performance budget enforcement for the agent-deck web app. Lighthouse CI runs
on every PR that touches `internal/web/**`, `.lighthouserc.json`,
`tests/lighthouse/**`, or the workflow file itself.

## Two-layer gating

1. **Absolute thresholds** (`.lighthouserc.json` `ci.assert.assertions`). Coarse
   upper bound, recalibrated whenever the bundle moves materially. See
   "Threshold Tiers" below and "Recalibrating Thresholds" further down.
2. **Delta gate** (`tests/lighthouse/compare-deltas.mjs`). Blocks any single
   PR that grows `total-byte-weight` or `resource-summary:script:size` by more
   than `MAX_BYTE_WEIGHT_DELTA_PCT` / `MAX_SCRIPT_SIZE_DELTA_PCT` (default 5%
   each, set in `.github/workflows/lighthouse-ci.yml`). The workflow runs
   `lhci collect` twice — once on the PR head, once on the base ref — and
   compares medians. This is the answer to the v1.7.42 audit pattern where the
   absolute budget went stale, every PR was over budget, and the gate was
   disabled rather than recalibrated. Slow growth is fine; a single PR that
   doubles the bundle is not.

   **Maintainer override.** A delta-gate failure is overridable: apply the
   `lighthouse-regression-acknowledged` label to the PR (Labels sidebar →
   click the label). The workflow re-runs on the `labeled` event, the
   override step sees the label, and the check turns green with the override
   logged in the workflow output. Removing the label re-fails the check on
   the next workflow trigger. The absolute lhci assert (layer 1) does NOT
   participate in the override — a hard absolute breach still blocks
   unconditionally.

   The delta gate skips with a logged warning when no base data is available
   (e.g. the base ref predates the workflow). Run-of-the-mill PRs after the
   workflow lands on `main` exercise the full gate.

## Using the delta-gate override

When the delta gate fails, the `Performance Budget` check is red and the
workflow log emits an `::error::` annotation naming the override label:

```
Lighthouse delta gate failed
PR exceeded the bundle-delta threshold. To override, apply the
`lighthouse-regression-acknowledged` label to the PR; the workflow
will re-run on the labeled event.
```

The same step prints the actual numbers above the annotation:

```
total-byte-weight: 286020 B → 317800 B (+31780 B, +11.11%) [limit +5%] FAIL
script:size:        269801 B → 299779 B (+29978 B, +11.11%) [limit +5%] FAIL
```

The full per-run Lighthouse HTML reports for both the PR head and the base
ref are uploaded as the `lighthouse-results-<run_id>` workflow artifact
(14-day retention). Download both `pr/` and `base/` to drill into which
network requests grew before you decide.

### Apply the label when

- A new feature genuinely needs the bundle bytes (a new view, a new
  dependency that's the lightest available option, etc.) and you've already
  confirmed the diff isn't accidentally bundling debug code, source maps, or
  duplicate copies of an existing dependency.
- A library upgrade legitimately grew the bundle and downgrading or
  replacing the library is out of scope for this PR.
- The delta is a known artifact of the change (e.g. enabling a new entry
  point) and you're prepared to recalibrate the absolute thresholds in
  `.lighthouserc.json` in a follow-up via `./tests/lighthouse/calibrate.sh`.

### Don't apply the label when

- The bundle grew by accident: an unused import, a debug-only library that
  wasn't tree-shaken, source maps inadvertently shipped, two copies of the
  same dependency from different package versions. **Fix the PR instead** —
  the gate is doing its job.
- You haven't actually looked at the per-run reports. The label is an
  explicit ack, not a rubber stamp.
- The growth is due to something separable (a planned framework upgrade, a
  new vendored dep). **Land that piece behind its own ack first**, then the
  feature PR rides on the new baseline at a normal delta.

### How the click works

1. PR sidebar → **Labels** → click `lighthouse-regression-acknowledged`.
2. GitHub fires a `labeled` event; `lighthouse-ci.yml` re-runs.
3. The override step sees the label on `github.event.pull_request.labels`,
   exits 0, and emits a `::notice::` annotation naming the override and the
   actor (`github.event.pull_request.user.login` — i.e. the PR author for
   self-applied labels, or the maintainer who applied it).
4. The `Performance Budget` check turns green; the merge gate clears.
5. Removing the label fires `unlabeled` and the check re-fails on the next
   workflow trigger. The label add/remove appears in the PR timeline; that
   _is_ the audit trail. There is no separate review state.

The override applies only to the delta gate. The absolute thresholds in
`.lighthouserc.json` (layer 1 above) still hard-block unconditionally — the
override is for "we expected this growth," not for "ignore the budget
entirely." A PR that breaches both the absolute ceiling and the delta still
fails on the absolute step regardless of the label.

## Threshold Tiers

Two tiers of assertions protect different aspects of performance:

| Metric | Level | Effect | Rationale |
|--------|-------|--------|-----------|
| `total-byte-weight` | `error` | Blocks merge | Deterministic wire-size check. No runner variance. |
| `resource-summary:script:size` | `error` | Blocks merge | Deterministic JS transfer size. No runner variance. |
| `cumulative-layout-shift` | `error` | Blocks merge | Layout stability is deterministic across runs. |
| `first-contentful-paint` | `warn` | Warning only | Timing metric with runner variance. |
| `largest-contentful-paint` | `warn` | Warning only | Timing metric with runner variance. |
| `total-blocking-time` | `warn` | Warning only | Timing metric with runner variance. |
| `speed-index` | `warn` | Warning only | Timing metric with runner variance. |

**Hard gates** (error) block merge. These are byte-count or layout assertions that
produce identical results regardless of CI runner CPU load.

**Soft warnings** (warn) surface regressions without blocking merge. Timing metrics
fluctuate on shared GitHub Actions runners. The thresholds are set at p95 + 20%
buffer from 10 baseline runs on main (or Phase 8 spec + buffer when live calibration
is unavailable).

## How CI Works

1. PR touches `internal/web/**`, `.lighthouserc.json`, `tests/lighthouse/**`,
   or `.github/workflows/lighthouse-ci.yml`.
2. The workflow checks out the PR head and the base ref into separate
   directories and builds both binaries (`GOTOOLCHAIN=go1.24.0 make build`).
3. `lhci collect` runs against the PR-head server (with `--no-tui`).
4. `lhci collect` runs against the base server (best-effort; failures are
   non-fatal so the PR still benefits from the absolute threshold check).
5. `lhci assert` runs against the PR-head reports, enforcing the absolute
   thresholds in `.lighthouserc.json`.
6. `tests/lighthouse/compare-deltas.mjs` reads both result dirs, takes the
   median per metric, and fails if `total-byte-weight` or `script:size` grew
   by more than `MAX_*_DELTA_PCT` (default 5%). Skipped with a warning when no
   base data is present.
7. If the delta gate failed, the override step checks for the
   `lighthouse-regression-acknowledged` label and exits 0 (with a `::notice::`)
   if present, else exits 1 (with an `::error::` explaining how to opt in).
8. Both result dirs (`pr/` and `base/`) are uploaded as the
   `lighthouse-results-<run_id>` GitHub Actions artifact (14-day retention)
   for post-mortem inspection. Lighthouse HTML reports for every individual
   run are inside that artifact.

## Local Verification

Run before pushing to catch budget regressions early:

```bash
make build
./tests/lighthouse/budget-check.sh
```

Prerequisites: Go 1.24.0, Node.js >= 18, Chrome/Chromium installed.

The script starts a test server on port 19999, runs `lhci collect` + `lhci assert`,
and exits with the assertion result code.

## Recalibrating Thresholds

Run after any performance-affecting change (bundle size change, new dependencies,
asset pipeline updates):

```bash
make build
./tests/lighthouse/calibrate.sh
```

The script runs 10 Lighthouse collections, computes p50 and p95 per metric, and
outputs recommended thresholds:

- Hard gates: p95 + 10% buffer (byte-weight, script size)
- Soft warnings: p95 + 20% buffer (FCP, LCP, TBT, Speed Index)
- CLS: fixed at 0.1 per Core Web Vitals spec

Review the output and update `.lighthouserc.json` accordingly. Then verify:

```bash
./tests/lighthouse/budget-check.sh
```

## Troubleshooting

**"lhci: command not found"**: The scripts use `npx @lhci/cli@0.15.1` which
downloads on first run. Ensure Node.js >= 18 and npx are in PATH.

**"Server did not become ready"**: The Go binary must be built first (`make build`).
Check that port 19999 (budget-check) or 19998 (calibrate) is not already in use.
The server cannot start inside an agent-deck session (nested-session detection
prevents it). Run `budget-check.sh` and `calibrate.sh` from a plain terminal.

**"Cannot find Chrome"**: Lighthouse requires Chrome or Chromium. Install via your
package manager. On CI, `ubuntu-latest` includes Chromium.

**Flaky timing warnings**: Timing metrics (FCP, LCP, TBT) are inherently noisy on
shared runners. If warnings appear on unchanged code, the thresholds may need
recalibration. Run `calibrate.sh` on the current main branch.

**Hard gate failure on valid code**: If `total-byte-weight` or `script:size` fails
after a legitimate addition, the budget needs to be increased. Recalibrate and
document why the budget grew in the PR description.

## Design Decisions

**JSON over CJS**: `.lighthouserc.json` is the canonical format for `@lhci/cli`.
JSON is simpler, does not require Node.js module resolution, and is parseable by
any language. The requirements explicitly specify JSON format.

**temporary-public-storage**: No self-hosted LHCI server needed. Results are
uploaded to Google's temporary storage and accessible via a public URL for 7 days.
Appropriate for a public OSS project with no sensitive data in Lighthouse reports.

**5 runs (not 1 or 3)**: Lighthouse official documentation recommends median of 5
runs for stable results. Single-run gates produce approximately 15% variance on
shared runners.

**Desktop preset**: agent-deck is a desktop-first developer tool. Mobile E2E
coverage is handled separately by TEST-D. Lighthouse mobile throttling
(`cpuSlowdownMultiplier: 4`) is too aggressive for CI assertion stability.

**Throttling disabled**: `cpuSlowdownMultiplier: 1` and `throughputKbps: 10240`
(cable speed). We are testing the actual page weight and rendering, not simulated
network conditions. The server is localhost with no real network hop.
