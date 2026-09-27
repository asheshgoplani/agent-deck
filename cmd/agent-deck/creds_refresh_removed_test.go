package main

import (
	"strings"
	"testing"
)

// An old systemd unit still runs `agent-deck creds-refresh ...` with
// Restart=always, so the removal stub must exit 0, point at the replacement
// and never touch the user's files.
func TestCredsRefreshRemovedStubExitsZero(t *testing.T) {
	home := t.TempDir()
	out, err := runIssue2025Helper(t, home, []string{"creds-refresh", "--interval", "25m", "--threshold", "20m"})
	if err != nil {
		t.Fatalf("creds-refresh stub exited non-zero: %v\n%s", err, out)
	}
	wants := []string{
		"creds-refresh was removed in " + credsRefreshRemovedIn,
		"systemctl --user disable --now agent-deck-creds-refresh",
		"claude setup-token",
	}
	for _, want := range wants {
		if !strings.Contains(string(out), want) {
			t.Fatalf("stub output missing %q:\n%s", want, out)
		}
	}
	assertNoFiles(t, home)
}

func TestCredsRefreshIsNotARegisteredCommand(t *testing.T) {
	if commandRegistry["creds-refresh"] {
		t.Fatal("creds-refresh is removed; only the pre-dispatch stub may answer it")
	}
}
