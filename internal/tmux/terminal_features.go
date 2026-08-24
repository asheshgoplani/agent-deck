package tmux

import (
	"errors"
	"log/slog"
	"os/exec"
	"strings"
)

// agentDeckTerminalFeature is the single `terminal-features` entry agent-deck
// wants present on the tmux server: OSC 8 hyperlink tracking plus extended key
// reporting, for every terminal type. One entry is sufficient regardless of how
// many sessions, clients or agent-deck processes come and go — `terminal-features`
// is a SERVER option, shared by every session on the socket.
const agentDeckTerminalFeature = "*:hyperlinks:extkeys"

// terminalFeatureState is what the target tmux server currently reports for the
// server-wide `terminal-features` array.
//
// known is false when the array could not be read at all: there is no server
// yet (Session.Start emits its option chunks in the SAME tmux command that
// creates the session, so the read runs before the server exists), the server is
// wedged past tmuxPollTimeout, or tmux is too old to know the option.
type terminalFeatureState struct {
	values []string
	known  bool
}

// readTerminalFeatures reads the server-wide `terminal-features` array from the
// tmux server selected by socketName ("" = the user's default server).
//
// `show-options -sv <array>` prints one value per line, and it keeps real
// newlines even with no client attached (the no-client sanitiser documented at
// tmuxFieldSep rewrites control bytes in FORMAT output, not in show-options).
func readTerminalFeatures(socketName string) terminalFeatureState {
	out, err := runBoundedOutput(socketName, "show-options", "-sv", "terminal-features")
	return classifyTerminalFeatures(out, err)
}

// classifyTerminalFeatures turns a `show-options -sv terminal-features` result
// into a state. It is separate from the exec so the three cases that matter can
// be tested directly, the way parsePanePID's contract is.
//
// A clean exit with NO output is a real answer: the array is empty, which is
// what `set -s terminal-features ""` leaves behind. It must not be confused
// with "we could not read", because the two demand opposite actions — an empty
// array should be SET to our entry, while an unknown array must never be
// rewritten from nothing.
//
// The WaitDelay contract (see tmuxSubprocessWaitDelay) is where that
// distinction gets sharp. When cmd.Wait abandons the stdio goroutine after the
// process exited, whatever bytes arrived are still authoritative — but an EMPTY
// body under that error is indeterminate, not an empty array: the values may
// simply never have been copied. Treating it as empty would rewrite a
// (possibly large) user array down to our single entry. So it is unknown, and
// the next pass asks again.
func classifyTerminalFeatures(out []byte, err error) terminalFeatureState {
	body := strings.TrimRight(string(out), "\n")
	if err != nil {
		if !errors.Is(err, exec.ErrWaitDelay) || body == "" {
			return terminalFeatureState{known: false}
		}
	}
	if body == "" {
		return terminalFeatureState{known: true} // empty array, no values
	}
	return terminalFeatureState{values: strings.Split(body, "\n"), known: true}
}

// terminalFeatureArgsFor is the pure decision: given what the server currently
// holds, what (if anything) must be written so that agentDeckTerminalFeature is
// present EXACTLY ONCE. The returned chunk is ";"-prefixed so callers can chain
// it onto a set-option in the same tmux command; nil means "nothing to do".
//
// This is the #2061 fix. `set -as terminal-features ,<value>` appends a new
// array item every time it runs, with no membership check, and the option lives
// on the long-lived tmux SERVER — so every repeat of agent-deck's per-session
// setup (a new spawn, a fresh process, a TUI storage reload, a control-client
// respawn that lands another EnsureConfigured pass) added one more copy for the
// life of that server. Reporters measured 5,018 entries / 219.8 KB after weeks
// of uptime, and a live server at 36,355. tmux consults the array on terminal
// setup and on every capability lookup, and `*` matches every terminal, so the
// duplicates show up as progressive display corruption.
func terminalFeatureArgsFor(st terminalFeatureState) []string {
	if !st.known {
		// Nothing to compare against. Append once: every LATER pass reads the
		// array successfully and short-circuits, so this can add at most one
		// entry per server — the append is no longer per-spawn. `-q` keeps a
		// tmux too old for the option quiet, as before.
		return []string{";", "set", "-asq", "terminal-features", "," + agentDeckTerminalFeature}
	}

	desired, changed := planTerminalFeatures(st.values)
	if !changed {
		return nil
	}
	if !safeToRewriteTerminalFeatures(st.values) {
		// The array holds something we cannot express as one comma-joined
		// value, so rewriting it could corrupt or drop a user entry. Fall back
		// to the historical append — still bounded, because we only reach it
		// when our entry is absent.
		return []string{";", "set", "-asq", "terminal-features", "," + agentDeckTerminalFeature}
	}
	return []string{";", "set", "-sq", "terminal-features", strings.Join(desired, ",")}
}

// planTerminalFeatures returns the array agent-deck wants: every existing entry
// in its existing order, with agentDeckTerminalFeature present exactly once.
// changed reports whether that differs from current.
//
// Only OUR entry is ever collapsed. Duplicates of anything else are left alone:
// agent-deck did not create them, and a server option is shared with the user's
// own tmux config.
func planTerminalFeatures(current []string) (desired []string, changed bool) {
	desired = make([]string, 0, len(current)+1)
	seen := false
	for _, v := range current {
		if v == agentDeckTerminalFeature {
			if seen {
				changed = true
				continue
			}
			seen = true
		}
		desired = append(desired, v)
	}
	if !seen {
		desired = append(desired, agentDeckTerminalFeature)
		changed = true
	}
	return desired, changed
}

// safeToRewriteTerminalFeatures reports whether the array can be rewritten with
// a single `set -s terminal-features "a,b,c"` without losing information.
//
// A value carrying a comma would come back as two array items, and an empty
// value cannot be round-tripped at all. Neither is a shape tmux itself produces
// for this option (entries are `<term-glob>:<feature>[:<feature>…]`), so hitting
// either means our read is not the whole truth — leave the array alone.
func safeToRewriteTerminalFeatures(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) == "" || strings.Contains(v, ",") {
			return false
		}
	}
	return true
}

// terminalFeatureArgs returns the ";"-prefixed tmux argument chunk that makes
// agent-deck's terminal-features entry present exactly once on this session's
// server, or nil when there is nothing to do.
//
// Every pass asks the server, and deliberately caches nothing across passes.
// The obvious optimisation — remember per socket that this process already saw
// the entry, and skip the read — is wrong, and wrong in a way that is silent and
// permanent: a tmux socket NAME is not a tmux server IDENTITY. The server on
// `-L agent-deck` exits when its last session closes (or crashes), and the next
// session starts a brand-new server under that same name. A process-lifetime
// memo keyed on the socket would report "already settled" for the replacement
// server and never write the entry to it, so hyperlinks and extended keys would
// stay off there until agent-deck itself restarted. That is #2061's defect
// pointing the other way — writing unconditionally leaks, skipping on stale
// state never writes, and both fail silently. The read is one bounded
// `show-options` on a cold path (session creation, or the once-per-process
// deferred configuration pass), which is the honest price of being right.
func (s *Session) terminalFeatureArgs() []string {
	st := readTerminalFeatures(s.SocketName)
	args := terminalFeatureArgsFor(st)
	if st.known && len(args) > 0 {
		if dupes := countTerminalFeature(st.values) - 1; dupes > 0 {
			// Collapsing an already-inflated server. Worth a log line: this is
			// the state that produced the reported display corruption, and the
			// count is the only evidence a support thread can quote.
			statusLog.Info("terminal_features_collapsed",
				slog.String("socket", s.SocketName),
				slog.Int("duplicates_removed", dupes),
				slog.Int("entries_before", len(st.values)))
		}
	}
	return args
}

// countTerminalFeature counts occurrences of agent-deck's entry in values.
func countTerminalFeature(values []string) int {
	n := 0
	for _, v := range values {
		if v == agentDeckTerminalFeature {
			n++
		}
	}
	return n
}
