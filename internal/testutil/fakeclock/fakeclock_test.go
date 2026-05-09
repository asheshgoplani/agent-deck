package fakeclock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil/fakeclock"
)

func TestNew_StartsAtSeed(t *testing.T) {
	seed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	c := fakeclock.New(seed)
	if got := c.Now(); !got.Equal(seed) {
		t.Fatalf("Now()=%v want %v", got, seed)
	}
}

func TestAdvance_MovesNowForward(t *testing.T) {
	seed := time.Unix(0, 0)
	c := fakeclock.New(seed)

	c.Advance(5 * time.Second)
	if got := c.Now().Sub(seed); got != 5*time.Second {
		t.Fatalf("after Advance(5s) elapsed=%v want 5s", got)
	}

	c.Advance(2 * time.Minute)
	if got := c.Now().Sub(seed); got != 5*time.Second+2*time.Minute {
		t.Fatalf("after second Advance elapsed=%v want 2m5s", got)
	}
}

func TestAdvance_NegativeIsNoop(t *testing.T) {
	seed := time.Unix(1000, 0)
	c := fakeclock.New(seed)
	c.Advance(-1 * time.Hour)
	if !c.Now().Equal(seed) {
		t.Fatalf("negative Advance moved clock: %v", c.Now())
	}
}

func TestAdvance_ConcurrentSafe(t *testing.T) {
	c := fakeclock.New(time.Unix(0, 0))
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Advance(time.Millisecond)
			_ = c.Now()
		}()
	}
	wg.Wait()
	if got := c.Now().Sub(time.Unix(0, 0)); got != 100*time.Millisecond {
		t.Fatalf("after 100 concurrent Advance(1ms) elapsed=%v want 100ms", got)
	}
}

func TestSet_OverridesNow(t *testing.T) {
	c := fakeclock.New(time.Unix(0, 0))
	target := time.Date(2030, 6, 6, 6, 6, 6, 0, time.UTC)
	c.Set(target)
	if !c.Now().Equal(target) {
		t.Fatalf("Set then Now()=%v want %v", c.Now(), target)
	}
}

// TestImplementsClockInterface ensures fakeclock satisfies the Clock interface
// so production code that depends on Clock can be wired to it.
func TestImplementsClockInterface(t *testing.T) {
	var _ fakeclock.Clock = fakeclock.New(time.Now())
	var _ fakeclock.Clock = fakeclock.Real{}
}

// TestReal_NowMatchesWallClock proves the Real impl is a thin wrapper.
func TestReal_NowMatchesWallClock(t *testing.T) {
	r := fakeclock.Real{}
	before := time.Now()
	got := r.Now()
	after := time.Now()
	if got.Before(before) || got.After(after) {
		t.Fatalf("Real.Now()=%v not within [%v,%v]", got, before, after)
	}
}
