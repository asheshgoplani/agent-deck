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
// TestNeverReapVerb pins the spelling rule the deny-list has to survive.
//
// tmux does not require the full verb. cmd_find() resolves a command name three
// ways, and the deny-list is only safe if it recognises all of them:
//
//   - the full name — `attach-session`;
//   - the alias, matched whole — `attach`, `new` (cmd-attach-session.c and
//     cmd-new-session.c both carry a .alias field; they are entries in tmux's
//     own command table, not documentation shorthand);
//   - ANY unambiguous prefix of the full name — verified against tmux 3.0a:
//     `attach-sess`, `att` and even `a` all reach attach-session, and
//     `new-sess -d -s x` creates a session. A prefix of the *alias* does not
//     resolve (`lscl` is rejected while `lsc` works), so an exact-match test is
//     enough for the alias but not for the name.
//
// Prefixes are matched to the shortest form on purpose, including ones tmux
// itself would reject as ambiguous (`n` could be new-session, new-window,
// next-layout or next-window). Such a process dies on its own error rather than
// attaching, so denying it costs one skipped reap — the direction this sweep is
// required to fail in. `new-window`/`neww` are not denied: neither is a prefix
// or alias of new-session, and this list names the verbs whose death costs the
// user a live client or a session being born, not everything tmux can spell.
func TestNeverReapVerb(t *testing.T) {
	tests := []struct {
		field string
		want  bool
	}{
		{"attach-session", true},
		{"attach", true},
		{"attach-sess", true},
		{"att", true},
		{"a", true},
		{"new-session", true},
		{"new", true},
		{"new-sess", true},
		{"n", true},

		{"", false},
		{"attach-sessions", false},
		{"attaché", false},
		{"new-window", false},
		{"neww", false},
		{"next-window", false},
		{"set-option", false},
		{"list-clients", false},
		{"agentdeck_npm_a0e435da", false},
		{"-t", false},
	}

	for _, tc := range tests {
		t.Run(tc.field, func(t *testing.T) {
			if got := isNeverReapVerb(tc.field); got != tc.want {
				t.Errorf("isNeverReapVerb(%q) = %v, want %v", tc.field, got, tc.want)
			}
		})
	}
}

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

		// Same chain, spelled the way tmux also accepts it. A deny-list keyed on
		// the full verb alone lets every one of these through to the allow-list's
		// set-option and reaps a live interactive client (see TestNeverReapVerb).
		{"alias attach chained with set-option", []string{"tmux", "attach", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, false},
		{"alias new chained with set-option", []string{"tmux", "new", "-d", "-s", "agentdeck_x", ";", "set-option", "status", "on"}, false},
		{"prefix attach-sess chained with set-option", []string{"tmux", "attach-sess", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, false},
		{"prefix att chained with set-option", []string{"tmux", "att", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, false},
		{"prefix new-sess chained with set-option", []string{"tmux", "new-sess", "-d", "-s", "agentdeck_x", ";", "set-option", "status", "on"}, false},

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
