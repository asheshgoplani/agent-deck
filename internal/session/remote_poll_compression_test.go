package session

import (
	"strings"
	"testing"
)

// A poll of a large fleet (~650 sessions) is a ~645KB JSON listing that
// compresses about ten-fold. Without ssh compression a slow or degraded link
// spends the whole 30s poll bound moving it and the host shows
// "unreachable: timeout".
func TestSSHRunnerBaseArgsCompressPollPayload(t *testing.T) {
	r := NewSSHRunner("deckbox", RemoteConfig{Host: "u@box"})
	joined := " " + strings.Join(r.sshBaseArgs("agent-deck list --json"), " ") + " "
	if !strings.Contains(joined, " -o Compression=yes ") {
		t.Fatalf("remote ssh args must enable Compression=yes, got: %s", joined)
	}
}
