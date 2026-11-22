package core

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// group.list counts the five buckets every status summary shares
// (session.SummaryBucket): starting and queued count as idle, so a group's
// buckets add up to its session count. They used to fall into no bucket.
func TestGroupStatusCountsStartingAndQueuedAsIdle(t *testing.T) {
	var sessions []*session.Instance
	for _, st := range []session.Status{
		session.StatusRunning, session.StatusWaiting, session.StatusIdle,
		session.StatusStarting, session.StatusQueued, session.StatusStopped, session.StatusError,
	} {
		sessions = append(sessions, &session.Instance{Status: st})
	}
	got := groupStatus(sessions)
	want := GroupStatus{Running: 1, Waiting: 1, Idle: 3, Stopped: 1, Error: 1}
	if got != want {
		t.Fatalf("groupStatus = %+v, want %+v", got, want)
	}
	if sum := got.Running + got.Waiting + got.Idle + got.Stopped + got.Error; sum != len(sessions) {
		t.Fatalf("buckets sum to %d for %d sessions", sum, len(sessions))
	}
}
