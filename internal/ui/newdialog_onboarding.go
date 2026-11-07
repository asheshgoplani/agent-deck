package ui

// First-run onboarding for the New Session form (ASK-211; plan and prototype
// by Sharjeel). Four pieces live here so newdialog.go stays readable:
//
//   - installed tools: the tool row lists installed agents first, then the
//     plain shell, then the missing tools in grey. A missing tool shows a
//     warning with its install line; Create still goes ahead (the probe can
//     miss a tool the pane finds through the user's shell setup). With no
//     default tool configured the form starts on the first installed agent instead of the shell.
//   - the path is shown with ~ for the home folder.
//   - the hooks line: when Claude hooks are not decided yet and the tool is
//     Claude, one ticked line replaces the old "Claude Code Hooks" box.
//   - "More options": worktree, Docker sandbox and multi-repo fold away.

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/harness"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// newDialogToolInstalled reports whether a tool can start on this machine.
// TestMain sets it to nil: the dialog then treats every tool as installed and
// keeps the tool order, so tests never depend on the host PATH. Tests of the
// installed/missing states install their own probe.
var newDialogToolInstalled = session.ToolInstalled

// hooksLineText is the one sentence that replaced the Claude Code Hooks box.
const hooksLineText = "Show me when Claude is busy or needs me (adds a small hook to Claude's settings)"

// RefreshInstalledTools probes every local tool and orders the tool row:
// installed agents (in the usual order), then the plain shell, then the
// missing tools. The current pick is kept. It does nothing for a remote
// target (install state there is the remote's) or when the probe is off.
func (d *NewDialog) RefreshInstalledTools() {
	if d.remoteTarget || newDialogToolInstalled == nil {
		d.toolInstalled = nil
		return
	}
	prev := d.GetSelectedCommand()
	installed := make(map[string]bool, len(d.presetCommands))
	var agents, missing []string
	hasShell := false
	for _, cmd := range d.presetCommands {
		if cmd == "" {
			hasShell = true
			installed[""] = true
			continue
		}
		ok := newDialogToolInstalled(cmd)
		installed[cmd] = ok
		if ok {
			agents = append(agents, cmd)
		} else {
			missing = append(missing, cmd)
		}
	}
	order := make([]string, 0, len(d.presetCommands))
	order = append(order, agents...)
	if hasShell {
		order = append(order, "")
	}
	order = append(order, missing...)
	d.presetCommands = order
	d.toolInstalled = installed
	d.commandCursor = 0
	for i, cmd := range d.presetCommands {
		if cmd == prev {
			d.commandCursor = i
			break
		}
	}
	d.updateToolOptions()
}

// FirstInstalledAgent is the first installed agent in the tool row, or ""
// when none is installed or install state is unknown.
func (d *NewDialog) FirstInstalledAgent() string {
	if d.toolInstalled == nil {
		return ""
	}
	for _, cmd := range d.presetCommands {
		if cmd != "" && d.toolInstalled[cmd] {
			return cmd
		}
	}
	return ""
}

// toolIsInstalled reports whether cmd may be created. Unknown install state
// (remote target, probe off, a tool added after the probe) counts as
// installed so nothing is blocked on a guess.
func (d *NewDialog) toolIsInstalled(cmd string) bool {
	if d.toolInstalled == nil || cmd == "" {
		return true
	}
	ok, known := d.toolInstalled[cmd]
	return !known || ok
}

// anyToolMissing reports whether the tool row has a grey (missing) tool.
func (d *NewDialog) anyToolMissing() bool {
	for _, cmd := range d.presetCommands {
		if !d.toolIsInstalled(cmd) {
			return true
		}
	}
	return false
}

// selectedToolMissing reports whether the picked tool is not installed and
// the session would run on this machine. A Docker sandbox runs the tool in
// its image, so the host's PATH does not decide it.
func (d *NewDialog) selectedToolMissing() bool {
	return !d.createsOffMachine() && !d.sandboxEnabled && !d.toolIsInstalled(d.GetSelectedCommand())
}

// toolInstallInfo returns the display name and the official install command
// of a tool from the harness table (with [harnesses.<name>] overrides). The
// install command is "" when the table has none.
func toolInstallInfo(cmd string) (display, install string) {
	display = displayCommandPreset(cmd)
	var overrides map[string]harness.Override
	if cfg, _ := session.LoadUserConfig(); cfg != nil {
		overrides = cfg.Harnesses
	}
	for _, e := range harness.All(overrides) {
		if e.Name == cmd {
			return e.DisplayName, e.InstallCommand
		}
	}
	return display, ""
}

// missingToolLines are the warning lines under the tool row for a missing
// pick. They warn only: Create is not refused, since the pane may still find
// the tool through the user's shell (an alias, a PATH set in an rc file).
func (d *NewDialog) missingToolLines() []string {
	if !d.selectedToolMissing() {
		return nil
	}
	display, install := toolInstallInfo(d.GetSelectedCommand())
	lines := []string{display + " not found here; Create still tries it."}
	if install != "" {
		lines = append(lines, "Install: "+install)
	}
	return lines
}

// tildePath shows a local path with ~ for the home folder. The create
// expands ~ again (GetValues), so the value round-trips.
func tildePath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || p == "" {
		return p
	}
	home = filepath.Clean(home)
	if p == home {
		return "~"
	}
	if strings.HasPrefix(p, home+string(os.PathSeparator)) {
		return "~" + p[len(home):]
	}
	return p
}

// SetClaudeHooksUndecided tells the form whether the Claude hooks question is
// still open (no recorded Install/Skip and hooks not present). The tick
// starts on for every opening.
func (d *NewDialog) SetClaudeHooksUndecided(undecided bool) {
	d.hooksUndecided = undecided
	d.hooksTicked = true
	d.rebuildFocusTargets()
}

// showHooksLine reports whether the hooks line is in the form: the question
// is open, the tool is Claude and the session runs on this machine.
func (d *NewDialog) showHooksLine() bool {
	return d.hooksUndecided && !d.createsOffMachine() && d.isClaudeSelected()
}

// HooksDecision returns whether the form asked the hooks question and, if
// so, whether the user left it ticked (install) or not (skip).
func (d *NewDialog) HooksDecision() (asked, install bool) {
	if !d.showHooksLine() {
		return false, false
	}
	return true, d.hooksTicked
}

// advancedShown reports whether the worktree, Docker and multi-repo rows are
// on screen: "More options" is open, or one of them is already on (a config
// default or the w/s/m shortcut), so a live setting is never hidden.
func (d *NewDialog) advancedShown() bool {
	return d.moreOptions || d.worktreeEnabled || d.sandboxEnabled || d.multiRepoEnabled
}

// SetFirstSession marks the form as the profile's first session
// (onboarding): the model rows and the tool's options fold under "More
// options" too, so the form is Name, Agent, Path, the hooks line and
// "More options" (Sharjeel's prototype).
func (d *NewDialog) SetFirstSession(first bool) {
	d.firstSession = first
	d.rebuildFocusTargets()
}

// modelRowsFolded reports whether a first session's model and reasoning
// rows are folded away: "More options" is closed and neither has a value.
func (d *NewDialog) modelRowsFolded() bool {
	return d.firstSession && !d.advancedShown() &&
		strings.TrimSpace(d.modelInput.Value()) == "" && d.reasoningEffort == ""
}

// toolOptionsShown reports whether the tool's options panel is in the form:
// always, except folded for a first session while "More options" is closed.
func (d *NewDialog) toolOptionsShown() bool {
	return !d.firstSession || d.advancedShown()
}

// toggleMoreOptions opens or closes "More options". A folded setting that is
// on keeps its rows visible either way (advancedShown).
func (d *NewDialog) toggleMoreOptions() {
	d.moreOptions = !d.moreOptions
	d.rebuildFocusTargets()
}

// renderHooksLine renders the ticked hooks sentence, wrapped under its box.
func (d *NewDialog) renderHooksLine(focused bool, innerWidth int) string {
	mark := "[ ]"
	if d.hooksTicked {
		mark = "[x]"
	}
	style := lipgloss.NewStyle().Foreground(ColorText)
	prefix := "  "
	if focused {
		style = lipgloss.NewStyle().Foreground(ColorCyan)
		prefix = lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render("▶ ")
	}
	width := innerWidth - 6
	if width < 20 {
		width = 20
	}
	lines := wrapLines(hooksLineText+".", width)
	var b strings.Builder
	for i, l := range lines {
		if i == 0 {
			b.WriteString(prefix + style.Render(mark+" "+l))
		} else {
			b.WriteString("      " + style.Render(l))
		}
		b.WriteString("\n")
	}
	return b.String()
}

// renderMoreOptionsLine renders the "More options" toggle row.
func (d *NewDialog) renderMoreOptionsLine(focused bool) string {
	arrow := "▸"
	if d.advancedShown() {
		arrow = "▾"
	}
	text := arrow + " More options: worktree, Docker sandbox, multi-repo"
	if d.firstSession {
		text = arrow + " More options: model, worktree, Docker, multi-repo"
	}
	if focused {
		return lipgloss.NewStyle().Foreground(ColorCyan).Bold(true).Render("▶ "+text) + "\n"
	}
	return "  " + lipgloss.NewStyle().Foreground(ColorTextDim).Render(text) + "\n"
}

// newDialogInlineLabelWidth is the label column of the one-line Name, Agent
// and Path rows ("Agent: " is the widest).
const newDialogInlineLabelWidth = 7

// newDialogInlineIndent is the label column plus the 2-column marker
// ("▶ " or "  ") in front of it.
const newDialogInlineIndent = 2 + newDialogInlineLabelWidth

// newDialogInlineLabel pads a field label to the inline label column.
func newDialogInlineLabel(label string) string {
	if pad := newDialogInlineLabelWidth - len(label); pad > 0 {
		return label + strings.Repeat(" ", pad)
	}
	return label + " "
}
