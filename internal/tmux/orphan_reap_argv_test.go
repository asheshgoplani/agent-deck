package tmux

import (
	"strings"
	"testing"
)

// nulArgv builds a /proc/<pid>/cmdline payload: NUL-separated, NUL-terminated.
func nulArgv(fields ...string) string {
	return strings.Join(fields, "\x00") + "\x00"
}

// TestIsKnownCadenceArgv pins the SCOPE this sweep targets: the one-shot
// poll/query/status commands agent-deck fires on a cadence. It is
// deliberately NOT a safety test — isKnownCadenceArgv is an allowlist that
// only ever narrows the sweep, never a denylist that could be trusted to keep
// something out. See TestIsLiveTmuxClientOrServer_ChainedAliasRepro for the
// actual safety guarantee and why it cannot live in this function.
//
// History: this file used to pin isReapableOneShotArgv, which paired this
// same allowlist with a denylist (neverReapVerbs) matching only the literal
// strings "attach-session" / "new-session" and trusted that pairing as the
// safety boundary. It was wrong to: tmux resolves command names by
// unambiguous prefix, so "attach" (a real tmux alias), "attac", "att", "at",
// even bare "a" all invoke attach-session unmodified (verified live on tmux
// 3.7b — `tmux -C a -t sess` attaches). A chained
// `tmux attach -t agentdeck_x \; set-option status on` cleared the old
// denylist (wrong spelling) while "set-option" satisfied this allowlist, so
// isReapableOneShotArgv ruled the whole line reapable with a live client
// behind it. isKnownCadenceArgv keeps only the allowlist half — a miss here
// means "skip", a hit means only "maybe, ask the server" — and
// isLiveTmuxClientOrServer supplies the actual guarantee by asking the tmux
// server itself, which cannot be abbreviated around.
func TestIsKnownCadenceArgv(t *testing.T) {
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

		// A cadence verb chained after an attach still matches the allowlist —
		// that is expected and fine, because this function no longer decides
		// whether the process is safe to kill. isLiveTmuxClientOrServer does.
		{"attach chained with set-option", []string{"tmux", "attach-session", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, true},
		{"alias attach chained with set-option", []string{"tmux", "attach", "-t", "agentdeck_x", ";", "set-option", "status", "on"}, true},

		// Verbs outside the cadence set are not reapable even though they are
		// legitimate tmux commands against an agent-deck session.
		{"bare attach-session, no cadence verb", []string{"tmux", "attach-session", "-t", "agentdeck_dev_d83a1787"}, false},
		{"pipe-pane", []string{"tmux", "pipe-pane", "-t", "agentdeck_x", "cat > /tmp/log"}, false},
		{"send-keys", []string{"tmux", "send-keys", "-t", "agentdeck_x", "hello", "Enter"}, false},
		{"no verb at all", []string{"tmux"}, false},
		{"empty", []string{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := isKnownCadenceArgv(nulArgv(tc.argv...))
			if got != tc.want {
				t.Errorf("isKnownCadenceArgv(%q) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}
