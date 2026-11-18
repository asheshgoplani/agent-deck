package session

import (
	"strings"
	"testing"
)

// optValue is the value ssh uses for option name: the first -o name=value.
func optValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-o" && strings.HasPrefix(args[i+1], name+"=") {
			return strings.TrimPrefix(args[i+1], name+"=")
		}
	}
	return ""
}

// S1 (rework round 1): the voice relay's link notices a dead route within
// seconds (ssh uses the first value of an option), and a blackholed connect
// gives up fast, so a remote is right again soon after the link returns.
func TestStdinStreamSSHArgs_FastKeepalive(t *testing.T) {
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box"})
	args := r.stdinStreamSSHArgs("agent-deck voice mirror-in")
	if got := optValue(args, "ServerAliveInterval"); got != "2" {
		t.Errorf("ServerAliveInterval = %q, want 2 (the shared 15 s takes 45 s to notice a dead link)", got)
	}
	if got := optValue(args, "ServerAliveCountMax"); got != "3" {
		t.Errorf("ServerAliveCountMax = %q, want 3", got)
	}
	if got := optValue(args, "ConnectTimeout"); got != "3" {
		t.Errorf("ConnectTimeout = %q, want 3", got)
	}
	if optValue(args, "ControlPath") != "none" {
		t.Errorf("relay link must own its connection: %v", args)
	}
	if args[len(args)-2] != "user@box" {
		t.Errorf("host not right before the command: %v", args)
	}
}
