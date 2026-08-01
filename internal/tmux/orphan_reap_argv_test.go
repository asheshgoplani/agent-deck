package tmux

import (
	"strings"
	"testing"
)

// nulArgv builds a /proc/<pid>/cmdline payload: NUL-separated, NUL-terminated.
func nulArgv(fields ...string) string {
	return strings.Join(fields, "\x00") + "\x00"
}

// TestIsReapableOneShotArgv pins WHICH tmux clients the orphan sweep may kill.
//
// Matching on comm alone (a tmux client) plus SessionPrefix (argv mentions an
// agent-deck session) is too broad in two directions, both of which SIGKILL
// something the user cares about:
//
//   - An INTERACTIVE client. A user who runs `tmux attach-session -t
//     agentdeck_foo` by hand gets a client whose parent is their shell, not
//     agent-deck — and isControlClientOrphan treats any non-agent-deck parent
//     as an orphan even while that parent is alive. Verified on a live host:
//     such a client is selected for the kill. The session survives on the
//     server, but the user is detached mid-interaction. Control-mode clients
//     are already covered by reapStaleControlClients, which filters on
//     client_control_mode and does not have this blind spot, so attach clients
//     of either kind have no business in this sweep.
//
//   - A NASCENT SERVER. tmux renames the client to "tmux: client" before it
//     forks the server (client.c calls proc_start("client") ahead of
//     client_connect), and the forked server inherits that comm until its own
//     proc_start("server") runs after daemon(). In that window a starting
//     server presents comm "tmux: client" AND the creating client's argv
//     (servers keep it — confirmed live: a running server shows
//     `new-session -d -s agentdeck_…`) AND a parent that is either tmux or
//     init. All three kill conditions hold. The window is sub-millisecond, but
//     agent-deck starts one server per session during restore, concurrently
//     with the first Connect that fires the sweep.
//
// Constraining the sweep to the one-shot cadence verbs it documents as its
// target set closes both: neither attach-session nor new-session is a cadence
// query, so neither is reapable regardless of comm or parentage.
func TestIsReapableOneShotArgv(t *testing.T) {
	tests := []struct {
		name string
		argv []string
		want bool
	}{
		// The 2026-08-01 orphan: a chained status-bar set-option batch.
		{
			"chained set-option batch",
			[]string{"tmux", "set-option", "-t", "agentdeck_npm_a0e435da", "@agentdeck_project_name", "workspace", ";", "set-option", "-t", "agentdeck_npm_a0e435da", "set-titles", "on"},
			true,
		},
		{"list-clients", []string{"tmux", "list-clients", "-F", "#{session_name}"}, true},
		{"display-message", []string{"tmux", "display-message", "-p", "-t", "agentdeck_x", "#{pane_pid}"}, true},
		{"list-panes with socket flag", []string{"tmux", "-L", "ad-sock", "list-panes", "-t", "agentdeck_x"}, true},
		{"has-session", []string{"tmux", "has-session", "-t", "agentdeck_x"}, true},
		{"kill-session mutation", []string{"tmux", "kill-session", "-t", "agentdeck_x"}, true},

		// Interactive and control-mode attaches: never this sweep's business.
		{"interactive attach", []string{"tmux", "attach-session", "-t", "agentdeck_dev_d83a1787"}, false},
		{"control-mode attach", []string{"tmux", "-C", "attach-session", "-t", "agentdeck_npm_a0e435da"}, false},

		// A server being born carries the creating client's argv.
		{"new-session", []string{"tmux", "-L", "ad-sock", "new-session", "-d", "-s", "agentdeck_x", "-c", "/home/u"}, false},

		// A chained command that both attaches and sets an option must lose:
		// the deny side is fail-safe, the allow side is an optimisation.
		{"attach chained with set-option", []string{"tmux", "attach-session", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, false},

		// Verbs outside the cadence set are not reapable even though they are
		// legitimate tmux commands against an agent-deck session.
		{"pipe-pane", []string{"tmux", "pipe-pane", "-t", "agentdeck_x", "cat > /tmp/log"}, false},
		{"send-keys", []string{"tmux", "send-keys", "-t", "agentdeck_x", "hello", "Enter"}, false},
		{"no verb at all", []string{"tmux"}, false},
		{"empty", []string{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isReapableOneShotArgv(nulArgv(tc.argv...))
			if got != tc.want {
				t.Errorf("isReapableOneShotArgv(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}
