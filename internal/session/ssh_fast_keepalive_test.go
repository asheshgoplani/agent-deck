package session

import "testing"

// L2 (round 2): the --stream-remotes follow of a remote's status gets the
// voice relay's keepalive (2 s x 3, connect 3 s), so a dead link is reported
// (session.link down) in about 6 s instead of 45-60 s.
func TestNegotiatedSSHArgs_FastKeepalive(t *testing.T) {
	r := NewSSHRunner("box", RemoteConfig{Host: "user@box"})
	slow := r.negotiatedSSHArgs("agent-deck events follow")
	if optValue(slow, "ServerAliveInterval") != "15" {
		t.Fatalf("default follow keepalive changed: %v", slow)
	}
	fast := r.WithFastKeepalive().negotiatedSSHArgs("agent-deck events follow")
	if got := optValue(fast, "ServerAliveInterval"); got != "2" {
		t.Errorf("ServerAliveInterval = %q, want 2", got)
	}
	if got := optValue(fast, "ServerAliveCountMax"); got != "3" {
		t.Errorf("ServerAliveCountMax = %q, want 3", got)
	}
	if got := optValue(fast, "ConnectTimeout"); got != "3" {
		t.Errorf("ConnectTimeout = %q, want 3", got)
	}
	if fast[len(fast)-2] != "user@box" {
		t.Errorf("host not right before the command: %v", fast)
	}
	if !r.WithFastKeepalive().FastKeepalive() || r.FastKeepalive() {
		t.Error("WithFastKeepalive must return a copy with the option set and leave the original as it was")
	}
}
