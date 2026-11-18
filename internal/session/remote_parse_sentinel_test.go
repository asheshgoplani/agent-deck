package session

import (
	"errors"
	"testing"
)

// needs-me tells a remote that answered garbage ("bad-reply") from one it
// could not reach by this sentinel, not by matching the message text.
func TestParseRemoteSessionsGarbageIsErrRemoteSessionsParse(t *testing.T) {
	_, err := parseRemoteSessions([]byte("[{not json"))
	if !errors.Is(err, ErrRemoteSessionsParse) {
		t.Fatalf("err = %v, want errors.Is ErrRemoteSessionsParse", err)
	}
	if _, err := parseRemoteSessions([]byte("No sessions found")); err != nil {
		t.Fatalf("non-JSON listing is an empty list, got %v", err)
	}
}

// A remote's list --json waiting_since reaches RemoteSessionInfo, so
// needs-me can order remote sessions by how long they waited (PR 3170 F1).
func TestParseRemoteSessionsCarriesWaitingSince(t *testing.T) {
	got, err := parseRemoteSessions([]byte(`[{"id":"r1","title":"t","status":"waiting","created_at":"2026-11-17T20:00:00Z","waiting_since":"2026-11-17T21:30:00Z"}]`))
	if err != nil || len(got) != 1 {
		t.Fatalf("parse = %v %v", got, err)
	}
	if got[0].WaitingSince != "2026-11-17T21:30:00Z" {
		t.Fatalf("WaitingSince = %q", got[0].WaitingSince)
	}
}
