// Plugin Manager dialog (RFC docs/rfc/PLUGIN_ATTACH.md). Hotkey `l`
// from home opens this for the selected claude session. Apply writes
// through session.SetField(FieldPlugins) for shared validation.

package ui

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

type pluginDialogItem struct {
	name        string
	id          string
	description string
	emitsChan   bool
	autoInstall bool
	enabled     bool
}

type PluginDialog struct {
	visible bool
	width   int
	height  int

	sessionID string
	tool      string

	items  []pluginDialogItem
	cursor int

	// Snapshot of enabled state at Show; used by HasChanged to gate restart.
	initialEnabled map[string]bool

	channelLinkDisabled bool

	configError string
	err         error
}

func NewPluginDialog() *PluginDialog { return &PluginDialog{} }

func (d *PluginDialog) Show(inst *session.Instance) error {
	d.configError = ""
	d.err = nil
	if _, err := session.ReloadUserConfig(); err != nil {
		d.configError = err.Error()
	}

	d.sessionID = inst.ID
	d.tool = inst.Tool
	d.channelLinkDisabled = inst.PluginChannelLinkDisabled
	d.cursor = 0

	avail := session.GetAvailablePlugins()
	names := session.GetAvailablePluginNames()

	enabledSet := map[string]bool{}
	for _, n := range inst.Plugins {
		enabledSet[n] = true
	}
	d.initialEnabled = make(map[string]bool, len(names))

	d.items = make([]pluginDialogItem, 0, len(names))
	for _, name := range names {
		def := avail[name]
		d.items = append(d.items, pluginDialogItem{
			name:        name,
			id:          def.ID(),
			description: def.Description,
			emitsChan:   def.EmitsChannel,
			autoInstall: def.AutoInstall,
			enabled:     enabledSet[name],
		})
		d.initialEnabled[name] = enabledSet[name]
	}

	d.visible = true
	return nil
}

func (d *PluginDialog) Hide()           { d.visible = false }
func (d *PluginDialog) IsVisible() bool { return d != nil && d.visible }
func (d *PluginDialog) GetSessionID() string {
	if d == nil {
		return ""
	}
	return d.sessionID
}
func (d *PluginDialog) SetSize(w, h int) { d.width, d.height = w, h }

func (d *PluginDialog) HasChanged() bool {
	if d == nil || len(d.items) == 0 {
		return false
	}
	for _, it := range d.items {
		if d.initialEnabled[it.name] != it.enabled {
			return true
		}
	}
	return false
}

func (d *PluginDialog) SelectedPluginNames() []string {
	out := make([]string, 0, len(d.items))
	for _, it := range d.items {
		if it.enabled {
			out = append(out, it.name)
		}
	}
	sort.Strings(out)
	return out
}

func (d *PluginDialog) Update(msg tea.KeyMsg) (*PluginDialog, tea.Cmd) {
	if !d.visible {
		return d, nil
	}
	key := msg.String()
	switch key {
	case "esc":
		d.Hide()
	case "down", "j", "tab":
		if len(d.items) > 0 {
			d.cursor = (d.cursor + 1) % len(d.items)
		}
	case "up", "k", "shift+tab":
		if len(d.items) > 0 {
			d.cursor = (d.cursor - 1 + len(d.items)) % len(d.items)
		}
	case " ", "space", "x":
		if len(d.items) > 0 {
			d.items[d.cursor].enabled = !d.items[d.cursor].enabled
		}
	case "g":
		d.cursor = 0
	case "G":
		if len(d.items) > 0 {
			d.cursor = len(d.items) - 1
		}
	case "enter":
		// Pure-UI layer: caller (home view) reads SelectedPluginNames + HasChanged.
	}
	return d, nil
}

func (d *PluginDialog) View() string {
	if !d.visible {
		return ""
	}
	width := d.width
	if width > 70 || width == 0 {
		width = 70
	}

	header := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13")).Render("Plugin Manager")
	hint := lipgloss.NewStyle().Faint(true).Render("space toggle · ↑/↓ move · enter apply · esc cancel")

	var body strings.Builder
	body.WriteString(header)
	body.WriteString("\n")
	body.WriteString(hint)
	body.WriteString("\n\n")

	if d.configError != "" {
		body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("config error: " + d.configError))
		body.WriteString("\n\n")
	}

	if len(d.items) == 0 {
		body.WriteString(lipgloss.NewStyle().Faint(true).Render(
			"No plugins in catalog. Add [plugins.<name>] tables to ~/.agent-deck/config.toml\n" +
				"See docs/rfc/PLUGIN_ATTACH.md §4.1",
		))
		body.WriteString("\n")
		return panelBox(body.String(), width)
	}

	for i, it := range d.items {
		mark := "[ ]"
		if it.enabled {
			mark = "[x]"
		}
		cursor := "  "
		nameStyle := lipgloss.NewStyle()
		if i == d.cursor {
			cursor = "▶ "
			nameStyle = nameStyle.Bold(true).Foreground(lipgloss.Color("12"))
		}
		var tags []string
		if it.emitsChan {
			tags = append(tags, "channel")
		}
		if it.autoInstall {
			tags = append(tags, "auto-install")
		}
		tagSuffix := ""
		if len(tags) > 0 {
			tagSuffix = " " + lipgloss.NewStyle().Faint(true).Render("["+strings.Join(tags, " ")+"]")
		}
		body.WriteString(fmt.Sprintf("%s%s %s%s\n", cursor, mark, nameStyle.Render(it.name), tagSuffix))
		idLine := lipgloss.NewStyle().Faint(true).Render("       " + it.id)
		body.WriteString(idLine)
		body.WriteString("\n")
		if it.description != "" {
			descLine := lipgloss.NewStyle().Faint(true).Render("       " + it.description)
			body.WriteString(descLine)
			body.WriteString("\n")
		}
	}

	if d.channelLinkDisabled {
		body.WriteString("\n")
		body.WriteString(lipgloss.NewStyle().Faint(true).Render("(auto-channel-link disabled — RFC §4.7)"))
		body.WriteString("\n")
	}

	if d.HasChanged() {
		body.WriteString("\n")
		body.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(
			"changes pending — press Enter to apply (session will restart if running)",
		))
		body.WriteString("\n")
	}

	return panelBox(body.String(), width)
}

func panelBox(content string, width int) string {
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("13")).
		Padding(0, 1).
		Width(width).
		Render(content)
}
