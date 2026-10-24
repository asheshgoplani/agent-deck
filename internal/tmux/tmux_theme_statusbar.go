package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/BurntSushi/toml"
	"github.com/asheshgoplani/agent-deck/internal/agentpaths"
	dark "github.com/thiagokokada/dark-mode-go"
)

type tmuxThemeStyle struct {
	windowStyle       string
	windowActiveStyle string
	statusStyle       string
	hintColor         string
}

func resolvedAgentDeckTheme() string {
	type cfg struct {
		Theme string `toml:"theme"`
	}
	if configPath, err := agentpaths.EffectiveConfigPath("config.toml"); err == nil {
		var c cfg
		if _, err := toml.DecodeFile(configPath, &c); err == nil {
			switch c.Theme {
			case "light", "dark":
				return c.Theme
			case "system", "":
				// fall through to OS detection
			default:
				return "dark"
			}
		}
	}
	// Check the terminal's own declaration before asking the OS.
	// COLORFGBG is set by iTerm2 and other terminals; format is "fg;bg"
	// where bg < 8 means a dark background. This catches the common case
	// where macOS is in light mode but the terminal profile is dark.
	if colorfgbg := os.Getenv("COLORFGBG"); colorfgbg != "" {
		if idx := strings.LastIndex(colorfgbg, ";"); idx >= 0 {
			var bg int
			if _, err := fmt.Sscanf(colorfgbg[idx+1:], "%d", &bg); err == nil {
				if bg < 8 {
					return "dark"
				}
				return "light"
			}
		}
	}

	isDark, err := dark.IsDarkMode()
	if err != nil {
		return "dark"
	}
	if isDark {
		return "dark"
	}
	return "light"
}

func currentTmuxThemeStyle() tmuxThemeStyle {
	if resolvedAgentDeckTheme() == "light" {
		return tmuxThemeStyle{
			// Light terminals can still inherit a dark-looking tmux window background
			// when we leave window-style at "default". Use an explicit neutral light
			// background so the attached client sees and renders against a light pane.
			windowStyle:       "bg=#f9f9f9",
			windowActiveStyle: "bg=#f9f9f9",
			statusStyle:       "bg=#e9e9ec,fg=#343b58",
			hintColor:         "#6a6d7c",
		}
	}
	return tmuxThemeStyle{
		// Preserve the historical dark behavior unless a light theme is active.
		windowStyle:       "default",
		windowActiveStyle: "default",
		statusStyle:       "bg=#1a1b26,fg=#a9b1d6",
		hintColor:         "#565f89",
	}
}

// SetStatusHints updates the detach/switch key labels shown in the tmux
// status-right bar. Empty labels are ignored (the existing value is kept).
func SetStatusHints(detach, switchKey string, switchEnabled bool) {
	statusHintMu.Lock()
	defer statusHintMu.Unlock()
	if detach != "" {
		detachHintLabel = detach
	}
	if switchKey != "" {
		switchHintLabel = switchKey
	}
	switchHintEnabled = switchEnabled
}

func (s *Session) themedStatusRight(themeStyle tmuxThemeStyle) string {
	statusHintMu.RLock()
	detach, switchKey, switchOn := detachHintLabel, switchHintLabel, switchHintEnabled
	statusHintMu.RUnlock()

	hints := fmt.Sprintf("#[fg=%s]%s detach#[default]", themeStyle.hintColor, detach)
	if switchOn {
		hints += fmt.Sprintf(" · #[fg=%s]%s switch#[default]", themeStyle.hintColor, switchKey)
	}
	return fmt.Sprintf("%s │ 📁 %s | %s ", hints, FormatLiteral(s.DisplayName), FormatLiteral(s.projectDisplayName()))
}

// FormatLiteral escapes text placed into a tmux format string such as
// status-right, status-left or a display-message. tmux expands "#(cmd)" there by running cmd through the shell
// on every status refresh, and "#[...]" as style codes; "##" is a literal "#".
// The session title and the project folder name are not ours to trust (a
// title can come from an OpenClaw gateway or a web request, a folder from a
// cloned repository), so every "#" in them is doubled.
func FormatLiteral(s string) string {
	return strings.ReplaceAll(s, "#", "##")
}

func (s *Session) projectDisplayName() string {
	folderName := filepath.Base(s.WorkDir)
	if folderName == "" || folderName == "." {
		folderName = "~"
	}
	return folderName
}

func (s *Session) ApplyThemeOptions() error {
	themeStyle := currentTmuxThemeStyle()
	var args []string
	if _, ok := s.OptionOverrides["window-style"]; !ok {
		args = append(args, "set-option", "-t", s.Name, "window-style", themeStyle.windowStyle, ";")
	}
	if _, ok := s.OptionOverrides["window-active-style"]; !ok {
		args = append(args, "set-option", "-t", s.Name, "window-active-style", themeStyle.windowActiveStyle, ";")
	}
	args = append(args, "set-option", "-t", s.Name, "status-style", themeStyle.statusStyle)
	if s.injectStatusLine {
		args = append(args,
			";", "set-option", "-t", s.Name, "status-right", s.themedStatusRight(themeStyle),
		)
	}
	// Bounded — see tmuxPollTimeout.
	return s.runBoundedRun(args...)
}

// buildStatusBarArgs returns the tmux command args for configuring the status bar.
// Returns nil if status bar injection is disabled.
// Skips any option key that exists in s.OptionOverrides — user-defined options take precedence.
func (s *Session) buildStatusBarArgs() []string {
	if !s.injectStatusLine {
		// Disabling injection must actively turn the bar OFF, not merely skip
		// setting ours. tmux's own default is `status on`, so a bar — from that
		// default, the user's tmux config, or an earlier run that had injection
		// enabled — persists on the session unless we emit `status off`.
		// Returning nil here is why `inject_status_line = false` silently did
		// nothing on any session that already showed a bar (#687). Respect an
		// explicit user `status` entry in [tmux].options.
		if _, overridden := s.OptionOverrides["status"]; overridden {
			return nil
		}
		return []string{"set-option", "-t", s.Name, "status", "off"}
	}
	themeStyle := currentTmuxThemeStyle()
	rightStatus := s.themedStatusRight(themeStyle)

	// Managed defaults — each can be skipped if user defined it in [tmux] options
	type option struct {
		key   string
		value string
	}
	defaults := []option{
		{"status", "on"},
		{"status-style", themeStyle.statusStyle},
		{"status-left-length", "120"},
		{"status-right", rightStatus},
		{"status-right-length", "100"},
	}

	var args []string
	for _, opt := range defaults {
		if _, overridden := s.OptionOverrides[opt.key]; overridden {
			continue
		}
		if len(args) > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-t", s.Name, opt.key, opt.value)
	}

	if len(args) == 0 {
		return nil
	}
	return args
}

// hideCwdPrefixInTitle, when true, drops the "[<project>] " prefix from the
// terminal title (set-titles-string). Default false preserves the historical
// "[<project>] <name>" format. Set once at startup from the user config's
// [display] include_cwd_prefix via SetHideCwdPrefixInTitle.
var hideCwdPrefixInTitle atomic.Bool

// SetHideCwdPrefixInTitle configures whether the terminal title includes the
// "[<cwd-basename>]" prefix. Pass true to drop it (display.include_cwd_prefix =
// false). Safe to call concurrently; intended to run once at startup.
func SetHideCwdPrefixInTitle(hide bool) {
	hideCwdPrefixInTitle.Store(hide)
}

// titleFormat, when non-empty, overrides the default set-titles-string with a
// user-supplied template (config [display] title_format). It takes precedence
// over hideCwdPrefixInTitle. Set once at startup from SetTitleFormat.
var titleFormat atomic.Value // holds string

// titleFormatReplacer translates the user-facing title template placeholders
// into tmux format references to the @agentdeck_* user options. Using tmux
// references (rather than baking literal values) means tmux re-renders the
// title live whenever an option changes — e.g. on rename or regroup.
var titleFormatReplacer = strings.NewReplacer(
	"{group}", "#{@agentdeck_group_path}",
	"{project}", "#{@agentdeck_project_name}",
	"{name}", "#{@agentdeck_display_name}",
)

// SetTitleFormat configures a custom terminal title template. Supported
// placeholders: {group}, {project}, {name}. An empty string (the default)
// preserves the historical "[<project>] <name>" format and the
// include_cwd_prefix toggle. Safe to call concurrently; intended to run once at
// startup from [display] title_format via SetTitleFormat.
func SetTitleFormat(format string) {
	titleFormat.Store(format)
}

// getTitleFormat returns the configured custom title template, or "" if unset.
func getTitleFormat() string {
	if v := titleFormat.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// buildTerminalTitleArgs returns the tmux command args for configuring the outer
// terminal title shown by clients such as iTerm2. Session metadata user options
// are always refreshed so custom title formats can reuse them.
func (s *Session) buildTerminalTitleArgs() []string {
	type option struct {
		key   string
		value string
	}

	defaults := []option{
		{"@agentdeck_project_name", s.projectDisplayName()},
		{"@agentdeck_display_name", s.DisplayName},
		{"@agentdeck_group_path", s.GetGroupPath()},
	}
	if _, overridden := s.OptionOverrides["set-titles"]; !overridden {
		defaults = append(defaults, option{key: "set-titles", value: "on"})
	}
	if _, overridden := s.OptionOverrides["set-titles-string"]; !overridden {
		titleStr := "[#{@agentdeck_project_name}] #{@agentdeck_display_name}"
		if custom := getTitleFormat(); custom != "" {
			titleStr = titleFormatReplacer.Replace(custom)
		} else if hideCwdPrefixInTitle.Load() {
			titleStr = "#{@agentdeck_display_name}"
		}
		defaults = append(defaults, option{key: "set-titles-string", value: titleStr})
	}

	args := make([]string, 0, len(defaults)*6)
	for i, opt := range defaults {
		if i > 0 {
			args = append(args, ";")
		}
		args = append(args, "set-option", "-t", s.Name, opt.key, opt.value)
	}
	return args
}

// ConfigureTerminalTitle sets tmux options that drive the outer terminal tab or
// window title for this session.
func (s *Session) ConfigureTerminalTitle() {
	args := s.buildTerminalTitleArgs()
	if len(args) == 0 {
		return
	}
	_ = s.runBoundedRun(args...)
}

// GetGroupPath and SetGroupPath synchronize the cached title metadata without
// contending with the tmux status/capture lock.
func (s *Session) GetGroupPath() string {
	s.groupTitleMu.Lock()
	defer s.groupTitleMu.Unlock()
	return s.GroupPath
}

func (s *Session) SetGroupPath(path string) {
	s.groupTitleMu.Lock()
	s.GroupPath = path
	s.groupTitleMu.Unlock()
}

// SetGroupTitleMetadata refreshes only the committed group option. It leaves
// per-session title formats and other user overrides intact.
func (s *Session) SetGroupTitleMetadata(groupPath string) error {
	return s.runBoundedRun("set-option", "-t", s.Name, "@agentdeck_group_path", groupPath)
}

// ConfigureStatusBar sets up the tmux status bar with session info.
// Shows: notification bar on left (managed by NotificationManager), session info on right.
// NOTE: status-left is reserved for the notification bar showing waiting sessions.
// Options defined in [tmux] options are respected — agent-deck skips those keys.
func (s *Session) ConfigureStatusBar() {
	args := s.buildStatusBarArgs()
	if args == nil {
		return
	}
	// Bounded — see tmuxPollTimeout. This status set-option batch was one of the
	// observed orphaned 100%-CPU tmux clients when the server was wedged.
	_ = s.runBoundedRun(args...)
}

// SetStatusLeft sets the left side of tmux status bar for a session.
// Used by NotificationManager to display waiting session notifications.
//
// text is a tmux format string: callers escape untrusted parts with
// FormatLiteral. It is passed as one argv element, so no shell quoting.
func SetStatusLeft(sessionName, text string) error {
	// Bounded — see tmuxMutationTimeout. Driven by the notification sweep.
	return runBoundedMutation(DefaultSocketName(), "set-option", "-t", sessionName, "status-left", text)
}

// ClearStatusLeft resets status-left to default for a session.
// Called when notifications are cleared or acknowledged.
func ClearStatusLeft(sessionName string) error {
	// -u flag unsets the option, reverting to tmux default
	// Bounded — see tmuxMutationTimeout.
	return runBoundedMutation(DefaultSocketName(), "set-option", "-t", sessionName, "-u", "status-left")
}

// savedStatusLeft holds the original global status-left value before agent-deck overwrites it.
// This allows ClearStatusLeftGlobal to restore the user's theme/plugin value (e.g., Oasis, Catppuccin)
// instead of unsetting it, which would fall back to tmux's built-in default "[#{session_name}]".
var savedStatusLeft struct {
	sync.Once
	value    string
	captured bool
}

// statusLeftOriginalOption is a tmux user option holding the user's own global
// status-left. It lives on the tmux server, so it outlives the TUI process: a
// TUI that exits without ClearStatusLeftGlobal (crash, kill -9, auto-update
// restart) leaves its notification bar as the global status-left, and the next
// TUI must not mistake that bar for the user's theme value (#2705).
const statusLeftOriginalOption = "@agentdeck_orig_status_left"

// statusLeftOriginalMarker prefixes the stored value so an empty original
// status-left is told apart from an unset option (show-option -q prints "").
const statusLeftOriginalMarker = "v:"

// notificationBarPrefix is the prefix every agent-deck notification bar
// carries (session.FormatBar / formatBarMinimal).
const notificationBarPrefix = "⚡ "

// looksLikeNotificationBar reports whether a status-left value is an
// agent-deck notification bar rather than a user or theme value.
func looksLikeNotificationBar(value string) bool {
	return strings.HasPrefix(value, notificationBarPrefix)
}

// captureOriginalStatusLeft reads and stores the current global status-left value.
// Called once on first SetStatusLeftGlobal to preserve the user's existing value.
// When the current value is a leftover notification bar from an earlier TUI,
// the original saved on the tmux server is used instead; with none saved,
// nothing is captured and ClearStatusLeftGlobal falls back to the tmux default.
func captureOriginalStatusLeft() {
	socket := DefaultSocketName()
	// Bounded — see tmuxPollTimeout. A read, and the only chance to capture the
	// user's theme value: if it hangs, SetStatusLeftGlobal never runs either.
	out, err := runBoundedOutput(socket, "show-option", "-gv", "status-left")
	if err != nil {
		return
	}
	current := strings.TrimRight(string(out), "\n")
	if !looksLikeNotificationBar(current) {
		savedStatusLeft.value = current
		savedStatusLeft.captured = true
		// Persist it for a later TUI that starts while a bar is showing.
		_ = runBoundedMutation(socket, "set-option", "-g", statusLeftOriginalOption,
			statusLeftOriginalMarker+current)
		return
	}
	// A previous TUI's bar is still up: never treat it as the original.
	stored, err := runBoundedOutput(socket, "show-option", "-gqv", statusLeftOriginalOption)
	if err != nil {
		return
	}
	if value, ok := strings.CutPrefix(strings.TrimRight(string(stored), "\n"), statusLeftOriginalMarker); ok {
		savedStatusLeft.value = value
		savedStatusLeft.captured = true
	}
}

// SetStatusLeftGlobal sets the left side of tmux status bar globally.
// This is a MAJOR performance optimization: ONE tmux call instead of 100+.
// All agentdeck sessions inherit this global setting.
// On first call, captures the existing status-left so ClearStatusLeftGlobal can restore it.
//
// text is a tmux format string (the bar uses #[fg=...] styles): callers
// escape untrusted parts such as session titles with FormatLiteral. It is
// passed as one argv element, so it needs no shell quoting (the former
// shell-style quote rewrite made tmux show "it's" with backslashes in it).
func SetStatusLeftGlobal(text string) error {
	savedStatusLeft.Do(captureOriginalStatusLeft)
	// Bounded — see tmuxMutationTimeout. Driven by the notification sweep.
	return runBoundedMutation(DefaultSocketName(), "set-option", "-g", "status-left", text)
}

// ClearStatusLeftGlobal restores the original global status-left value.
// If the original value was captured, it is restored so the user's theme/plugin
// (e.g., tmux-oasis) is preserved. Falls back to unsetting the option only if
// no original value was captured.
func ClearStatusLeftGlobal() error {
	socket := DefaultSocketName()
	if savedStatusLeft.captured {
		// Restored verbatim: it is the user's own format string.
		// Bounded — see tmuxMutationTimeout.
		return runBoundedMutation(socket, "set-option", "-g", "status-left", savedStatusLeft.value)
	}
	// No saved value — fall back to unset (original behavior)
	return runBoundedMutation(socket, "set-option", "-gu", "status-left")
}

// InitializeStatusBarOptions sets optimal status bar options for agent-deck.
// Fixes truncation by setting adequate status-left-length globally.
// Should be called once during startup.
func InitializeStatusBarOptions() error {
	// Set adequate status-left-length globally (default is only 10 chars!)
	// This ensures the notification bar content is not truncated
	// Bounded — see tmuxMutationTimeout.
	return runBoundedMutation(DefaultSocketName(), "set-option", "-g", "status-left-length", "120")
}

// RefreshStatusBarImmediate forces an immediate status bar redraw for ALL connected clients.
// This bypasses the status-interval timer (default 15s) for instant visual feedback.
// Uses -S flag which only refreshes the status line (lightweight operation ~1-2ms per client).
// Filters out control mode clients (from PipeManager) which don't have a visible status bar.
func RefreshStatusBarImmediate() error {
	// One list-clients plus one refresh-client per attached client. A web
	// terminal is another client, so this scales with viewers, not sessions.
	// The status-pass budget is per session; charging this is what made
	// Ctrl+Q (the bar changes as the attached session changes) exceed it.
	var err error
	RunUncounted(func() { err = refreshStatusBarImmediate() })
	return err
}

func refreshStatusBarImmediate() error {
	socket := DefaultSocketName()
	// Get all connected clients, filtering out control mode clients
	// client_name is free-text (a pts path) so it goes LAST, after the 0/1
	// control-mode flag, to stay collision-safe under tmuxFieldSep.
	// Bounded — see tmuxPollTimeout. list-clients against a wedged server was a
	// primary orphan-spin source when the client outlived its owning TUI.
	output, err := runBoundedOutput(socket, "list-clients", "-F", tmuxFmt("#{client_control_mode}", "#{client_name}"))
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		parts := strings.SplitN(line, tmuxFieldSep, 2)
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		// Skip control mode clients (PipeManager pipes)
		if parts[0] == "1" {
			continue
		}
		// Bounded — see tmuxMutationTimeout. One client per iteration on the
		// bar-refresh path: the highest-frequency spawn site in the codebase.
		_ = runBoundedMutation(socket, "refresh-client", "-S", "-t", parts[1])
	}
	return nil
}
