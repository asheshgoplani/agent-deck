package ui

import (
	"fmt"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/git"
)

// hookTrustRequestMsg asks the TUI to show the hook trust dialog. reply
// receives exactly one decision.
type hookTrustRequestMsg struct {
	id    git.WorktreeScriptIdentity
	reply chan<- git.ScriptConsentDecision
}

// NewHookTrustPrompter returns the git.ScriptConsentPrompter the TUI
// installs. The git layer calls it from the background goroutine doing the
// worktree work; it posts a dialog request to the program and blocks until
// the user answers. Requests are serialized so only one dialog is open.
func NewHookTrustPrompter(send func(tea.Msg)) git.ScriptConsentPrompter {
	var mu sync.Mutex
	return func(id git.WorktreeScriptIdentity) git.ScriptConsentDecision {
		mu.Lock()
		defer mu.Unlock()
		reply := make(chan git.ScriptConsentDecision, 1)
		send(hookTrustRequestMsg{id: id, reply: reply})
		return <-reply
	}
}

// hookTrustChoices are the dialog buttons, in display order.
var hookTrustChoices = []struct {
	label    string
	decision git.ScriptConsentDecision
}{
	{"Run once", git.ScriptConsentRunOnce},
	{"Always trust this version", git.ScriptConsentTrust},
	{"Skip", git.ScriptConsentSkip},
}

const hookTrustSkipIndex = 2

// HookTrustDialog asks whether to run a repository's worktree hook that is
// new or changed since it was approved. It shows the repo, the hook, the
// effective command, the sha256 and the first lines of the script. Enter on
// the default focus (Skip) never runs anything.
type HookTrustDialog struct {
	visible bool
	id      git.WorktreeScriptIdentity
	reply   chan<- git.ScriptConsentDecision
	focused int
	width   int
	height  int
}

func NewHookTrustDialog() *HookTrustDialog { return &HookTrustDialog{} }

// Show opens the dialog for id; the answer is sent on reply.
func (d *HookTrustDialog) Show(id git.WorktreeScriptIdentity, reply chan<- git.ScriptConsentDecision) {
	d.visible = true
	d.id = id
	d.reply = reply
	d.focused = hookTrustSkipIndex
}

// IsVisible is nil-safe: Home values built without NewHome (tests) have no dialog.
func (d *HookTrustDialog) IsVisible() bool { return d != nil && d.visible }

func (d *HookTrustDialog) SetSize(width, height int) {
	if d == nil {
		return
	}
	d.width = width
	d.height = height
}

// answer delivers decision to the waiting prompter and closes the dialog.
func (d *HookTrustDialog) answer(decision git.ScriptConsentDecision) {
	if d.reply != nil {
		d.reply <- decision
	}
	d.visible = false
	d.reply = nil
}

// HandleKey processes a key while the dialog is open.
func (d *HookTrustDialog) HandleKey(msg tea.KeyMsg) {
	switch msg.String() {
	case "o", "1":
		d.answer(git.ScriptConsentRunOnce)
	case "a", "2":
		d.answer(git.ScriptConsentTrust)
	case "s", "n", "3", "esc", "q", "ctrl+c":
		d.answer(git.ScriptConsentSkip)
	case "left", "h", "shift+tab":
		if d.focused > 0 {
			d.focused--
		}
	case "right", "l", "tab":
		if d.focused < len(hookTrustChoices)-1 {
			d.focused++
		}
	case "enter":
		d.answer(hookTrustChoices[d.focused].decision)
	}
}

// View renders the dialog centered on screen.
func (d *HookTrustDialog) View() string {
	if !d.IsVisible() {
		return ""
	}
	id := d.id
	dialogWidth := fitDialogWidth(84, 40, d.width)
	inner := dialogWidth - 6

	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorYellow).MarginBottom(1).
		Render(fmt.Sprintf("Run this repository's worktree %s hook?", id.Kind))
	intro := lipgloss.NewStyle().Foreground(ColorText).Width(inner).MarginBottom(1).
		Render("It is new or changed since you last approved it. It runs as you, with your full environment.")

	var facts strings.Builder
	fmt.Fprintf(&facts, "repo     %s\n", id.RepoRoot)
	fmt.Fprintf(&facts, "hook     %s\n", id.ScriptPath)
	if id.ResolvedPath != id.ScriptPath {
		fmt.Fprintf(&facts, "target   %s\n", id.ResolvedPath)
	}
	fmt.Fprintf(&facts, "command  %s\n", id.CommandLine())
	fmt.Fprintf(&facts, "sha256   %s", id.SHA256)
	factsBlock := lipgloss.NewStyle().Foreground(ColorText).Width(inner).MarginBottom(1).Render(facts.String())

	// Preview lines are already sanitized by the git layer; truncate to
	// the box width instead of wrapping so line boundaries stay obvious.
	var preview strings.Builder
	for i, l := range id.Preview {
		if i > 0 {
			preview.WriteString("\n")
		}
		preview.WriteString(ansi.Truncate("│ "+l, inner, "…"))
	}
	if more := id.TotalLines - len(id.Preview); more > 0 {
		fmt.Fprintf(&preview, "\n│ … %d more lines", more)
	}
	previewBlock := dim.Width(inner).Render(preview.String())

	buttons := make([]string, 0, len(hookTrustChoices))
	colors := []lipgloss.Color{ColorYellow, ColorRed, ColorAccent}
	for i, c := range hookTrustChoices {
		style := lipgloss.NewStyle().Bold(true).Padding(0, 1)
		label := "  " + c.label
		if i == d.focused {
			style = style.Foreground(ColorBg).Background(colors[i])
			label = "▸ " + c.label
		} else {
			style = style.Foreground(colors[i])
		}
		buttons = append(buttons, style.Render(label))
	}
	buttonRow := strings.Join(buttons, " ")
	hint := dim.Render(glueHintGroups("o once · a always · s skip · ←/→ navigate · Enter select · Esc skip"))

	content := lipgloss.JoinVertical(lipgloss.Left, title, intro, factsBlock, previewBlock, "", buttonRow, hint)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorYellow).
		Padding(1, 2).
		Width(dialogWidth).
		Render(content)

	if d.width <= 0 || d.height <= 0 {
		return box
	}
	return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, box)
}
