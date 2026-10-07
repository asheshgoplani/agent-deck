package telemetry

import (
	"os"
	"strings"
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

// TestSamplerMergesStoredHourWhenResumeLostLock: a TUI whose resume loses the
// state lock (to the upload the TUI itself starts at launch, or to a CLI or
// hook recording) does not adopt the stored part of the current hour. When
// it stays open past the hour end, the hour must still ship once, with the
// minutes of both parts.
func TestSamplerMergesStoredHourWhenResumeLostLock(t *testing.T) {
	c := env(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5)) // stores 21:00
	c.set(at(1, 21, 30))
	unlock, err := lockState() // an upload holds the state lock during resume
	if err != nil {
		t.Fatal(err)
	}
	sp := NewSampler()
	unlock()
	if sp == nil {
		t.Fatal("no sampler")
	}
	sp.Observe(running(1))
	c.set(at(1, 22, 1))
	sp.Observe(running(1)) // the hour rolls in this TUI
	sp.Close()
	openCloseTUI(t, c, at(1, 23, 2)) // emits any stored past hour
	var rows []spoolLine
	for _, l := range hourlyLines(t) {
		if *l.H == 21 {
			rows = append(rows, l)
		}
	}
	if len(rows) != 1 {
		for _, l := range rows {
			t.Logf("21:00 row sampled_min=%v", l.P["sampled_min"])
		}
		t.Fatalf("%d activity.hourly rows for 21:00, want 1", len(rows))
	}
	if got := rows[0].P["sampled_min"]; got != "2-3" {
		t.Fatalf("21:00 sampled_min=%v, want 2-3 (both parts)", got)
	}
}

// TestOpenHourNeedsConsent: without consent nothing is stored or spooled.
func TestOpenHourNeedsConsent(t *testing.T) {
	c := env(t)
	c.set(at(1, 21, 5))
	sp := NewSampler()
	if sp == nil {
		t.Fatal("no sampler")
	}
	sp.Observe(running(1))
	sp.KeyPressed()
	sp.Close()
	p, _ := StatePath()
	b, _ := os.ReadFile(p)
	if LoadState().OpenHour != nil || strings.Contains(string(b), "open_hour") {
		t.Fatalf("open hour stored without consent: %s", b)
	}
	if n := len(hourlyLines(t)); n != 0 {
		t.Fatalf("%d hourly lines without consent", n)
	}
}

// TestDeclineAndResetIDForgetOpenHour: a declined hour never ships after a
// later grant, and ResetID rotates the id and forgets the stored hour.
func TestDeclineAndResetIDForgetOpenHour(t *testing.T) {
	c := env(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5))
	s := LoadState()
	Decline(s, "9.9.9", c.now())
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	if LoadState().OpenHour != nil {
		t.Fatal("open hour survived Decline")
	}
	c.set(at(2, 10, 0))
	grant(t, c)
	openCloseTUI(t, c, at(2, 11, 5))
	for _, l := range hourlyLines(t) {
		if l.D == dayOf(at(1, 21, 0)) {
			t.Fatalf("pre-decline hour shipped after re-grant: %+v", l)
		}
	}
	old := LoadState().InstallID
	if LoadState().OpenHour == nil {
		t.Fatal("expected a stored open hour before ResetID")
	}
	s2, err := ResetID()
	if err != nil {
		t.Fatal(err)
	}
	if s2.InstallID == old || LoadState().InstallID == old {
		t.Fatal("ResetID did not rotate the id")
	}
	if LoadState().OpenHour != nil {
		t.Fatal("open hour survived ResetID")
	}
}
