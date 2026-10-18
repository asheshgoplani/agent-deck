package main

import "testing"

// Only a forwarded command that reads stdin gets the controller's stdin;
// everything else hands ssh /dev/null (a terminal stdin it never reads made
// every successful `remote <host> ...` exit 1 with ErrWaitDelay).
func TestRemoteForwardNeedsStdin(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"session", "send", "s1", "--message-file", "-"}, true},
		{[]string{"launch", "--message-file", "-", "/repo"}, true},
		{[]string{"session", "image-upload", "s1", "--name", "a.png", "--json"}, true},
		{[]string{"session", "annotate", "s1", "--note-stdin"}, true},
		{[]string{"session", "send", "s1", "hello"}, false},
		{[]string{"session", "output", "s1", "--json"}, false},
		{[]string{"list", "--json"}, false},
		{[]string{"add", "/repo", "-t", "x"}, false},
		{[]string{"session", "annotate", "s1", "--outcome", "worked"}, false},
		{[]string{"session", "send", "s1", "--", "--message-file", "-"}, false},
		{[]string{"worktree", "cleanup", "--force"}, true},
		{[]string{"worktree", "cleanup"}, false},
		{[]string{"worktree", "list", "--json"}, false},
		{[]string{"session", "context", "s1", "--verify"}, true},
		{[]string{"session", "context", "s1", "--verify", "--yes"}, false},
		{[]string{"session", "context", "s1", "--json"}, false},
	}
	for _, tc := range cases {
		if got := remoteForwardNeedsStdin(tc.args); got != tc.want {
			t.Errorf("remoteForwardNeedsStdin(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
	// Whatever remoteMessageInput forwards for a stdin message keeps it.
	forwarded, _, closeInput, err := remoteMessageInput([]string{"session", "send", "s1", "--message-file=-"})
	defer closeInput()
	if err != nil || !remoteForwardNeedsStdin(forwarded) {
		t.Fatalf("forwarded %v (err %v) must keep stdin", forwarded, err)
	}
}
