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
