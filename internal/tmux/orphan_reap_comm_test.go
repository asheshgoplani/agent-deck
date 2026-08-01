package tmux

import "testing"

// TestIsReapableTmuxClientComm pins the /proc/<pid>/comm values that the
// orphaned-poll-client sweep must recognise as a tmux CLIENT.
//
// The 2026-08-01 incident: two orphaned clients — `tmux list-clients` and
// `tmux set-option -t agentdeck_npm_a0e435da …` — spun at ~98% CPU for 14
// hours (13h47m and 13h43m of CPU time, 1023/1022 open fds, nearly all
// anon_inode:[eventpoll]) while a healthy agent-deck TUI ran alongside them
// for 12.5 of those hours and never reaped either one.
//
// reapOrphanedPollClients filtered on `comm == "tmux"`, documented as "the
// server is 'tmux: server' and never matches". Half of that is right: the
// server does rename itself. What the filter missed is that tmux renames the
// CLIENT too — comm is "tmux: client", not "tmux". Measured on tmux 3.0a /
// Linux 5.4, where `pgrep -x tmux` matches zero processes on a host running
// twenty of them:
//
//	comm=[tmux: client]  argv=[tmux -C attach-session -t agentdeck_mvn_672de607]
//	comm=[tmux: server]  argv=[/usr/bin/tmux -L ad1031-2044680 new-session -d …]
//
// So the equality test never held for any process, the sweep was inert on
// every Linux host, and the orphans it exists to mop up survived indefinitely.
//
// Rejecting "tmux: server" is the load-bearing safety property here, not a
// nicety: the sweep SIGKILLs what it matches, and killing a server would
// destroy every session it hosts — the user's work, not a leaked query client.
func TestIsReapableTmuxClientComm(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want bool
	}{
		// The value actually observed for one-shot query/option clients on
		// Linux. This is the case the original filter missed.
		{"renamed client", "tmux: client", true},
		// Trailing newline: /proc/<pid>/comm always ends in one.
		{"renamed client with newline", "tmux: client\n", true},
		// Kept for platforms/versions that do not rename the client.
		{"bare tmux", "tmux", true},
		{"bare tmux with newline", "tmux\n", true},

		// Never reapable — killing a server destroys live user sessions.
		{"server", "tmux: server", false},
		{"server with newline", "tmux: server\n", false},

		// Unrelated binaries whose names merely start with "tmux".
		{"tmuxinator", "tmuxinator", false},
		{"tmuxp", "tmuxp", false},
		{"empty", "", false},
		{"unrelated", "bash", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReapableTmuxClientComm(tc.comm); got != tc.want {
				t.Errorf("isReapableTmuxClientComm(%q) = %v, want %v", tc.comm, got, tc.want)
			}
		})
	}
}
