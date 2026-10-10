package telemetry

import (
	"testing"
	"time"
)

// dropOne makes one recording attempt fail on a held state lock.
func dropOne(t *testing.T, record func()) {
	t.Helper()
	unlock, err := lockState()
	if err != nil {
		t.Fatal(err)
	}
	record()
	unlock()
}

func droppedToday(c *clock) int {
	return LoadState().day(dayOf(c.now())).Dropped
}

// TestLockDropBeforeConsentIsNotCounted: nothing observed before consent is
// sent, not even as a count of lock drops.
func TestLockDropBeforeConsentIsNotCounted(t *testing.T) {
	c := env(t)
	dropOne(t, func() { FeatureUsed("costs", false) })
	grant(t, c)
	FeatureUsed("fork", false)
	if d := droppedToday(c); d != 0 {
		t.Fatalf("pre-consent drop counted after grant: dropped=%d", d)
	}
}

// TestLockDropDoesNotSurviveDisableAndRegrant: a decline forgets lock drops
// counted under the previous consent.
func TestLockDropDoesNotSurviveDisableAndRegrant(t *testing.T) {
	c := env(t)
	old := grant(t, c).InstallID
	dropOne(t, func() { FeatureUsed("costs", false) })
	if err := Disable("9.9.9", c.now()); err != nil {
		t.Fatal(err)
	}
	if grant(t, c).InstallID == old {
		t.Fatal("expected a new id after decline and re-grant")
	}
	FeatureUsed("fork", false)
	if d := droppedToday(c); d != 0 {
		t.Fatalf("drop from the previous consent leaked into the new id: dropped=%d", d)
	}
}

// TestLockDropDoesNotSurviveResetID: reset-id forgets lock drops counted
// under the old id.
func TestLockDropDoesNotSurviveResetID(t *testing.T) {
	c := env(t)
	grant(t, c)
	dropOne(t, func() { FeatureUsed("costs", false) })
	if _, err := ResetID(); err != nil {
		t.Fatal(err)
	}
	FeatureUsed("fork", false)
	if d := droppedToday(c); d != 0 {
		t.Fatalf("drop from before reset-id leaked into the new id: dropped=%d", d)
	}
}

// TestLockDropDiscardedWhenConsentLapses: a drop counted under a grant is
// forgotten once a locked update sees consent no longer granted, even when a
// re-prompt answered yes keeps the same install id.
func TestLockDropDiscardedWhenConsentLapses(t *testing.T) {
	c := env(t)
	id := grant(t, c).InstallID
	dropOne(t, func() { FeatureUsed("costs", false) })
	s := LoadState()
	s.Consent = ConsentUndecided
	if err := SaveState(s); err != nil {
		t.Fatal(err)
	}
	FeatureUsed("fork", false)
	if grant(t, c).InstallID != id {
		t.Fatal("expected the re-prompt to keep the install id")
	}
	FeatureUsed("fork", false)
	if d := droppedToday(c); d != 0 {
		t.Fatalf("drop counted before consent lapsed was flushed after: dropped=%d", d)
	}
}

// TestLockDropOfUnrecordableEventIsNotCounted: at level basic an event the
// level would not record is not counted as dropped either.
func TestLockDropOfUnrecordableEventIsNotCounted(t *testing.T) {
	c := env(t)
	grant(t, c)
	if _, err := SetLevel(LevelBasic); err != nil {
		t.Fatal(err)
	}
	dropOne(t, func() { record("session.create", map[string]any{"tool": "claude"}, "") })
	TUIStarted(FleetCounts{Sessions: 1}, true)
	if d := droppedToday(c); d != 0 {
		t.Fatalf("drop of an event basic level never records was counted: dropped=%d", d)
	}
}

// TestLockDropStillCountedUnderSameConsent keeps the original behavior.
func TestLockDropStillCountedUnderSameConsent(t *testing.T) {
	c := env(t)
	grant(t, c)
	dropOne(t, func() { FeatureUsed("costs", false) })
	FeatureUsed("fork", false)
	if d := droppedToday(c); d != 1 {
		t.Fatalf("drop under the current consent not counted: dropped=%d", d)
	}
}

// blockedUpload starts an upload of one pending day and returns once its
// request is in flight; release lets the endpoint answer.
func blockedUpload(t *testing.T, c *clock) (*fakePostHog, chan UploadResult, func()) {
	t.Helper()
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	FeatureUsed("fork", false)
	c.set(at(1, 9, 0))
	fake.mu.Lock()
	fake.block = make(chan struct{})
	release := fake.block
	fake.mu.Unlock()
	done := make(chan UploadResult, 1)
	go func() { done <- MaybeUpload(t.Context()) }()
	for fake.hits() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	var once bool
	return fake, done, func() {
		if !once {
			once = true
			close(release)
		}
	}
}

// TestSetLevelWaitsForInFlightUpload: `telemetry level basic` returns only
// once a send built at the previous level has finished, as `off` does.
func TestSetLevelWaitsForInFlightUpload(t *testing.T) {
	c := env(t)
	_, done, release := blockedUpload(t, c)
	t.Cleanup(release)
	set := make(chan error, 1)
	go func() { _, err := SetLevel(LevelBasic); set <- err }()
	select {
	case <-set:
		t.Fatal("SetLevel returned while a send was in flight")
	case <-time.After(300 * time.Millisecond):
	}
	release()
	<-done
	if err := <-set; err != nil {
		t.Fatal(err)
	}
	if LoadState().Level != LevelBasic {
		t.Fatal("level not saved")
	}
}

// TestUploadInFlightClosesOlderBinariesGate: 1.16.26 and older upload under
// the state lock only after checking now < Upload.NextTry. While this
// process sends without the state lock, that gate must be closed so an
// older binary running side by side cannot send the same spool again.
func TestUploadInFlightClosesOlderBinariesGate(t *testing.T) {
	c := env(t)
	_, done, release := blockedUpload(t, c)
	t.Cleanup(release)
	if s := LoadState(); !c.now().Before(s.Upload.NextTry) {
		t.Fatalf("older binaries' upload gate open during the send: next_try=%v now=%v", s.Upload.NextTry, c.now())
	}
	release()
	if r := <-done; !r.Sent {
		t.Fatalf("upload: %+v", r)
	}
	if s := LoadState(); !s.Upload.NextTry.Equal(c.now().Add(uploadInterval)) {
		t.Fatalf("outcome did not replace the in-flight lease: %v", s.Upload.NextTry)
	}
}

// TestPastDayWriteDuringUploadIsNotResent pins the residual edge case: a
// write into a day whose rollups are in flight keeps its spool line, and the
// day's rollups (deterministic uuids) are not sent a second time.
func TestPastDayWriteDuringUploadIsNotResent(t *testing.T) {
	c := env(t)
	fake, done, release := blockedUpload(t, c)
	t.Cleanup(release)
	day0 := dayOf(at(0, 0, 0))
	recordFrom(surface, "activity.hourly", map[string]any{"running": CountBucket(1), "human_active": true}, "", at(0, 23, 0))
	release()
	if r := <-done; !r.Sent {
		t.Fatalf("upload: %+v", r)
	}
	if !spoolHas(t, "activity.hourly", day0) {
		t.Fatalf("past-day line written during the send was lost; spool %v", eventNames(spoolLines(t)))
	}
	if _, ok := LoadState().Daily[day0]; ok {
		t.Fatal("sent day kept after the upload; its rollups would be resent with the same uuids")
	}
	first := map[string]bool{}
	for _, ev := range fake.batch(t, 0).Batch {
		first[ev.UUID] = true
	}
	c.add(uploadInterval + time.Minute)
	if r := MaybeUpload(t.Context()); !r.Sent {
		t.Fatalf("second upload: %+v", r)
	}
	sawLine := false
	for _, ev := range fake.batch(t, fake.hits()-1).Batch {
		if first[ev.UUID] {
			t.Fatalf("%s resent with uuid already sent", ev.Event)
		}
		if ev.Event == "activity.hourly" {
			sawLine = true
		}
	}
	if !sawLine {
		t.Fatal("past-day line not delivered by the next upload")
	}
}
