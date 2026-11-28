package main

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// L2 (round 2): the runner agent-deck web uses for --stream-remotes has the
// fast keepalive.
func TestStreamRemoteRunner_FastKeepalive(t *testing.T) {
	r := streamRemoteRunner("box", session.RemoteConfig{Host: "user@box"})
	if !r.FastKeepalive() {
		t.Fatal("the --stream-remotes follow uses the shared 15 s x 3 keepalive: a dead link is said after ~60 s")
	}
}
