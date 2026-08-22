package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// AutomaticUpdatePrompt is the reusable onboarding/tour seam for the strictly
// opt-in updates.auto_install choice. First-run and the four-step tour must use
// this component rather than growing subtly different prompt/config behavior.
type AutomaticUpdatePrompt struct{ enabled bool }

func NewAutomaticUpdatePrompt() *AutomaticUpdatePrompt { return &AutomaticUpdatePrompt{} }
func (p *AutomaticUpdatePrompt) Enabled() bool         { return p.enabled }

// HandleKey completes on one keypress. No/Enter is the safe default; Y opts in.
func (p *AutomaticUpdatePrompt) HandleKey(key string) bool {
	switch strings.ToLower(key) {
	case "y":
		p.enabled = true
		return true
	case "n", "enter":
		p.enabled = false
		return true
	}
	return false
}

func (p *AutomaticUpdatePrompt) View(title, label, hint lipgloss.Style) string {
	return title.Render("Enable automatic updates?") + "\n\n" +
		label.Render("Verified standalone releases can be installed in the background. [y/N]") + "\n\n" +
		hint.Render("Change later with updates.auto_install in config.toml.")
}
