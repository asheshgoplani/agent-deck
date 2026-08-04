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

		// A tmux invoked under a longer argv[0] still names its role as long
		// as "<progname>: <role>" survives the 15-char comm limit. The role
		// token is what authorises the kill, not the program name, so these
		// are reapable — see tmuxCommRole.
		{"five-char progname keeps role", "tmuxx: client", true},
		{"five-char progname server", "tmuxx: server", false},

		// Measured, not assumed: invoking /usr/bin/tmux through a symlink
		// named "tmux-3.5a" yields exactly this comm — the role token is gone
		// entirely, not truncated to a prefix. tmux formats "<progname>:
		// <role> (<socket>)", the kernel caps comm at 15 chars, and tmux then
		// cuts the result back to its last space, which for a 9-char progname
		// lands immediately after the colon.
		//
		// A SERVER under the same binary produces the identical string, so
		// nothing here can tell the two apart. Refusing to match is the only
		// safe answer: a false positive SIGKILLs a server and takes every
		// session it hosts with it. agent-deck never spawns tmux this way
		// (every call site is exec.Command("tmux", …), so argv[0] is the
		// literal "tmux"), so no orphan of its own can land in this state.
		{"renamed binary loses role", "tmux-3.5a:", false},
		{"renamed binary loses role, newline", "tmux-3.5a:\n", false},
		{"colon with no role", "tmux:", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isReapableTmuxClientComm(tc.comm); got != tc.want {
				t.Errorf("isReapableTmuxClientComm(%q) = %v, want %v", tc.comm, got, tc.want)
			}
		})
	}
}

// TestIsTruncatedTmuxComm pins which comm values the sweep must REPORT rather
// than silently pass over.
//
// The original defect was not that the sweep killed the wrong thing — it was
// that it killed nothing at all and said nothing about it, so two orphans burned
// a core each for 14 hours with a healthy TUI running beside them. A comm whose
// role token was lost to truncation puts the sweep back in exactly that state
// for the affected process: it cannot be classified, so it cannot be reaped.
// That is the right call, but it must be visible, not silent.
func TestIsTruncatedTmuxComm(t *testing.T) {
	tests := []struct {
		name string
		comm string
		want bool
	}{
		// Role lost to truncation — unclassifiable, so worth reporting.
		{"renamed binary", "tmux-3.5a:", true},
		{"renamed binary with newline", "tmux-3.5a:\n", true},
		{"bare colon", "tmux:", true},

		// Classifiable: handled normally, nothing to report.
		{"client", "tmux: client", false},
		{"server", "tmux: server", false},
		{"bare tmux", "tmux", false},
		{"long progname keeping role", "tmuxx: client", false},

		// Not tmux at all — the sweep skips these silently and always did.
		{"bash", "bash", false},
		{"empty", "", false},
		{"unrelated with colon", "systemd:", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isTruncatedTmuxComm(tc.comm); got != tc.want {
				t.Errorf("isTruncatedTmuxComm(%q) = %v, want %v", tc.comm, got, tc.want)
			}
		})
	}
}
