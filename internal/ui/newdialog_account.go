package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The Account field of the new-session dialog.
//
// This is the priority surface of account selection: everywhere else is a flag
// or a config key, but this is where a human with several logins actually picks
// one, at the moment the session is born.
//
// Two rules shape it:
//
//   - OPT-IN BY PRESENCE. The field exists only when the currently-selected
//     tool has at least one configured account ([profiles.<name>.claude] for a
//     claude-compatible tool, [profiles.<name>.codex] for codex, …). A user
//     with no accounts configured never sees it, and the dialog is
//     byte-identical to the pre-feature one.
//
//   - TOOL-SCOPED. The list is rebuilt whenever the tool changes, so a codex
//     session is never offered a claude account. Switching claude -> codex
//     re-resolves the default instead of carrying an incompatible pick over.

// accountDefaultLabel is the "no account" entry — the tool's own default home,
// which is what a session got before this feature existed.
const accountDefaultLabel = "default"

// refreshAccounts rebuilds the account choices for the currently-selected tool
// and re-resolves the default selection. Called from updateToolOptions, so it
// runs on every tool change and on every show.
func (d *NewDialog) refreshAccounts() {
	tool := d.GetSelectedCommand()
	previous := d.SelectedAccount()

	cfg, err := session.LoadUserConfig()
	if err != nil {
		cfg = nil
	}
	d.accounts = session.AccountsForTool(cfg, tool)
	d.accountCursor = 0
	if len(d.accounts) == 0 {
		return
	}

	// Keep an explicit pick across a tool change when the new tool also has
	// that account — a user with `work` configured for both claude and codex
	// means the same person either way.
	if previous != "" {
		for i, a := range d.accounts {
			if a.Name == previous {
				d.accountCursor = i + 1 // +1: 0 is the "default" entry
				return
			}
		}
	}

	// Otherwise resolve the configured default for this group/tool.
	res := session.ResolveSessionAccount(cfg, "", tool, d.parentGroupPath, d.selectedConductorName())
	if res.Account == "" {
		return
	}
	for i, a := range d.accounts {
		if a.Name == res.Account {
			d.accountCursor = i + 1
			return
		}
	}
}

// selectedConductorName returns the conductor name currently picked in the
// Conducting-parent field, or "" for None. Feeds the account default
// resolution so picking a conductor also picks that conductor's account.
func (d *NewDialog) selectedConductorName() string {
	if d.conductorCursor == 0 || d.conductorCursor > len(d.conductorSessions) {
		return ""
	}
	inst := d.conductorSessions[d.conductorCursor-1]
	if inst == nil {
		return ""
	}
	name := strings.TrimPrefix(inst.Title, "conductor-")
	if name == inst.Title {
		return ""
	}
	return name
}

// hasAccountField reports whether the Account field is active — the
// opt-in-by-presence gate consulted by focus, key handling, and rendering.
func (d *NewDialog) hasAccountField() bool {
	return len(d.accounts) > 0
}

// SelectedAccount returns the chosen account name, or "" for the tool default.
func (d *NewDialog) SelectedAccount() string {
	if d.accountCursor <= 0 || d.accountCursor > len(d.accounts) {
		return ""
	}
	return d.accounts[d.accountCursor-1].Name
}

// accountChoiceCount is the number of selectable entries: "default" plus one
// per configured account.
func (d *NewDialog) accountChoiceCount() int {
	return len(d.accounts) + 1
}

// nextAccount / prevAccount move the selection, clamped at both ends (the same
// non-wrapping behaviour the conductor selector has).
func (d *NewDialog) nextAccount() {
	if d.accountCursor < d.accountChoiceCount()-1 {
		d.accountCursor++
	}
}

func (d *NewDialog) prevAccount() {
	if d.accountCursor > 0 {
		d.accountCursor--
	}
}

// renderAccountSection renders the Account pill row. No-op when the selected
// tool has no configured accounts.
func (d *NewDialog) renderAccountSection(content *strings.Builder, cur focusTarget) {
	if !d.hasAccountField() {
		return
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	activeLabelStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
	if cur == focusAccount {
		content.WriteString(activeLabelStyle.Render("▶ Account:"))
	} else {
		content.WriteString(labelStyle.Render("  Account:"))
	}
	content.WriteString("\n  ")

	selectedStyle := lipgloss.NewStyle().Foreground(ColorBg).Background(ColorAccent).Bold(true).Padding(0, 2)
	idleStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Background(ColorSurface).Padding(0, 2)

	pills := make([]string, 0, d.accountChoiceCount())
	for i := 0; i < d.accountChoiceCount(); i++ {
		label := accountDefaultLabel
		if i > 0 {
			acct := d.accounts[i-1]
			label = acct.Name
			// A logged-out home is the single most useful thing to surface
			// here: picking it produces a session that opens straight onto a
			// login prompt. Marked, never hidden — the user may be about to
			// log in deliberately.
			if acct.Login == session.AccountLoginOut {
				label += " ⚠"
			}
		}
		if i == d.accountCursor {
			pills = append(pills, selectedStyle.Render(label))
		} else {
			pills = append(pills, idleStyle.Render(label))
		}
	}
	content.WriteString(lipgloss.JoinHorizontal(lipgloss.Left, pills...))
	content.WriteString("\n")

	// Detail line for the current pick: the env var and the home it points at
	// is the whole mechanism, so showing it removes any doubt about what the
	// session will actually run against.
	hintStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
	content.WriteString("  ")
	content.WriteString(hintStyle.Render(d.accountHintLine()))
	content.WriteString("\n\n")
}

// accountHintLine describes the current selection in one line.
func (d *NewDialog) accountHintLine() string {
	if d.accountCursor == 0 {
		if len(d.accounts) > 0 {
			return "tool default home (" + d.accounts[0].EnvVar + " unchanged)"
		}
		return "tool default home"
	}
	acct := d.accounts[d.accountCursor-1]
	line := acct.EnvVar + "=" + acct.Home
	switch {
	case !acct.HomeExists:
		line += "  (created on launch)"
	case acct.Login == session.AccountLoginOut:
		line += "  (not logged in yet)"
	}
	return line
}
