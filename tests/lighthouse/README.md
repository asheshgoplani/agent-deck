# Lighthouse CI

Performance budget enforcement for the agent-deck web app. Lighthouse CI runs
on every PR that touches `internal/web/**` or `.lighthouserc.json`.

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

1. PR touches `internal/web/**` or `.lighthouserc.json`
2. GitHub Actions workflow `.github/workflows/lighthouse-ci.yml` triggers
3. Workflow builds the Go binary with `GOTOOLCHAIN=go1.24.0`
4. `treosh/lighthouse-ci-action@v12` reads `.lighthouserc.json`
5. LHCI starts the test server via `startServerCommand`
6. LHCI runs 5 Lighthouse collections (median used for assertions)
7. LHCI asserts thresholds: `error` failures cause non-zero exit (check fails)
8. Results uploaded to `temporary-public-storage` (public link in check output)
9. HTML report artifacts attached to the workflow run

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
