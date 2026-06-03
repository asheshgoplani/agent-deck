package ui

import (
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// pickBadgeTime composes the 4-layer "most recent" formula that drives the
// session-row timestamp badge. Extracting it from renderSessionItem lets
// us pin the composition without faking unexported Home/Instance fields.
// Layers, in newest-wins order:
//
//	1. CreatedAt              (always set)
//	2. LastStartedAt          (zero if never started)
//	3. hookEvent.UpdatedAt    (nil if no hooks installed)
//	4. confirmedActivity      (bool gates use — false means "ignore")

func TestPickBadgeTime_PicksCreatedAtFloor(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	got := pickBadgeTime(created, time.Time{}, nil, time.Time{}, false)
	if !got.Equal(created) {
		t.Errorf("with only CreatedAt set, expected %v, got %v", created, got)
	}
}

func TestPickBadgeTime_LastStartedBeatsCreatedAt(t *testing.T) {
	created := time.Now().Add(-5 * 24 * time.Hour)
	started := time.Now().Add(-10 * time.Minute)
	got := pickBadgeTime(created, started, nil, time.Time{}, false)
	if !got.Equal(started) {
		t.Errorf("LastStartedAt should beat older CreatedAt, expected %v, got %v", started, got)
	}
}

func TestPickBadgeTime_HookEventBeatsLifecycle(t *testing.T) {
	created := time.Now().Add(-5 * 24 * time.Hour)
	started := time.Now().Add(-2 * time.Hour)
	hookAt := time.Now().Add(-30 * time.Second)
	hook := &session.HookStatus{UpdatedAt: hookAt}

	got := pickBadgeTime(created, started, hook, time.Time{}, false)
	if !got.Equal(hookAt) {
		t.Errorf("newer hook event should beat lifecycle floor, expected %v, got %v", hookAt, got)
	}
}

func TestPickBadgeTime_NilHookIgnored(t *testing.T) {
	created := time.Now().Add(-2 * time.Hour)
	started := time.Now().Add(-1 * time.Hour)
	got := pickBadgeTime(created, started, nil, time.Time{}, false)
	if !got.Equal(started) {
		t.Errorf("nil hookEvent must be ignored, expected %v, got %v", started, got)
	}
}

func TestPickBadgeTime_StaleHookIgnored(t *testing.T) {
	// A hook event from a previous process can be stale relative to the
	// lifecycle floor (session restarted after the hook). Older events
	// must not pull the badge backwards.
	created := time.Now().Add(-5 * 24 * time.Hour)
	started := time.Now().Add(-5 * time.Minute)
	staleHook := &session.HookStatus{UpdatedAt: time.Now().Add(-3 * time.Hour)}

	got := pickBadgeTime(created, started, staleHook, time.Time{}, false)
	if !got.Equal(started) {
		t.Errorf("stale hook older than LastStartedAt must not win, expected %v, got %v", started, got)
	}
}

func TestPickBadgeTime_ConfirmedActivityBeatsHook(t *testing.T) {
	created := time.Now().Add(-5 * 24 * time.Hour)
	started := time.Now().Add(-2 * time.Hour)
	hook := &session.HookStatus{UpdatedAt: time.Now().Add(-1 * time.Minute)}
	confirmed := time.Now().Add(-3 * time.Second)

	got := pickBadgeTime(created, started, hook, confirmed, true)
	if !got.Equal(confirmed) {
		t.Errorf("newer tmux-confirmed activity should beat hook event, expected %v, got %v", confirmed, got)
	}
}

func TestPickBadgeTime_ConfirmedActivityBeatsLifecycleWithoutHooks(t *testing.T) {
	// Production path for users who haven't installed agent-deck hooks:
	// no hook event is ever present, the tmux confirmed-activity layer is
	// the only thing that can move the badge past LastStartedAt. Covered
	// transitively by other cases (confirmed-beats-hook + hook-beats-
	// lifecycle), but this is *the* hot path so it deserves a direct pin.
	created := time.Now().Add(-5 * 24 * time.Hour)
	started := time.Now().Add(-2 * time.Hour)
	confirmed := time.Now().Add(-15 * time.Second)

	got := pickBadgeTime(created, started, nil, confirmed, true)
	if !got.Equal(confirmed) {
		t.Errorf("with no hook installed, tmux confirmed activity should beat LastStartedAt, "+
			"expected %v, got %v", confirmed, got)
	}
}

func TestPickBadgeTime_UnobservedConfirmedIgnored(t *testing.T) {
	// When the tracker has never observed real activity, the confirmed
	// timestamp is meaningless (would be tracker-init time.Now). The bool
	// gates use — false must cause the value to be ignored even if it's
	// "newer" than everything else.
	created := time.Now().Add(-2 * time.Hour)
	started := time.Now().Add(-1 * time.Hour)
	confirmedButUnobserved := time.Now() // would dominate if not gated

	got := pickBadgeTime(created, started, nil, confirmedButUnobserved, false)
	if !got.Equal(started) {
		t.Fatalf("confirmedObserved=false must cause the timestamp to be ignored, "+
			"expected %v (LastStartedAt), got %v", started, got)
	}
}

func TestPickBadgeTime_AllLayersZero(t *testing.T) {
	got := pickBadgeTime(time.Time{}, time.Time{}, nil, time.Time{}, false)
	if !got.IsZero() {
		t.Errorf("with no signals at all, the function must return the zero time, got %v", got)
	}
}
