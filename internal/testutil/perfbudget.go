// Package testutil contains shared helpers for agent-deck's test suites.
//
// Performance regression API (this file)
// ======================================
//
// All TestPerf_* tests use this small set of primitives. The design has
// three deliberate parts:
//
//  1. Cold vs warm budget classification.
//  2. n=11 trimmed-mean measurement.
//  3. 1 ms budget floor.
//
// ── Cold vs warm ────────────────────────────────────────────────────────
//
// The budget formula differs based on whether the test crosses a process
// or syscall boundary that introduces external variance the test code
// can't control:
//
//   - COLD tests use ColdBudget(t, base) = max(base × 5, 1 ms) × multiplier.
//     For: cold-start exec, real-disk fsync (Tier 2 — see CLAUDE.md
//     "Performance regression: mandatory test coverage"), child-process
//     spawn, network round-trips. The 5× factor reflects that runner /
//     loader / fsync variance can't be capped tighter without false
//     positives.
//
//   - WARM tests use WarmBudget(t, base) = max(base × 3, 1 ms) × multiplier.
//     For: pure in-process Go work measurable under controlled GC. The
//     tighter 3× factor is safe because TrimmedMeanWarm forces a GC
//     cycle and disables auto-GC during each timed window — the largest
//     noise source is eliminated. WARM tests run on tmpfs / in memory
//     (Tier 1) so disk speed is irrelevant.
//
// Both formulas are scaled by PERF_BUDGET_MULTIPLIER (default 1.0; CI
// sets 2.0 to absorb shared-runner variance). The effective CI gate is
// 10× local for cold, 6× local for warm.
//
// ── n=11 trimmed mean ───────────────────────────────────────────────────
//
// TrimmedMean runs n=11 timed iterations, sorts, drops the top 2 and
// bottom 2 samples, and averages the middle 7 (a 36% trimmed mean).
//
// Why n=11:
//   - Odd: median is well-defined as a fallback diagnostic.
//   - Cheap: 11 × ~10 ms typical = ~110 ms per test, well under the
//     60 s perf-suite timeout.
//   - Drop 2/2: handles one GC pause + one scheduler hiccup without
//     losing too many samples. In practice, ~1 in 10 samples on a
//     loaded host is an outlier; dropping 2 from each end is robust.
//   - Middle 7: variance of the mean scales as 1/√7 ≈ 0.38 — about
//     2× noise reduction vs a single sample, ~1.5× better than the
//     median of 5 used previously.
//
// Larger n (21, 51) was considered but rejected: marginal variance
// reduction (~30%) at 2–5× test cost. n=11 is the sweet spot.
//
// ── 1 ms floor ──────────────────────────────────────────────────────────
//
// PerfBudgetFloor caps the minimum budget at 1 ms regardless of the
// caller's base × multiplier. Anything faster is either "just fast"
// (sub-millisecond timing is dominated by clock resolution + scheduler
// jitter) or a sign that the unit under test is too small to be a
// meaningful regression target. Move such tests to Benchmark* (Track A)
// instead of TestPerf_* (Track B).
package testutil

import (
	"os"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"testing"
	"time"
)

// PerfBudgetMultiplierEnv is the env var read by ColdBudget and WarmBudget.
// CI sets this to 2.0 to absorb shared-runner variance; developers on slow
// laptops can bump it locally. Default is 1.0.
const PerfBudgetMultiplierEnv = "PERF_BUDGET_MULTIPLIER"

// PerfBudgetFloor is the minimum budget any TestPerf_* gate may apply.
// Sub-millisecond budgets are dominated by clock resolution and scheduler
// jitter, not by the unit under test. See package docblock for rationale.
const PerfBudgetFloor = 1 * time.Millisecond

// trimmedMeanN is the sample count for TrimmedMean. n=11 is the project
// default; see the package docblock for the selection rationale.
const trimmedMeanN = 11

// trimmedMeanDrop is the count dropped from each end before averaging.
const trimmedMeanDrop = 2

// ColdBudget returns the budget for a COLD test: one that crosses a
// process or syscall boundary (cold-start exec, real-disk fsync,
// child-process spawn, network). Formula:
//
//	max(base * 5, PerfBudgetFloor) * PERF_BUDGET_MULTIPLIER
//
// Pair with TrimmedMean(fn) for measurement.
func ColdBudget(t *testing.T, base time.Duration) time.Duration {
	t.Helper()
	measure := base * 5
	if measure < PerfBudgetFloor {
		measure = PerfBudgetFloor
	}
	return applyMultiplier(t, measure)
}

// WarmBudget returns the budget for a WARM test: pure in-process Go work
// that can be measured under controlled GC. Formula:
//
//	max(base * 3, PerfBudgetFloor) * PERF_BUDGET_MULTIPLIER
//
// Pair with TrimmedMeanWarm(fn) — that variant forces a GC cycle and
// disables auto-GC around each timed iteration, eliminating the largest
// noise source.
//
// No callers in this PR; exported to capture the cold/warm convention
// in code so future contributors classify their tests correctly.
func WarmBudget(t *testing.T, base time.Duration) time.Duration {
	t.Helper()
	measure := base * 3
	if measure < PerfBudgetFloor {
		measure = PerfBudgetFloor
	}
	return applyMultiplier(t, measure)
}

func applyMultiplier(t *testing.T, measure time.Duration) time.Duration {
	t.Helper()
	raw := os.Getenv(PerfBudgetMultiplierEnv)
	if raw == "" {
		return measure
	}
	mult, err := strconv.ParseFloat(raw, 64)
	if err != nil || mult <= 0 {
		t.Logf("ignoring invalid %s=%q (using 1.0)", PerfBudgetMultiplierEnv, raw)
		return measure
	}
	return time.Duration(float64(measure) * mult)
}

// TrimmedMean runs fn n=11 times (plus 1 warm-up), sorts the samples,
// drops the top 2 and bottom 2, and returns the average of the middle 7.
//
// Use for COLD tests where GC manipulation in the parent process doesn't
// help (the timed work happens in a child process or kernel).
func TrimmedMean(fn func()) time.Duration {
	return trimmedMeanCore(false, nil, fn)
}

// TrimmedMeanWarm is TrimmedMean with controlled GC: forces a runtime.GC()
// before each timed iteration and disables auto-GC during the timed
// window via debug.SetGCPercent(-1), restoring the original setting on
// return.
//
// Use for WARM tests measuring pure-Go in-process work, where GC pauses
// are the dominant noise source.
func TrimmedMeanWarm(fn func()) time.Duration {
	return trimmedMeanCore(true, nil, fn)
}

// TrimmedMeanWithSetup runs setup() before each timed op() but excludes
// setup from the timing window. Use when the timed primitive needs a
// fresh fixture per iteration (e.g. a populated tree before timing
// DeleteAll). Setup and op share state via closure capture in the caller.
//
// COLD variant — no GC manipulation. For warm work needing setup,
// compose: temporarily disable GC yourself or call runtime.GC() inside
// setup before each timed op.
func TrimmedMeanWithSetup(setup, op func()) time.Duration {
	return trimmedMeanCore(false, setup, op)
}

func trimmedMeanCore(controlGC bool, setup, op func()) time.Duration {
	if setup == nil {
		setup = func() {}
	}

	if controlGC {
		// Disable automatic GC for the full helper lifetime (including
		// warm-up). We force a single collection per timed iteration
		// ourselves (below) so the measured op() never absorbs an
		// unrelated GC pause.
		orig := debug.SetGCPercent(-1)
		defer debug.SetGCPercent(orig)
	}

	// Warm-up: full setup + op cycle. Discarded — first invocation pays
	// for any package-level lazy init in fn's transitive callees.
	setup()
	op()

	samples := make([]time.Duration, trimmedMeanN)
	for i := 0; i < trimmedMeanN; i++ {
		setup()
		if controlGC {
			runtime.GC() // pay collection cost outside the timed window
		}
		start := time.Now()
		op()
		samples[i] = time.Since(start)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	middle := samples[trimmedMeanDrop : trimmedMeanN-trimmedMeanDrop]
	var sum time.Duration
	for _, s := range middle {
		sum += s
	}
	return sum / time.Duration(len(middle))
}

// SkipIfShort skips the test when `go test -short` is in effect. TestPerf_*
// tests are expensive enough that contributors running quick unit-test
// loops shouldn't pay for them. CI always runs in long mode.
func SkipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping perf test in -short mode")
	}
}
