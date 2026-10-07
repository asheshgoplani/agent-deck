package telemetry

import (
	"testing"
	"time"
)

// openCloseTUI runs one TUI's sampler: it wins the lock, samples once at
// the given time and exits.
func openCloseTUI(t *testing.T, c *clock, when time.Time) {
	t.Helper()
	c.set(when)
	sp := NewSampler()
	if sp == nil {
		t.Fatal("TUI did not win the sampler lock")
	}
	sp.Observe(running(1))
	sp.Close()
}

// TestSamplerEmitsOneHourAcrossTUIRuns: TUIs opened and closed one after
// another in the same local hour yield one activity.hourly for that hour,
// with the minutes of every run, emitted only once the hour is over.
func TestSamplerEmitsOneHourAcrossTUIRuns(t *testing.T) {
	c := env(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5))
	openCloseTUI(t, c, at(1, 21, 25))
	if n := len(hourlyLines(t)); n != 0 {
		t.Fatalf("%d activity.hourly spooled while 21:00 is still open", n)
	}
	openCloseTUI(t, c, at(1, 22, 1)) // the next hour's TUI emits 21:00
	hourly := hourlyLines(t)
	if len(hourly) != 1 {
		for _, l := range hourly {
			t.Logf("day %s hour %d %v", l.D, *l.H, l.P)
		}
		t.Fatalf("want one activity.hourly for 21:00, got %d", len(hourly))
	}
	if l := hourly[0]; l.D != dayOf(at(1, 21, 0)) || *l.H != 21 || l.P["sampled_min"] != "2-3" || l.P["running"] != "1" {
		t.Fatalf("hour %s %d props %v, want 21:00 with both runs' minutes", l.D, *l.H, l.P)
	}
}

// TestOpenHourShipsWithUploadAfterLastTUI: the last TUI of a day leaves its
// hour open; the next upload emits it before building that day's rollups, so
// it is neither lost nor added to an already uploaded day.
func TestOpenHourShipsWithUploadAfterLastTUI(t *testing.T) {
	c := env(t)
	f := newFakePostHog(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5))
	c.set(at(2, 9, 0))
	if r := MaybeUpload(t.Context()); f.hits() == 0 {
		t.Fatalf("no upload: %+v", r)
	}
	var hourly, usage int
	for i := 0; i < f.hits(); i++ {
		for _, e := range f.batch(t, i).Batch {
			switch e.Event {
			case "activity.hourly":
				hourly++
			case "usage.daily":
				usage++
			}
		}
	}
	if hourly != 1 || usage != 1 {
		t.Fatalf("uploaded %d activity.hourly and %d usage.daily, want 1 and 1", hourly, usage)
	}
	if s := LoadState(); s.OpenHour != nil {
		t.Fatalf("open hour kept after it was emitted: %+v", s.OpenHour)
	}
}

// TestDisableForgetsOpenHour: turning telemetry off drops the stored hour.
func TestDisableForgetsOpenHour(t *testing.T) {
	c := env(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5))
	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if s := LoadState(); s.OpenHour != nil {
		t.Fatalf("open hour survived Disable: %+v", s.OpenHour)
	}
}
