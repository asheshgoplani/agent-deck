package session

import "github.com/asheshgoplani/agent-deck/internal/events"

// RemovedSessionCleanup reports what CleanupRemovedSession did. Every part is
// best effort: callers log the errors but never fail the remove on them.
type RemovedSessionCleanup struct {
	InboxSwept     int
	InboxErr       error
	NotifyStateErr error
}

// CleanupRemovedSession is the cleanup every path that deletes a session row
// runs once the row is gone (CLI remove, session remove, bulk remove,
// worktree finish, conductor teardown, TUI and web delete or finish):
//
//  1. drop the removed session's lines from every conductor inbox (#910);
//  2. drop its transition-notify-state record (#910, #1576);
//  3. publish macapp.session.closed {reason:"removed"} on profile's bus, so a
//     following Mac app forgets that session's Browser state.
//
// Call it once per deleted row, right after that row's delete succeeded, so
// a later failure in a bulk remove still leaves the rows already deleted
// announced.
//
// A delete that can still be undone (TUI and web delete) publishes with
// PublishSessionClosed at delete time and runs SweepRemovedSession only once
// the delete is final, so an undone delete keeps its pending inbox events.
func CleanupRemovedSession(profile, id string) RemovedSessionCleanup {
	r := SweepRemovedSession(id)
	PublishSessionClosed(profile, id, SessionClosedRemoved)
	return r
}

// SweepRemovedSession is the sweep half of CleanupRemovedSession: the inbox
// and notify-state cleanup (#910) for a session whose delete is final.
func SweepRemovedSession(id string) RemovedSessionCleanup {
	var r RemovedSessionCleanup
	r.InboxSwept, r.InboxErr = SweepInboxesForChildSession(id)
	_, r.NotifyStateErr = RemoveNotifyStateRecord(id)
	return r
}

// Reasons carried by macapp.session.closed.
const (
	// SessionClosedRemoved: the session row was deleted.
	SessionClosedRemoved = "removed"
	// SessionClosedMoved: the session moved to another profile; it is gone
	// from this profile's bus, and the app sees it again on the target's.
	SessionClosedMoved = "moved"
)

// PublishSessionClosed publishes macapp.session.closed {reason} for id on
// profile's events bus. Best effort: the tap never blocks or fails the caller,
// and a disabled bus publishes nothing.
func PublishSessionClosed(profile, id, reason string) {
	if id == "" {
		return
	}
	if profile == "" {
		profile = DefaultProfile
	}
	sessionClosedPublisher(profile, events.KindMacappSessionClosed, id, map[string]string{"reason": reason})
}

var sessionClosedPublisher = events.PublishProfile

// SetSessionClosedPublisherForTest replaces the macapp.session.closed
// publisher (tests only) and returns a func that restores it.
func SetSessionClosedPublisherForTest(fn func(profile, kind, sessionID string, data any)) (restore func()) {
	prev := sessionClosedPublisher
	sessionClosedPublisher = fn
	return func() { sessionClosedPublisher = prev }
}
