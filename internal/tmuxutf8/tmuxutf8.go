// Package tmuxutf8 owns one decision: whether a tmux command line agent-deck
// builds carries tmux's global `-u` flag.
//
// # Why this exists (issue #1867)
//
// tmux decides a CLIENT's charset from that client's own LC_ALL / LC_CTYPE /
// LANG. When none of them contain "UTF-8"/"UTF8", the client is not marked
// CLIENT_UTF8 and the server runs every message it prints to that client
// through utf8_sanitize(), which rewrites each non-ASCII byte to "_". The
// server's own buffers stay correct — only the bytes handed to the non-UTF-8
// client are rewritten — so the damage is invisible until something parses
// them.
//
// agent-deck parses them constantly. `list-panes -F '#{pane_title}'` is how
// RefreshPaneInfoCache learns that Claude is mid-tool-call: the braille
// spinner (U+2800–U+28FF) in the pane title is the only reliable "running"
// signal there is. Downgraded to "_", AnalyzePaneTitle returns
// TitleStateUnknown, the title fast path in (*Session).GetStatus never fires,
// and a plainly-running session is reported idle. `capture-pane -p` loses the
// box-drawing and "⏵⏵" indicators the content scan looks for in the same way.
//
// This is not an exotic configuration. systemd (--user included) and launchd
// start services with no LANG/LC_* at all, and so does a bare container — so
// the notify-daemon, the conductor heartbeat, the web daemon and CI are all in
// the affected state without anyone having configured anything. Measured on
// tmux 3.2a / 3.4 / 3.5a / 3.6b / 3.7b across five Linux distros.
//
// # Why -u and not a locale in the child environment
//
// The obvious alternative is to hand the tmux client `LC_ALL=C.UTF-8` via
// cmd.Env. It is rejected:
//
//   - `tmux new-session` copies the CLIENT's environment wholesale into the new
//     session's environment, which tmux then hands to every process it spawns
//     in that session. A forced LC_ALL would therefore silently relocalise the
//     agent itself (message catalogues, collation, date formatting) — a much
//     wider blast radius than the parsing bug being fixed.
//   - It would fight internal/childenv, which is the sanctioned single filter
//     for the environment of processes agent-deck spawns. childenv deliberately
//     subtracts variables (TELEGRAM_*, an inherited CLAUDE_CONFIG_DIR); adding a
//     locale there would push a value into the agent's env from a package whose
//     whole contract is "never let the parent's pollution through".
//   - `-u` is exactly scoped: it sets CLIENT_UTF8 on this one client and changes
//     nothing else, including nothing the server keeps.
//
// # Version floor
//
// `-u` is one of tmux's oldest global flags — it is in the `usage: tmux
// [-2CDlNuVv] …` line of every tmux from 1.0 through 3.7b, well below the
// tmux 3.2+ floor install.sh's shipped tmux.conf already assumes. A tmux that
// did not accept it would reject the flag with a usage error on stderr and a
// non-zero exit, i.e. fail loudly and uniformly on the very first invocation
// rather than corrupt anything.
package tmuxutf8

// Flag is tmux's global "assume the terminal supports UTF-8" option. It must
// appear before the subcommand, alongside -L/-S, not after it.
const Flag = "-u"

// Prepend returns a fresh argv with Flag in front of args. It is the only
// sanctioned way to add the flag, so `grep -r tmuxutf8` enumerates every tmux
// command line in the codebase that is UTF-8 safe.
//
// The caller's slice is never mutated or aliased: agent-deck's tmux argv
// builders run on hot status-poll paths and share their input slices.
//
// Prepend is idempotent for an argv that already leads with Flag, so wrapping a
// pre-built command line twice cannot produce `tmux -u -u …`.
func Prepend(args []string) []string {
	if len(args) > 0 && args[0] == Flag {
		out := make([]string, len(args))
		copy(out, args)
		return out
	}
	if len(args) == int(^uint(0)>>1) {
		panic("tmux argument list too large")
	}
	out := make([]string, 0, len(args)+1)
	out = append(out, Flag)
	return append(out, args...)
}

// HasFlag reports whether argv carries Flag in its GLOBAL flag position, i.e.
// as the leading token. It deliberately does not scan the whole slice: `-u` is
// also a per-subcommand option with an unrelated meaning ("unset") on
// set-option and set-environment, and matching those would report UTF-8 safety
// that is not there.
func HasFlag(args []string) bool {
	return len(args) > 0 && args[0] == Flag
}
