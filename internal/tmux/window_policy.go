package tmux

import "fmt"

// windowPolicyOptions are the per-window sizing options Deck applies to every
// window of a session: Start() sets them on the initial window, NewShellWindow
// on windows Deck opens, and the after-new-window hook installed by
// windowPolicyHookArgs on windows opened any other way (#2259). Each option's
// effective value (Deck's default, or the user's [tmux.options] override) is
// published on the session as a @agentdeck_* user option so the hook, which is
// shared by every session on the server, can read the per-session value.
var windowPolicyOptions = []struct{ key, defaultValue, userOption string }{
	{"window-size", "smallest", "@agentdeck_window_size"},
	{"aggressive-resize", "on", "@agentdeck_aggressive_resize"},
}

// afterNewWindowHookIndex is the fixed slot Deck owns in the server-wide
// after-new-window hook array. tmux hooks are arrays (tmux >= 3.0); a hook
// set at a session scope would replace the user's global array entirely
// (tmux resolves the hook option at the most specific scope and stops), so
// Deck's hook has to live in the global array next to the user's entries.
// Re-setting one fixed index is idempotent across any number of Start()
// calls on the same server, with no read-then-append race between sessions
// starting concurrently, and leaves the user's own indices (0..n from
// `set-hook -g` / `set-hook -ga`) untouched.
const afterNewWindowHookIndex = 2259

// windowPolicyValue returns the value Deck applies for a window policy
// option: the user's override when one is configured, else Deck's default.
func (s *Session) windowPolicyValue(key, defaultValue string) string {
	if value, ok := s.OptionOverrides[key]; ok {
		return value
	}
	return defaultValue
}

// windowPolicyOptionArgs publishes the session's effective window policy as
// @agentdeck_* session options for the after-new-window hook to read. It is
// meant to be appended to an in-progress `;`-separated tmux command chain.
func (s *Session) windowPolicyOptionArgs() []string {
	args := make([]string, 0, len(windowPolicyOptions)*6)
	for _, option := range windowPolicyOptions {
		args = append(args, ";", "set-option", "-t", s.Name, option.userOption, s.windowPolicyValue(option.key, option.defaultValue))
	}
	return args
}

// windowPolicyHookArgs installs (or re-installs, idempotently) Deck's
// server-wide after-new-window hook. For each policy option the hook reads
// the session's published @agentdeck_* value and, when the new window belongs
// to a session that has one, applies it with `set-option -o` so a value the
// user's own hook entry installed earlier in the array is preserved (the
// #2186 contract NewShellWindow honours). Windows of sessions Deck did not
// start carry no @agentdeck_* option and are left alone. Everything is
// evaluated by tmux's format engine (`if-shell -F`, `set-option -F`); no
// shell is involved. Meant to be appended last to a `;`-separated chain:
// tmux aborts a chain at the first failing command, and on a tmux too old
// for array hooks (< 3.0) only this command may fail.
func windowPolicyHookArgs() []string {
	hook := ""
	for _, option := range windowPolicyOptions {
		if hook != "" {
			hook += " ; "
		}
		hook += fmt.Sprintf("if-shell -F '#{%[2]s}' \"set-option -woqF %[1]s '#{%[2]s}'\"", option.key, option.userOption)
	}
	return []string{";", "set-hook", "-g", fmt.Sprintf("after-new-window[%d]", afterNewWindowHookIndex), hook}
}
