package tmux

import (
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

// windowPolicyOption is one of the per-window sizing options Deck applies to
// every window of a session. defaultValue is Deck's value for the tmux the
// session runs on (a `tmux -V` version string, see parseTmuxVersion).
// accepted lists the values tmux takes for the option (tmux(1)
// "window-size", and the flag spellings of "aggressive-resize"); anything
// else in [tmux.options] is a typo tmux would reject.
type windowPolicyOption struct {
	key, userOption string
	defaultValue    func(tmuxVersion string) string
	accepted        []string
}

// windowPolicyOptions is the one definition of the multi-client size policy
// (#2186, #2259, shared attach): the per-window sizing options Deck applies
// to every window of a session. Start() sets them on the initial window,
// NewShellWindow on windows Deck opens, the after-new-window hook installed
// by installWindowPolicyHook on windows opened any other way (#2259), and
// ApplySharedViewSize on every window again before each attach (sharedview.go:
// resize-window pins a window to `manual`, and a session created by an older
// build still carries `smallest`). Each option's effective value (Deck's
// default, or the user's [tmux.options] override) is published on the session
// as a @agentdeck_* user option so the hook, which is shared by every session
// on the server, can read the per-session value.
//
// The policy is `latest`: the window follows the client that most recently
// attached, typed or resized, so the person using the session sees it
// full-size and nobody's idle terminal can pin it. `smallest` (rc.6, #2186)
// boxes every client larger than the smallest one into a corner with dots;
// `largest` produces a synthetic size no client can show once the client
// geometries cross. tmux < 3.1 has no `latest`; there the closest policy is
// `largest` with aggressive-resize, so at least the active window follows
// its viewers.
var windowPolicyOptions = []windowPolicyOption{
	{"window-size", "@agentdeck_window_size", windowSizeDefault, []string{"largest", "smallest", "manual", "latest"}},
	{"aggressive-resize", "@agentdeck_aggressive_resize", func(string) string { return "on" }, []string{"on", "off", "yes", "no", "1", "0"}},
}

// Deck's window-size policy and its fallback for a tmux without `latest`.
const (
	windowSizePolicy         = "latest"
	windowSizePolicyFallback = "largest"
)

// windowSizeLatestMinTmux{Major,Minor} name tmux 3.1, the first tmux with
// `window-size latest` (tmux CHANGES, 3.1: "Add a latest value for
// window-size"). Unparseable versions ("master", "next", "") are assumed new
// enough, as for hook arrays.
const (
	windowSizeLatestMinTmuxMajor = 3
	windowSizeLatestMinTmuxMinor = 1
)

// windowSizeDefault is Deck's window-size for the given tmux version.
func windowSizeDefault(tmuxVersion string) string {
	if tmuxSupportsWindowSizeLatest(tmuxVersion) {
		return windowSizePolicy
	}
	return windowSizePolicyFallback
}

// tmuxSupportsWindowSizeLatest reports whether a parsed tmux version names a
// tmux with `window-size latest` (3.1 or later).
func tmuxSupportsWindowSizeLatest(ver string) bool {
	major, minor, _, ok := splitTmuxVersion(ver)
	if !ok {
		return true
	}
	return major > windowSizeLatestMinTmuxMajor ||
		(major == windowSizeLatestMinTmuxMajor && minor >= windowSizeLatestMinTmuxMinor)
}

// afterNewWindowHookIndex is the fixed slot Deck owns in the server-wide
// after-new-window hook array. tmux hooks are arrays (tmux >= 3.0); a hook
// set at a session scope would replace the user's global array entirely
// (tmux resolves the hook option at the most specific scope and stops), so
// Deck's hook has to live in the global array next to the user's entries.
// Re-setting one fixed index is idempotent across any number of Start()
// calls on the same server, with no read-then-append race between sessions
// starting concurrently, and leaves the user's own indices (0..n from
// `set-hook -g` / `set-hook -ga`) untouched. Like terminal-features'
// reserved slot (agentDeckTerminalFeatureIndex), a foreign occupant wins and
// is never overwritten.
const afterNewWindowHookIndex = 2259

// afterNewWindowHookSlot is the option name of Deck's reserved slot.
var afterNewWindowHookSlot = fmt.Sprintf("after-new-window[%d]", afterNewWindowHookIndex)

// hookArrayMinTmuxVersion is the first tmux whose hooks are array options
// (tmux CHANGES, 2.9 -> 3.0: "Hooks are now stored in the options tree as
// array options"). Older servers have no indexed slot to install into.
const (
	hookArrayMinTmuxVersion = "3.0"
	hookArrayMinTmuxMajor   = 3
)

// value returns the value Deck applies for the option: the user's override
// when one is configured and tmux would accept it, else Deck's default for
// tmuxVersion (the host's, see hostTmuxVersionString). An override tmux
// would reject is reported once per call site and yields ok=false: nothing
// is applied and nothing is published for the hook, which would otherwise
// fail on every new window and make the `new-window` command itself exit 1
// (the duplicate-tab path #2186 exists to prevent). The generic override
// pass in Start() still applies the raw value with `-q`, so the failure
// surfaces there exactly as it did before the hook.
func (o windowPolicyOption) value(overrides map[string]string, tmuxVersion string) (value string, ok bool) {
	override, overridden := overrides[o.key]
	if !overridden {
		return o.defaultValue(tmuxVersion), true
	}
	if slices.Contains(o.accepted, override) {
		return override, true
	}
	statusLog.Warn("window_policy_override_invalid",
		slog.String("option", o.key), slog.String("value", override), slog.Any("accepted", o.accepted))
	return "", false
}

// windowPolicyArgs chains one `setCmd -t <target> <option> <value>` per
// target and policy option, separated by ";", for the tmux named by
// tmuxVersion. An option whose override is invalid is skipped. Callers pick
// the set command: ApplySharedViewSize uses `set-option -w -q`,
// NewShellWindow `set-window-option -oq` (-o keeps an option a user's
// after-new-window hook already set on the brand-new window).
func windowPolicyArgs(targets []string, overrides map[string]string, tmuxVersion string, setCmd ...string) []string {
	var args []string
	for _, target := range targets {
		for _, option := range windowPolicyOptions {
			value, ok := option.value(overrides, tmuxVersion)
			if !ok {
				continue
			}
			if len(args) > 0 {
				args = append(args, ";")
			}
			args = append(args, setCmd...)
			args = append(args, "-t", target, option.key, value)
		}
	}
	return args
}

// windowPolicyStartArgs applies the window policy to the session's initial
// window and publishes the effective values as @agentdeck_* session options
// for the after-new-window hook to read. It is meant to be appended to an
// in-progress `;`-separated tmux command chain. An option whose override is
// invalid is neither applied nor published, so the hook (which tests the
// option with `if-shell -F`) leaves that option alone.
func (s *Session) windowPolicyStartArgs() []string {
	tmuxVersion := hostTmuxVersionString()
	args := make([]string, 0, len(windowPolicyOptions)*14)
	for _, option := range windowPolicyOptions {
		value, ok := option.value(s.OptionOverrides, tmuxVersion)
		if !ok {
			continue
		}
		args = append(args,
			";", "set-option", "-w", "-q", "-t", s.Name, option.key, value,
			";", "set-option", "-t", s.Name, option.userOption, value)
	}
	return args
}

// windowPolicyHook is the body of Deck's server-wide after-new-window hook.
// For each policy option the hook reads the session's published @agentdeck_*
// value and, when the new window belongs to a session that has one, applies
// it with `set-option -o` so a value the user's own hook entry installed
// earlier in the array is preserved (the #2186 contract NewShellWindow
// honours). Windows of sessions Deck did not start carry no @agentdeck_*
// option and are left alone. Everything is evaluated by tmux's format engine
// (`if-shell -F`, `set-option -F`); no shell is involved.
func windowPolicyHook() string {
	parts := make([]string, 0, len(windowPolicyOptions))
	for _, option := range windowPolicyOptions {
		parts = append(parts, fmt.Sprintf("if-shell -F '#{%[2]s}' \"set-option -woqF %[1]s '#{%[2]s}'\"", option.key, option.userOption))
	}
	return strings.Join(parts, " ; ")
}

// windowPolicyHookSignature marks a hook as Deck's, from this or an older
// release, so an upgrade refreshes the slot: only Deck's hook reads the
// @agentdeck_* options. tmux re-serialises a stored hook (quoting differs
// from what was set), so ownership cannot be an exact string match.
const windowPolicyHookSignature = "@agentdeck_"

// WindowPolicyHookState is what the reserved after-new-window slot holds.
type WindowPolicyHookState int

const (
	// WindowPolicyHookAbsent: the slot is unset (or empty).
	WindowPolicyHookAbsent WindowPolicyHookState = iota
	// WindowPolicyHookOwned: the slot holds Deck's hook.
	WindowPolicyHookOwned
	// WindowPolicyHookForeign: the slot holds something Deck did not install.
	WindowPolicyHookForeign
)

// WindowPolicyHookStatus reads Deck's reserved after-new-window slot on the
// server behind socketName ("" = the user's default server). An error means
// the slot could not be read (no server, a wedged client, a tmux without
// array hooks); nothing is ever written after a failed read.
func WindowPolicyHookStatus(socketName string) (WindowPolicyHookState, error) {
	out, err := runBoundedOutput(socketName, "show-options", "-gqv", afterNewWindowHookSlot)
	if err != nil {
		return WindowPolicyHookAbsent, err
	}
	content := strings.TrimRight(string(out), "\n")
	switch {
	case content == "":
		return WindowPolicyHookAbsent, nil
	case strings.Contains(content, windowPolicyHookSignature):
		return WindowPolicyHookOwned, nil
	default:
		return WindowPolicyHookForeign, nil
	}
}

// InstallWindowPolicyHook installs (or refreshes) Deck's after-new-window
// hook in its reserved slot on the server behind socketName. A foreign
// occupant is left in place and reported as WindowPolicyHookForeign; the
// returned state is what the slot held before the call.
func InstallWindowPolicyHook(socketName string) (WindowPolicyHookState, error) {
	state, err := WindowPolicyHookStatus(socketName)
	if err != nil || state == WindowPolicyHookForeign {
		return state, err
	}
	return state, runBoundedMutation(socketName, "set-hook", "-g", afterNewWindowHookSlot, windowPolicyHook())
}

// UninstallWindowPolicyHook removes Deck's after-new-window hook from its
// reserved slot on the server behind socketName, only when the slot holds
// Deck's hook. The returned state is what the slot held before the call.
// The per-session @agentdeck_* options need no cleanup: they die with their
// sessions and are inert without the hook.
func UninstallWindowPolicyHook(socketName string) (WindowPolicyHookState, error) {
	state, err := WindowPolicyHookStatus(socketName)
	if err != nil || state != WindowPolicyHookOwned {
		return state, err
	}
	return state, runBoundedMutation(socketName, "set-hook", "-gu", afterNewWindowHookSlot)
}

// tmuxSupportsHookArrays reports whether a `tmux -V` version string names a
// tmux with array hooks (>= hookArrayMinTmuxVersion, so any 3.x or later).
// Unparseable versions ("master", "next", "openbsd-7.4", "") are assumed new
// enough: the install is best-effort and a failure is logged, while refusing
// would silently drop the policy on every build that does not print a plain
// number.
func tmuxSupportsHookArrays(ver string) bool {
	major, _, _, ok := splitTmuxVersion(ver)
	return !ok || major >= hookArrayMinTmuxMajor
}

var (
	hostTmuxVersionOnce sync.Once
	hostTmuxVersion     string
)

// hostTmuxVersionString probes `tmux -V` once per process and returns the
// parsed version ("" when the probe failed, which every consumer treats as
// new enough). Every tmux Deck talks to is the host's: a remote attach runs
// the remote's own agent-deck binary against the remote's tmux.
func hostTmuxVersionString() string {
	hostTmuxVersionOnce.Do(func() {
		raw, err := defaultTmuxVersionProbe()
		if err == nil {
			hostTmuxVersion = parseTmuxVersion(raw)
		}
	})
	return hostTmuxVersion
}

// hostTmuxSupportsHookArrays reports whether the host tmux has array hooks.
func hostTmuxSupportsHookArrays() bool {
	return tmuxSupportsHookArrays(hostTmuxVersionString())
}

// installWindowPolicyHook is Start()'s best-effort install of the hook on
// the session's server. On a tmux without array hooks it does nothing: the
// initial window already has the policy from Start() and NewShellWindow
// covers Deck-opened windows; only hand-opened windows miss it there.
func (s *Session) installWindowPolicyHook() {
	if !hostTmuxSupportsHookArrays() {
		statusLog.Info("window_policy_hook_skipped",
			slog.String("tmux_version", hostTmuxVersion), slog.String("requires", hookArrayMinTmuxVersion))
		return
	}
	state, err := InstallWindowPolicyHook(s.SocketName)
	switch {
	case err != nil:
		statusLog.Warn("window_policy_hook_install_failed", slog.String("error", err.Error()))
	case state == WindowPolicyHookForeign:
		statusLog.Warn("window_policy_hook_slot_foreign", slog.String("slot", afterNewWindowHookSlot))
	}
}
