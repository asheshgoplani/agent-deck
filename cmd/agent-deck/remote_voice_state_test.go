package main

import (
	"strings"
	"testing"
)

// Only the read-only `voice state [--json]` travels to a remote.
func TestRemoteCommandArgs_VoiceState(t *testing.T) {
	for _, ok := range [][]string{{"voice", "state"}, {"voice", "state", "--json"}} {
		got, err := remoteCommandArgs(ok)
		if err != nil || strings.Join(got, " ") != strings.Join(ok, " ") {
			t.Errorf("%v: got %v, %v", ok, got, err)
		}
	}
	for _, bad := range [][]string{{"voice"}, {"voice", "relay", "--to", "x", "--self", "y"}, {"voice", "mirror-in"},
		{"voice", "result-publish"}, {"voice", "state", "--dir", "/tmp"}} {
		if _, err := remoteCommandArgs(bad); err == nil {
			t.Errorf("%v forwarded", bad)
		}
	}
}
