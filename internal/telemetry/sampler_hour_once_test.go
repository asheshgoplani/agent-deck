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

// TestUploadSpoolReadErrorDoesNotReemitOpenHour: when the uploader cannot
// read the spool, the stored open hour must not be spooled without the
// state that records it as emitted, or the retry spools it a second time.
func TestUploadSpoolReadErrorDoesNotReemitOpenHour(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("needs a non-root user: root reads a write-only spool")
	}
	c := env(t)
	f := newFakePostHog(t)
	grant(t, c)
	openCloseTUI(t, c, at(1, 21, 5)) // stores 21:00
	p, err := spoolPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o200); err != nil { // writable, not readable
		t.Fatal(err)
	}
	c.set(at(2, 9, 0))
	if r := MaybeUpload(t.Context()); r.Attempted {
		t.Fatalf("upload attempted with an unreadable spool: %+v", r)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	MaybeUpload(t.Context())
	n := 0
	for i := 0; i < f.hits(); i++ {
		for _, e := range f.batch(t, i).Batch {
			if e.Event == "activity.hourly" {
				n++
			}
		}
	}
	n += len(hourlyLines(t))
	if n != 1 {
		t.Fatalf("21:00 shipped or left spooled %d times, want 1", n)
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

// openCloseTUIActive is openCloseTUI with a key press, so the hour is active
// both by minutes and by human input.
func openCloseTUIActive(t *testing.T, c *clock, when time.Time) {
	t.Helper()
	c.set(when)
	sp := NewSampler()
	if sp == nil {
		t.Fatal("TUI did not win the sampler lock")
	}
	sp.Observe(running(1))
	sp.KeyPressed()
	sp.Close()
}

// assertNoOpenHourStored fails when the state holds an open hour, in memory
// or on disk.
func assertNoOpenHourStored(t *testing.T, why string) {
	t.Helper()
	p, err := StatePath()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if LoadState().OpenHour != nil || strings.Contains(string(b), "open_hour") {
		t.Fatalf("open hour stored %s: %s", why, b)
	}
}

// TestBasicLevelNeverStoresOpenHour: activity.hourly is a full-only event, so
// a TUI closed at level basic (chosen with `telemetry level`, or capped by
// config) stores no open hour, and that hour never ships after the level is
// raised to full. Raising basic to full is a consent decision.
func TestBasicLevelNeverStoresOpenHour(t *testing.T) {
	for _, viaConfig := range []bool{false, true} {
		name := "state"
		if viaConfig {
			name = "config"
		}
		t.Run(name, func(t *testing.T) {
			c := env(t)
			f := newFakePostHog(t)
			grant(t, c)
			if viaConfig {
				SetConfigLevel("basic")
			} else if _, err := SetLevel(LevelBasic); err != nil {
				t.Fatal(err)
			}
			openCloseTUIActive(t, c, at(1, 21, 5))
			assertNoOpenHourStored(t, "at level basic")
			if viaConfig {
				SetConfigLevel("")
			} else if _, err := SetLevel(LevelFull); err != nil {
				t.Fatal(err)
			}
			openCloseTUIActive(t, c, at(1, 22, 3)) // next TUI, now at full
			c.set(at(2, 9, 0))
			MaybeUpload(t.Context())
			for _, l := range hourlyLines(t) {
				if l.H != nil && *l.H == 21 {
					t.Fatalf("hour sampled at basic shipped after raising to full: %v", l.P)
				}
			}
			for i := 0; i < f.hits(); i++ {
				for _, e := range f.batch(t, i).Batch {
					if e.Event == "activity.hourly" && strings.HasPrefix(e.Timestamp, dayOf(at(1, 0, 0))+"T21") {
						t.Fatalf("hour sampled at basic uploaded after raising to full: %+v", e)
					}
				}
			}
		})
	}
}

// TestBasicLevelNeverEmitsStoredOpenHour: an hour stored at full is not
// emitted, adopted or kept once the effective level is basic (a config cap
// does not go through SetLevel), by a TUI resume or by an upload.
func TestBasicLevelNeverEmitsStoredOpenHour(t *testing.T) {
	for _, path := range []string{"resume", "upload", "preview"} {
		t.Run(path, func(t *testing.T) {
			c := env(t)
			f := newFakePostHog(t)
			grant(t, c)
			openCloseTUIActive(t, c, at(1, 21, 5)) // stored at full
			if LoadState().OpenHour == nil {
				t.Fatal("expected a stored open hour at full")
			}
			SetConfigLevel("basic")
			switch path {
			case "resume":
				c.set(at(1, 21, 30)) // same hour: must not be adopted
				sp := NewSampler()
				if sp == nil {
					t.Fatal("no sampler")
				}
				sp.Close()
				assertNoOpenHourStored(t, "after a resume at level basic")
				SetConfigLevel("")
				openCloseTUI(t, c, at(1, 22, 3))
			case "upload":
				c.set(at(2, 9, 0))
				MaybeUpload(t.Context())
				assertNoOpenHourStored(t, "after an upload at level basic")
			case "preview":
				c.set(at(2, 9, 0))
				bodies, err := PreviewBatch()
				if err != nil {
					t.Fatal(err)
				}
				for _, b := range bodies {
					if strings.Contains(string(b), "activity.hourly") {
						t.Fatalf("preview at level basic shows the stored hour: %s", b)
					}
				}
			}
			for _, l := range hourlyLines(t) {
				if l.D == dayOf(at(1, 0, 0)) && (l.H == nil || *l.H == 21) {
					t.Fatalf("stored hour emitted at level basic: %+v", l)
				}
			}
			for i := 0; i < f.hits(); i++ {
				for _, e := range f.batch(t, i).Batch {
					if e.Event == "activity.hourly" {
						t.Fatalf("stored hour uploaded at level basic: %+v", e)
					}
				}
			}
		})
	}
}

// TestSetLevelLeavingFullClearsOpenHour: `telemetry level basic` forgets the
// stored hour; staying at full keeps it.
func TestSetLevelLeavingFullClearsOpenHour(t *testing.T) {
	c := env(t)
	grant(t, c)
	openCloseTUIActive(t, c, at(1, 21, 5))
	if _, err := SetLevel(LevelFull); err != nil {
		t.Fatal(err)
	}
	if LoadState().OpenHour == nil {
		t.Fatal("SetLevel(full) at full dropped the stored hour")
	}
	if _, err := SetLevel(LevelBasic); err != nil {
		t.Fatal(err)
	}
	assertNoOpenHourStored(t, "after SetLevel(basic)")
	if _, err := SetLevel(LevelFull); err != nil {
		t.Fatal(err)
	}
	openCloseTUI(t, c, at(1, 22, 3))
	for _, l := range hourlyLines(t) {
		if *l.H == 21 {
			t.Fatalf("hour stored before leaving full shipped after returning to full: %v", l.P)
		}
	}
}
