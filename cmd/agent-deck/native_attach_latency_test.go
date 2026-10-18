package main

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// nativeLatencyBatch is one attach in the latency comparison: count frames
// sent through the direct-SSH path or the Agent Deck path.
type nativeLatencyBatch struct {
	direct bool
	count  int
}

// nativeLatencySchedule interleaves the direct-SSH and Agent Deck latency
// samples in short alternating batches. Shared CI runners (macOS in
// particular) drift between latency regimes during a run, for example from a
// ~150ms to a ~300ms ceiling. Measuring all direct samples and then all deck
// samples compared two different regimes and failed the p95 budget without
// any Agent Deck regression (#2698). Interleaving exposes both paths to the
// same runner conditions; the leading path alternates each round.
func nativeLatencySchedule(samples, perBatch int) []nativeLatencyBatch {
	var schedule []nativeLatencyBatch
	for round, done := 0, 0; done < samples; round++ {
		count := min(perBatch, samples-done)
		first := round%2 == 0
		schedule = append(schedule, nativeLatencyBatch{direct: first, count: count}, nativeLatencyBatch{direct: !first, count: count})
		done += count
	}
	return schedule
}

// nativeLatencyBudget checks the sorted sample sets: the Agent Deck p95 may
// exceed the direct-SSH p95 by at most 50ms, and no deck sample may exceed 1s.
func nativeLatencyBudget(direct, deck []time.Duration) error {
	if len(direct) != len(deck) || len(deck) < 2 {
		return fmt.Errorf("unmatched latency samples: direct=%d deck=%d", len(direct), len(deck))
	}
	p95 := len(deck) * 95 / 100
	if p95 >= len(deck) {
		p95 = len(deck) - 1
	}
	if deck[p95] > direct[p95]+50*time.Millisecond {
		return fmt.Errorf("agent deck p95=%v exceeds direct SSH p95=%v plus 50ms", deck[p95], direct[p95])
	}
	if slowest := deck[len(deck)-1]; slowest > time.Second {
		return fmt.Errorf("agent deck max latency=%v exceeds 1s", slowest)
	}
	return nil
}

func TestNativeLatencyScheduleBalancesRunnerDrift(t *testing.T) {
	const samples = 30
	schedule := nativeLatencySchedule(samples, 6)
	var directTotal, deckTotal int
	for index, batch := range schedule {
		if index%2 == 1 && batch.direct == schedule[index-1].direct {
			t.Fatalf("batch %d does not alternate paths: %+v", index, schedule)
		}
		if batch.direct {
			directTotal += batch.count
		} else {
			deckTotal += batch.count
		}
	}
	if directTotal != samples || deckTotal != samples {
		t.Fatalf("schedule sample totals direct=%d deck=%d want %d each", directTotal, deckTotal, samples)
	}

	// Model the observed failure (runs 39076188732, 39071390602): the runner
	// moves from a ~150ms latency ceiling to a ~300ms ceiling halfway through
	// the comparison while both paths have identical latency.
	var direct, deck []time.Duration
	position := 0
	for _, batch := range schedule {
		for range batch.count {
			ceiling := 150 * time.Millisecond
			if position >= samples {
				ceiling = 300 * time.Millisecond
			}
			latency := 20*time.Millisecond + time.Duration(position*37%100)*(ceiling-20*time.Millisecond)/100
			if batch.direct {
				direct = append(direct, latency)
			} else {
				deck = append(deck, latency)
			}
			position++
		}
	}
	sort.Slice(direct, func(i, j int) bool { return direct[i] < direct[j] })
	sort.Slice(deck, func(i, j int) bool { return deck[i] < deck[j] })
	if err := nativeLatencyBudget(direct, deck); err != nil {
		t.Fatalf("identical paths failed the budget under runner drift: %v", err)
	}

	// The budget must still catch a real added delay on the deck path.
	slow := make([]time.Duration, len(deck))
	for index, latency := range deck {
		slow[index] = latency + 100*time.Millisecond
	}
	if err := nativeLatencyBudget(direct, slow); err == nil {
		t.Fatal("a 100ms deck regression passed the budget")
	}
}
