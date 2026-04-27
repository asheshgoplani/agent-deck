package testutil

import (
	"os"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

func TestColdBudget_Formula(t *testing.T) {
	clearMultiplier(t)

	// base * 5
	if got := ColdBudget(t, 7*time.Millisecond); got != 35*time.Millisecond {
		t.Fatalf("ColdBudget(7ms) = %v, want 35ms", got)
	}
	// base * 5 below floor → floor
	if got := ColdBudget(t, 100*time.Microsecond); got != PerfBudgetFloor {
		t.Fatalf("ColdBudget(100µs) = %v, want %v (floor)", got, PerfBudgetFloor)
	}
	// base * 5 at exactly floor → floor (tied)
	if got := ColdBudget(t, 200*time.Microsecond); got != PerfBudgetFloor {
		t.Fatalf("ColdBudget(200µs) = %v, want %v (= floor)", got, PerfBudgetFloor)
	}
}

func TestWarmBudget_Formula(t *testing.T) {
	clearMultiplier(t)

	if got := WarmBudget(t, 7*time.Millisecond); got != 21*time.Millisecond {
		t.Fatalf("WarmBudget(7ms) = %v, want 21ms", got)
	}
	if got := WarmBudget(t, 100*time.Microsecond); got != PerfBudgetFloor {
		t.Fatalf("WarmBudget(100µs) = %v, want %v (floor)", got, PerfBudgetFloor)
	}
}

func TestBudget_HonorsMultiplier(t *testing.T) {
	setMultiplier(t, "2.5")

	if got := ColdBudget(t, 4*time.Millisecond); got != 50*time.Millisecond {
		t.Fatalf("ColdBudget(4ms) at multiplier 2.5 = %v, want 50ms", got)
	}
	if got := WarmBudget(t, 4*time.Millisecond); got != 30*time.Millisecond {
		t.Fatalf("WarmBudget(4ms) at multiplier 2.5 = %v, want 30ms", got)
	}
}

func TestBudget_InvalidMultiplierFallsBack(t *testing.T) {
	setMultiplier(t, "garbage")

	if got := ColdBudget(t, 7*time.Millisecond); got != 35*time.Millisecond {
		t.Fatalf("ColdBudget at invalid multiplier = %v, want 35ms (fallback to 1.0)", got)
	}
}

func TestTrimmedMean_RunsElevenSamples(t *testing.T) {
	calls := 0
	_ = TrimmedMean(func() { calls++ })
	want := trimmedMeanN + 1 // 1 warm-up + 11 samples
	if calls != want {
		t.Fatalf("TrimmedMean ran fn %d times, want %d", calls, want)
	}
}

func TestTrimmedMean_DropsExtremes(t *testing.T) {
	// Construct 11 samples where the means with vs without trimming
	// would differ noticeably. Use sleep durations from a fixed sequence.
	// Untrimmed mean ≈ (1+2+3+4+5+6+7+8+9+10+99)/11 ≈ 14ms.
	// Trimmed (drop top 2, bottom 2; middle 7 = 3..9) → (3+4+5+6+7+8+9)/7 = 6ms.
	// We can't directly inject samples, but we can verify the trimmed result
	// is much closer to the middle of the sequence than the untrimmed mean.
	delays := []time.Duration{
		1 * time.Millisecond,
		99 * time.Millisecond, // huge outlier — must be trimmed
		3 * time.Millisecond,
		2 * time.Millisecond,
		4 * time.Millisecond,
		5 * time.Millisecond,
		6 * time.Millisecond,
		7 * time.Millisecond,
		8 * time.Millisecond,
		9 * time.Millisecond,
		10 * time.Millisecond,
	}
	idx := 0
	got := TrimmedMean(func() {
		// First call is warm-up, discarded. After that, run through delays in order.
		if idx == 0 {
			idx++
			return // warm-up
		}
		i := idx - 1
		idx++
		if i < len(delays) {
			time.Sleep(delays[i])
		}
	})

	// The 99 ms outlier must be dropped. If trimming is broken the mean
	// would be ≥ 14 ms. The middle 7 of {1,2,3,4,5,6,7,8,9,10,99} are
	// {3,4,5,6,7,8,9} → mean 6 ms. Allow generous slack for sleep
	// imprecision.
	if got > 12*time.Millisecond {
		t.Fatalf("TrimmedMean = %v; the 99ms outlier was not trimmed (untrimmed mean would be ~14ms)", got)
	}
	if got < 3*time.Millisecond {
		t.Fatalf("TrimmedMean = %v; suspiciously low — not enough samples being averaged", got)
	}
}

func TestTrimmedMeanWithSetup_ExcludesSetup(t *testing.T) {
	setupCalls, opCalls := 0, 0
	got := TrimmedMeanWithSetup(
		func() {
			setupCalls++
			time.Sleep(20 * time.Millisecond)
		},
		func() {
			opCalls++
			time.Sleep(2 * time.Millisecond)
		},
	)
	wantCalls := trimmedMeanN + 1
	if setupCalls != wantCalls || opCalls != wantCalls {
		t.Fatalf("setup=%d op=%d, want both=%d", setupCalls, opCalls, wantCalls)
	}
	// 20 ms setup must NOT be in the trimmed mean. Op sleeps 2 ms.
	if got > 15*time.Millisecond {
		t.Fatalf("TrimmedMeanWithSetup absorbed setup time: got %v, want <15ms", got)
	}
}

func TestTrimmedMeanWarm_DisablesGCAndRestores(t *testing.T) {
	const sentinel = 50
	orig := debug.SetGCPercent(sentinel)
	defer debug.SetGCPercent(orig)

	// Track the minimum GCPercent observed across all op calls
	// (warm-up + timed). It must be -1 — the helper disables auto-GC
	// for its full lifetime.
	minSeen := sentinel
	_ = TrimmedMeanWarm(func() {
		// Read current GCPercent without changing it: SetGCPercent
		// returns the previous value, so we set to that same value.
		cur := debug.SetGCPercent(-1)
		debug.SetGCPercent(cur)
		if cur < minSeen {
			minSeen = cur
		}
	})

	if minSeen != -1 {
		t.Errorf("inside TrimmedMeanWarm, GCPercent never observed as -1; min seen = %d", minSeen)
	}

	// After return, the helper must have restored GCPercent to sentinel.
	if got := debug.SetGCPercent(sentinel); got != sentinel {
		t.Errorf("TrimmedMeanWarm did not restore GCPercent: got %d, want %d", got, sentinel)
	}
}

func TestTrimmedMeanWarm_RunsForcedGC(t *testing.T) {
	// Force the heap to grow a bit so a GC has visible effect.
	before := readNumGC()
	_ = TrimmedMeanWarm(func() {
		// Allocate something garbage-collectable.
		_ = make([]byte, 1<<16)
	})
	after := readNumGC()

	// Each of the 11 timed iterations forces a runtime.GC(). Plus the
	// warm-up doesn't call runtime.GC, but the forced cycles inside the
	// loop should still produce at least 11 collections.
	if after-before < uint32(trimmedMeanN) {
		t.Errorf("TrimmedMeanWarm did not force GC per iteration: NumGC delta = %d, want >= %d",
			after-before, trimmedMeanN)
	}
}

func readNumGC() uint32 {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return s.NumGC
}

// ---- helpers --------------------------------------------------------------

func setMultiplier(t *testing.T, val string) {
	t.Helper()
	old, had := os.LookupEnv(PerfBudgetMultiplierEnv)
	os.Setenv(PerfBudgetMultiplierEnv, val)
	t.Cleanup(func() {
		if had {
			os.Setenv(PerfBudgetMultiplierEnv, old)
		} else {
			os.Unsetenv(PerfBudgetMultiplierEnv)
		}
	})
}

func clearMultiplier(t *testing.T) {
	t.Helper()
	old, had := os.LookupEnv(PerfBudgetMultiplierEnv)
	os.Unsetenv(PerfBudgetMultiplierEnv)
	t.Cleanup(func() {
		if had {
			os.Setenv(PerfBudgetMultiplierEnv, old)
		}
	})
}
