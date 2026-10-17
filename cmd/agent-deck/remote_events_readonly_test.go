package main

import (
	"reflect"
	"testing"
)

// Integration of #2550 (events follow --read-only) with core-1-5 (remote
// events follow forwarding): the observer flag is forwarded to the remote
// unchanged, and like the other boolean flags it takes no inline value.
func TestRemoteEventsFollowForwardsReadOnly(t *testing.T) {
	for _, args := range [][]string{
		{"events", "follow", "--jsonl", "--read-only"},
		{"events", "follow", "--read-only", "--jsonl", "--since", "123", "--kind", "session.status"},
		{"events", "follow", "-read-only", "--json"},
	} {
		got, err := remoteCommandArgs(args)
		if err != nil || !reflect.DeepEqual(got, args) {
			t.Fatalf("forward %q: %q %v", args, got, err)
		}
		if !isRemoteReadCapability(args) || !isRemoteFollow(args) {
			t.Fatalf("%q is not routed as a remote follow stream", args)
		}
	}
	for _, args := range [][]string{
		{"events", "follow", "--jsonl", "--read-only=true"},
		{"events", "follow", "--jsonl", "--read-write"},
		{"events", "stats", "--read-only"},
	} {
		if _, err := remoteCommandArgs(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
