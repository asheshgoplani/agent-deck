package session

import "testing"

func TestSummaryBucket_StartingQueuedAndUnknownCountAsIdle(t *testing.T) {
	for st, want := range map[Status]Status{
		StatusRunning: StatusRunning, StatusWaiting: StatusWaiting, StatusIdle: StatusIdle,
		StatusStopped: StatusStopped, StatusError: StatusError,
		StatusStarting: StatusIdle, StatusQueued: StatusIdle, Status("bogus"): StatusIdle,
	} {
		if got := SummaryBucket(st); got != want {
			t.Errorf("SummaryBucket(%q) = %q, want %q", st, got, want)
		}
	}
}
