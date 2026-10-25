package ui

import (
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/provider"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// whereKind is where a new session from a local opening of the dialog runs.
type whereKind int

const (
	whereLocal   whereKind = iota // this machine (default, the unchanged path)
	whereRemote                   // an already configured remote
	whereSandbox                  // a new sandbox from a configured provider
)

// whereChoice is one entry of the dialog's "Where" row. name is the remote
// (whereRemote) or the provider (whereSandbox).
type whereChoice struct {
	kind  whereKind
	name  string
	label string
}

// newDialogWhereChoices lists "this machine", every configured remote (a
// sandbox remote labelled with provider.DisplayKind) and one "new sandbox"
// entry per configured provider. Only "this machine" means nothing else is
// configured, and the row stays hidden.
func newDialogWhereChoices(cfg *session.UserConfig) []whereChoice {
	choices := []whereChoice{{kind: whereLocal, label: "this machine"}}
	if cfg == nil {
		return choices
	}
	names := make([]string, 0, len(cfg.Remotes))
	for name := range cfg.Remotes {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		label := name
		if kind := provider.DisplayKind(cfg.Remotes[name]); kind != "" {
			label += " · " + kind
		}
		choices = append(choices, whereChoice{kind: whereRemote, name: name, label: label})
	}
	for _, p := range provider.Names(cfg) {
		choices = append(choices, whereChoice{kind: whereSandbox, name: p, label: "new sandbox (" + p + ")"})
	}
	return choices
}

// SetWhereChoices installs the Where row's entries for a local opening and
// selects "this machine". Call it after Show/ShowInGroup, which clear them.
func (d *NewDialog) SetWhereChoices(choices []whereChoice) {
	d.whereChoices = choices
	d.whereCursor = 0
	d.rebuildFocusTargets()
}

// clearWhere drops the Where row (every new opening starts without it).
func (d *NewDialog) clearWhere() {
	d.whereChoices = nil
	d.whereCursor = 0
}

// hasWhereRow reports whether the Where row is shown: a local opening with
// something besides this machine to pick. A dialog opened on a remote row
// already has its target.
func (d *NewDialog) hasWhereRow() bool {
	return !d.remoteTarget && len(d.whereChoices) > 1
}

// GetWhere returns the selected Where entry; "this machine" when the row is
// hidden.
func (d *NewDialog) GetWhere() whereChoice {
	if !d.hasWhereRow() || d.whereCursor < 0 || d.whereCursor >= len(d.whereChoices) {
		return whereChoice{kind: whereLocal, label: "this machine"}
	}
	return d.whereChoices[d.whereCursor]
}

// createsOffMachine reports whether submitting creates the session
// somewhere other than this machine: a dialog opened on a remote row, or a
// remote or new sandbox picked in the Where row. Such creates need a typed
// name (the folder-name default of ASK-110 is local only).
func (d *NewDialog) createsOffMachine() bool {
	return d.remoteTarget || d.GetWhere().kind != whereLocal
}

func (d *NewDialog) cycleWhere(delta int) {
	n := len(d.whereChoices)
	if n == 0 {
		return
	}
	d.whereCursor = ((d.whereCursor+delta)%n + n) % n
	d.ClearError()
}

// GetWhereRemoteCreateOptions collects what a session created on a remote
// picked in the Where row carries. The dialog was filled from this
// machine's config, so only what the user typed travels (name, tool, path,
// worktree branch); the remote applies its own defaults for the rest, as
// ResetRemoteDefaults arranges for a dialog opened on a remote row.
func (d *NewDialog) GetWhereRemoteCreateOptions() session.RemoteAddOptions {
	name, path, command := d.GetRemoteValues()
	opts := session.RemoteAddOptions{Tool: command, Title: name, Path: path}
	if d.worktreeEnabled {
		opts.WorktreeBranch = strings.TrimSpace(d.branchInput.Value())
	}
	return opts
}

func (d *NewDialog) renderWhereSection(content *strings.Builder, cur focusTarget) {
	if !d.hasWhereRow() {
		return
	}
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	valueStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	value := d.GetWhere().label
	if cur == focusWhere {
		content.WriteString(activeLabelStyle.Render("▶ Where:"))
		content.WriteString(" ")
		content.WriteString(activeLabelStyle.Render("← " + value + " →"))
	} else {
		content.WriteString(labelStyle.Render("  Where:"))
		content.WriteString(" ")
		content.WriteString(valueStyle.Render(value))
	}
	content.WriteString("\n\n")
}
