package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestSummaryBucket_StartingAndQueuedCountAsIdle(t *testing.T) {
	for st, want := range map[session.Status]session.Status{
		session.StatusRunning:  session.StatusRunning,
		session.StatusWaiting:  session.StatusWaiting,
		session.StatusIdle:     session.StatusIdle,
		session.StatusStopped:  session.StatusStopped,
		session.StatusError:    session.StatusError,
		session.StatusStarting: session.StatusIdle,
		session.StatusQueued:   session.StatusIdle,
		session.Status(""):     session.StatusIdle,
	} {
		if got := summaryBucket(st); got != want {
			t.Errorf("summaryBucket(%q) = %q, want %q", st, got, want)
		}
	}
}
