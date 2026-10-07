package telemetry

import (
	"context"
	"testing"
	"time"
)

// spoolHas reports whether the spool holds an event named name on day.
func spoolHas(t *testing.T, name, day string) bool {
	t.Helper()
	for _, l := range spoolLines(t) {
		if l.E == name && l.D == day {
			return true
		}
	}
	return false
}

// TestTUIStartDuringUploadIsRecorded: the TUI start that triggers a due
// upload records app.start, tui_starts and env.snapshot while the request is
// in flight, and the upload's spool rewrite keeps them.
func TestTUIStartDuringUploadIsRecorded(t *testing.T) {
	c := env(t)
	fake := newFakePostHog(t)
	grant(t, c)
	SessionEnded(SessionEndInfo{Tool: "claude", Kind: EndStop})
	c.set(at(1, 9, 0))
	today := dayOf(c.now())
	fake.mu.Lock()
	fake.block = make(chan struct{})
	release := fake.block
	fake.mu.Unlock()
	done := make(chan UploadResult, 1)
	go func() { done <- MaybeUpload(t.Context()) }()
	for fake.hits() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	// The endpoint stays blocked for 2 s, as a slow network would.
	timer := time.AfterFunc(2*time.Second, func() { close(release) })
	t.Cleanup(func() { timer.Stop() })
	TUIStarted(FleetCounts{Sessions: 1}, true)
	EnvSnapshot(EnvInfo{Terminal: "ghostty", Shell: "zsh"})
	if r := <-done; !r.Sent {
		t.Fatalf("upload: %+v", r)
	}
	sawEnd := false
	for _, ev := range fake.batch(t, 0).Batch {
		if ev.Event == "session.end" {
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Fatal("the pending session.end was not uploaded")
	}
	if !spoolHas(t, "app.start", today) || !spoolHas(t, "env.snapshot", today) {
		t.Fatalf("TUI start during the upload was dropped; spool %v", eventNames(spoolLines(t)))
	}
	if spoolHas(t, "session.end", dayOf(at(0, 0, 0))) {
		t.Fatal("acknowledged line kept in the spool")
	}
	if r := LoadState().Daily[today]; r == nil || r.TUIStarts != 1 {
		t.Fatalf("tui_starts not counted: %+v", r)
	}
}

// TestTUIStartDuringInstallTickIsRecorded: the install.tick POST does not
// hold the state lock either.
func TestTUIStartDuringInstallTickIsRecorded(t *testing.T) {
	c := tickEnv(t)
	today := dayOf(c.now())
	done := make(chan UploadResult, 1)
	entered, release := make(chan struct{}), make(chan struct{})
	go func() {
		done <- maybeInstallTick(context.Background(), func(context.Context, []byte, time.Duration) postResult {
			close(entered)
			<-release
			return postResult{status: 200}
		})
	}()
	<-entered
	TUIStarted(FleetCounts{Sessions: 1}, true)
	close(release)
	if r := <-done; !r.Sent {
		t.Fatalf("tick: %+v", r)
	}
	if !spoolHas(t, "app.start", today) {
		t.Fatalf("TUI start during the tick was dropped; spool %v", eventNames(spoolLines(t)))
	}
	if tick, _ := readInstallTick(); !tick.Sent || tick.Day != today {
		t.Fatalf("tick acknowledgment not saved: %+v", tick)
	}
}

// TestRecordingWaitsBrieflyAndCountsLockDrops: a short lock hold no longer
// drops an event, and a drop that remains is counted in the day's Dropped.
func TestRecordingWaitsBrieflyAndCountsLockDrops(t *testing.T) {
	c := env(t)
	grant(t, c)
	today := dayOf(c.now())
	unlock, err := lockState()
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(15*time.Millisecond, unlock)
	FeatureUsed("fork", false)
	if f := LoadState().day(today).Features["fork"]; f == nil {
		t.Fatal("event dropped behind a 15 ms lock hold")
	}
	unlock, err = lockState()
	if err != nil {
		t.Fatal(err)
	}
	FeatureUsed("costs", false)
	unlock()
	if f := LoadState().day(today).Features["costs"]; f != nil {
		t.Fatal("event recorded while the lock was held")
	}
	FeatureUsed("fork", false)
	if r := LoadState().day(today); r.Dropped != 1 {
		t.Fatalf("lock drop not counted: dropped=%d", r.Dropped)
	}
}
