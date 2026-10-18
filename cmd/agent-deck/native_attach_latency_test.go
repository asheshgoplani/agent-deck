package main

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// nativeLatencyOrder returns the measurement order for the direct-SSH and
// Agent Deck latency comparison: true means the next sample goes through the
// direct-SSH path. Both clients stay attached and the paths alternate per
// sample (D K D K ...), so any stretch of the run holds the same number of
// samples from each path, give or take one.
//
// Shared CI runners (macOS in particular) switch between latency regimes
// during a run, for example from a ~150ms to a ~300ms ceiling. Measuring all
// direct samples first compared two different regimes (#2698). Alternating
// batches of six still let one slow window land on deck batches only: run
// 39093139319 shifted every deck quantile by ~60ms (median, mean and p95)
// although the deck minimum and every other run showed no added per-keystroke
// delay. Alternating per sample gives any runner slowdown longer than a few
// samples to both paths in equal measure.
func nativeLatencyOrder(samples int) []bool {
	order := make([]bool, 0, 2*samples)
	for range samples {
		order = append(order, true, false)
	}
	return order
}

// nativeLatencyBudget checks the sorted sample sets: the mean of the Agent
// Deck samples from the median up to the p95 may exceed the same direct-SSH
// mean by at most 40ms, and no deck sample may exceed 1s.
//
// The previous check compared the two p95 samples alone (the second-largest
// of 30) against a 50ms margin. On macOS runners samples spread from ~20ms up
// to a 150ms or 300ms ceiling, so that single order statistic was decided by
// the two or three slowest keystrokes of each path. The upper-half mean uses
// fourteen samples of the same tail. Resampling the recorded macOS sets of 21
// main and PR runs (both paths pooled, so no real difference) fails it as
// often as the old check (0.3%), and it catches a real deck delay at least as
// often: a constant 60ms (94% vs 82%), a 0-100ms polling delay (84% vs 78%).
func nativeLatencyBudget(direct, deck []time.Duration) error {
	if len(direct) != len(deck) || len(deck) < 2 {
		return fmt.Errorf("unmatched latency samples: direct=%d deck=%d", len(direct), len(deck))
	}
	p95 := len(deck) * 95 / 100
	if p95 >= len(deck) {
		p95 = len(deck) - 1
	}
	upper := func(samples []time.Duration) time.Duration {
		var sum time.Duration
		for _, sample := range samples[len(samples)/2 : p95+1] {
			sum += sample
		}
		return sum / time.Duration(p95+1-len(samples)/2)
	}
	if deckUpper, directUpper := upper(deck), upper(direct); deckUpper > directUpper+40*time.Millisecond {
		return fmt.Errorf("agent deck median-to-p95 mean=%v exceeds direct SSH mean=%v plus 40ms", deckUpper, directUpper)
	}
	if slowest := deck[len(deck)-1]; slowest > time.Second {
		return fmt.Errorf("agent deck max latency=%v exceeds 1s", slowest)
	}
	return nil
}

// nativeLatencySamples models identical paths on a drifting runner: sample
// latencies spread from 20ms up to a 150ms ceiling, or a 300ms ceiling while
// the runner is slow, and two back-to-back samples see the same runner
// conditions. extra is added to every deck sample.
func nativeLatencySamples(order []bool, slow func(position int) bool, extra time.Duration) (direct, deck []time.Duration) {
	for position, isDirect := range order {
		ceiling := 150 * time.Millisecond
		if slow(position) {
			ceiling = 300 * time.Millisecond
		}
		latency := 20*time.Millisecond + time.Duration(position/2*37%100)*(ceiling-20*time.Millisecond)/100
		if isDirect {
			direct = append(direct, latency)
		} else {
			deck = append(deck, latency+extra)
		}
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i] < direct[j] })
	sort.Slice(deck, func(i, j int) bool { return deck[i] < deck[j] })
	return direct, deck
}

func TestNativeLatencyOrderBalancesRunnerDrift(t *testing.T) {
	const samples = 30
	order := nativeLatencyOrder(samples)
	var directTotal, deckTotal int
	for _, isDirect := range order {
		if isDirect {
			directTotal++
		} else {
			deckTotal++
		}
	}
	if directTotal != samples || deckTotal != samples {
		t.Fatalf("order sample totals direct=%d deck=%d want %d each", directTotal, deckTotal, samples)
	}

	// Any stretch of the run gives both paths nearly the same number of
	// samples, so a runner slowdown cannot land on one path only.
	for width := 2; width <= len(order); width++ {
		for start := 0; start+width <= len(order); start++ {
			balance := 0
			for _, isDirect := range order[start : start+width] {
				if isDirect {
					balance++
				} else {
					balance--
				}
			}
			if balance > 1 || balance < -1 {
				t.Fatalf("samples %d..%d are unbalanced between paths (direct-deck=%d)", start, start+width-1, balance)
			}
		}
	}

	// Runs 39076188732 and 39071390602: the runner moves to the slow regime
	// halfway through the comparison.
	direct, deck := nativeLatencySamples(order, func(position int) bool { return position >= samples }, 0)
	if err := nativeLatencyBudget(direct, deck); err != nil {
		t.Fatalf("identical paths failed the budget when the runner slowed halfway: %v", err)
	}
	// Run 39093139319: a slow window of a few seconds, anywhere in the run.
	// With batches of six it can cover deck samples only.
	for width := 6; width <= 18; width++ {
		for start := 0; start+width <= len(order); start++ {
			slow := func(position int) bool { return position >= start && position < start+width }
			direct, deck := nativeLatencySamples(order, slow, 0)
			if err := nativeLatencyBudget(direct, deck); err != nil {
				t.Fatalf("identical paths failed the budget with a slow window of %d samples at %d: %v", width, start, err)
			}
		}
	}

	// The budget must still catch a real added delay on the deck path, in
	// either runner regime and across a slow window.
	for _, slow := range []func(int) bool{
		func(int) bool { return false },
		func(int) bool { return true },
		func(position int) bool { return position >= 40 && position < 52 },
	} {
		direct, deck := nativeLatencySamples(order, slow, 100*time.Millisecond)
		if err := nativeLatencyBudget(direct, deck); err == nil {
			t.Fatal("a 100ms deck regression passed the budget")
		}
	}
}

func nativeLatencyMillis(values ...float64) []time.Duration {
	samples := make([]time.Duration, len(values))
	for index, value := range values {
		samples[index] = time.Duration(value * float64(time.Millisecond))
	}
	return samples
}

func TestNativeLatencyBudgetRecordedMacOSRuns(t *testing.T) {
	// Run 39085819309 (passed): p95 delta 43.8ms, a few ms under the old margin.
	direct := nativeLatencyMillis(28.1, 35.3, 48.3, 84.6, 89.3, 90.2, 96.3, 106.3, 111.9, 112.4, 120.9, 127.1, 128.4, 130.1, 149.5, 161.3, 161.6, 167.2, 175.3, 175.4, 204.2, 206.1, 206.2, 208.8, 213.7, 216.0, 220.9, 226.0, 227.6, 237.2)
	deck := nativeLatencyMillis(20.0, 42.0, 59.6, 92.4, 102.5, 102.9, 103.0, 103.3, 105.4, 107.6, 119.9, 120.0, 121.0, 138.4, 141.4, 157.8, 163.9, 164.9, 203.0, 204.3, 222.7, 233.2, 234.2, 234.8, 237.1, 241.7, 262.7, 269.5, 271.4, 272.9)
	if err := nativeLatencyBudget(direct, deck); err != nil {
		t.Fatalf("recorded passing run: %v", err)
	}
	slow := make([]time.Duration, len(deck))
	for index, latency := range deck {
		slow[index] = latency + 60*time.Millisecond
	}
	if err := nativeLatencyBudget(direct, slow); err == nil {
		t.Fatal("a 60ms deck delay on a recorded run passed the budget")
	}
	// Run 39093139319 (failed, batches of six): every deck quantile sat ~60ms
	// higher, not just the p95. A looser statistic would have hidden that, so
	// it must still fail; per-sample alternation is what keeps a runner
	// slowdown off one path.
	direct = nativeLatencyMillis(26.1, 91.9, 102.2, 102.3, 102.3, 102.4, 116.0, 116.9, 125.0, 136.4, 137.0, 138.2, 138.6, 153.1, 154.2, 156.4, 187.1, 203.1, 203.3, 206.8, 211.4, 214.4, 220.2, 221.0, 221.0, 225.4, 232.1, 239.9, 242.7, 253.9)
	deck = nativeLatencyMillis(102.2, 112.1, 141.2, 155.1, 155.3, 203.4, 204.0, 204.2, 204.9, 206.2, 217.9, 230.2, 231.4, 235.6, 239.1, 240.4, 240.5, 241.1, 243.4, 243.8, 248.1, 249.8, 256.2, 256.6, 279.4, 280.3, 287.6, 299.0, 304.6, 317.4)
	if err := nativeLatencyBudget(direct, deck); err == nil {
		t.Fatal("a whole-distribution 60ms deck shift passed the budget")
	}
}
