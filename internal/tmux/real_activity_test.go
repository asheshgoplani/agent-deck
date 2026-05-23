package tmux

import (
	"testing"
	"time"
)

// realActivityConfirmed must default to false at tracker construction —
// even though lastChangeTime is initialized to time.Now(), we don't yet
// know whether the underlying pane has actually done anything. Callers
// (the TUI timestamp badge) rely on this gating to avoid showing every
// session as "just now" right after agent-deck starts.

func TestStateTracker_RealActivity_FalseAtConstruction(t *testing.T) {
	tracker := &StateTracker{
		lastChangeTime: time.Now(),
	}
	if tracker.realActivityConfirmed {
		t.Fatal("freshly-constructed tracker must NOT report realActivityConfirmed=true; " +
			"that flag is what callers use to distinguish 'we've never observed activity' " +
			"from 'we observed activity at lastChangeTime'")
	}
}

func TestSession_LastObservedActivity_FalseWhenNoTracker(t *testing.T) {
	s := &Session{}
	ts, observed := s.LastObservedActivity()
	if observed {
		t.Errorf("Session with nil stateTracker must return observed=false, got %v", ts)
	}
	if !ts.IsZero() {
		t.Errorf("Session with nil stateTracker must return zero time, got %v", ts)
	}
}

func TestSession_LastObservedActivity_FalseAtInit(t *testing.T) {
	s := &Session{
		stateTracker: &StateTracker{
			lastChangeTime: time.Now(),
		},
	}
	ts, observed := s.LastObservedActivity()
	if observed {
		t.Fatal("Session whose tracker has never confirmed real activity must return observed=false")
	}
	if !ts.IsZero() {
		t.Errorf("must return zero time when !realActivityConfirmed so callers that miss the bool check get a sentinel, got %v", ts)
	}
}

func TestSession_LastObservedActivity_TrueAfterConfirmation(t *testing.T) {
	want := time.Now().Add(-3 * time.Minute)
	s := &Session{
		stateTracker: &StateTracker{
			lastChangeTime:        want,
			realActivityConfirmed: true,
		},
	}
	got, observed := s.LastObservedActivity()
	if !observed {
		t.Fatal("after the tracker flips realActivityConfirmed=true, LastObservedActivity must report observed=true")
	}
	if !got.Equal(want) {
		t.Errorf("LastObservedActivity returned %v, want %v", got, want)
	}
}
