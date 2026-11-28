package voicestream

import (
	"testing"
	"time"
)

// The stop frame ("off", reason stopped) is the last thing a follower
// hears from this producer. A bus that refuses it for a few hundred
// milliseconds (a full queue at shutdown) must not lose it.
func TestProducer_StopFrameSurvivesABusyBus(t *testing.T) {
	r := newRig(t)
	r.write(nil)
	if ev, ok := r.step(); !ok || !ev.On {
		t.Fatalf("setup: %+v %v", ev, ok)
	}
	busyUntil := time.Now().Add(400 * time.Millisecond)
	var stops int
	r.p.Publish = func(kind, sid string, data any) bool {
		if time.Now().Before(busyUntil) {
			return false
		}
		if ev, ok := data.(StateEvent); ok && !ev.On && ev.Reason == ReasonStopped {
			stops++
		}
		return true
	}
	r.p.stop()
	if stops != 1 {
		t.Fatalf("stop frame published %d times after a 400 ms busy bus, want 1", stops)
	}
}
