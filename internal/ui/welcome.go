package ui

// Welcome empty state (ASK-211, from Sharjeel's onboarding plan and
// prototype): when there are no sessions at all, the main screen itself is
// the welcome. No modal: one box, full width at 80x24, with three actions
// picked with ↑↓ and Enter or their own keys:
//
//	n  New session with <first installed agent> in <this folder>
//	   (opens the New Session form filled in; one more Enter creates it)
//	i  Import your tmux sessions (the existing import)
//	?  Read the 5 keys (help)
//
// While it is on screen the header shows no CPU/RAM/disk/net numbers and
// the Alt+j/k tip is held back: a new user can't use either yet.

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// welcomeAction is one of the welcome's three actions, in screen order.
type welcomeAction int

const (
	welcomeNew welcomeAction = iota
	welcomeImport
	welcomeHelp
	welcomeActionCount
)

// welcomeFolder is the folder the welcome's new session starts in: the
// folder agent-deck was started from (a seam for frame goldens).
var welcomeFolder = onboardingFolder

// welcomeNoticeMsg is a one-line result shown in the welcome box.
type welcomeNoticeMsg struct{ text string }

// welcomeActive reports whether the main screen is the welcome: the first
// load is done, there is no session (local or remote) and no group, and no
// filter or group scope is hiding any.
func (h *Home) welcomeActive() bool {
	if h.initialLoading || len(h.flatItems) != 0 || h.groupScope != "" {
		return false
	}
	if h.embeddedMode && h.embeddedSidebarHidden {
		return false
	}
	if _, filtered := h.filteredEmptyState(); filtered {
		return false
	}
	return !h.hasAnySession()
}

// hasAnySession reports whether any local session exists.
func (h *Home) hasAnySession() bool {
	h.instancesMu.RLock()
	defer h.instancesMu.RUnlock()
	return len(h.instances) > 0
}

// refreshWelcomeAgents makes the welcome detect the agents again on its
// next frame (after the check page, which may have found new ones).
func (h *Home) refreshWelcomeAgents() {
	h.welcomeAgentsKnown = false
}

// welcomeAgentList is the agents the welcome knows about, detected once.
func (h *Home) welcomeAgentList() []onboardingAgent {
	if !h.welcomeAgentsKnown {
		h.welcomeAgents = detectOnboardingAgents()
		h.welcomeAgentsKnown = true
	}
	return h.welcomeAgents
}

// welcomeTool is the agent the welcome's new session starts: the first
// installed agent, or "shell".
func (h *Home) welcomeTool() string {
	return onboardingChoices(h.welcomeAgentList())[0]
}

// handleWelcomeKey runs a welcome key. handled is false for any key the
// welcome does not own, which then goes the usual way.
func (h *Home) handleWelcomeKey(key string) (handled bool, cmd tea.Cmd) {
	switch key {
	case "up", "k":
		h.welcomeSel = (h.welcomeSel + int(welcomeActionCount) - 1) % int(welcomeActionCount)
		return true, nil
	case "down", "j":
		h.welcomeSel = (h.welcomeSel + 1) % int(welcomeActionCount)
		return true, nil
	case "enter":
		return true, h.runWelcomeAction(welcomeAction(h.welcomeSel))
	case defaultHotkeyBindings[hotkeyNewSession]:
		return true, h.runWelcomeAction(welcomeNew)
	case defaultHotkeyBindings[hotkeyImport]:
		return true, h.runWelcomeAction(welcomeImport)
	case defaultHotkeyBindings[hotkeyHelp]:
		return true, h.runWelcomeAction(welcomeHelp)
	}
	return false, nil
}

func (h *Home) runWelcomeAction(a welcomeAction) tea.Cmd {
	h.welcomeSel = int(a)
	h.welcomeNotice = ""
	switch a {
	case welcomeNew:
		return h.openPrefilledNewSession(h.welcomeTool(), welcomeFolder())
	case welcomeImport:
		return func() tea.Msg {
			msg, n := h.importSessionsCounted()
			if _, ok := msg.(importReloadMsg); ok && n == 0 {
				return welcomeNoticeMsg{text: "No tmux sessions to import. Start one with " + h.actionKey(hotkeyNewSession) + "."}
			}
			return msg
		}
	case welcomeHelp:
		h.helpOverlay.SetEmbeddedLayout(h.embeddedLayout)
		h.helpOverlay.SetSize(h.width, h.height)
		h.helpOverlay.Show()
	}
	return nil
}

// openPrefilledNewSession opens the local New Session form on folder with
// tool picked ("shell" for a plain shell), so one more Enter creates it.
// Local on purpose: the welcome says "in <this folder>".
func (h *Home) openPrefilledNewSession(tool, folder string) tea.Cmd {
	cmd := h.openLocalNewSessionDialog()
	if h.newDialog.IsVisible() {
		h.newDialog.SetPath(folder)
		if tool == "shell" {
			h.newDialog.SetDefaultTool("")
		} else {
			h.newDialog.SetDefaultTool(tool)
		}
	}
	return cmd
}

// renderWelcome draws the welcome box centred in width x height.
func (h *Home) renderWelcome(width, height int) string {
	title := lipgloss.NewStyle().Bold(true).Foreground(ColorCyan)
	text := lipgloss.NewStyle().Foreground(ColorText)
	dim := lipgloss.NewStyle().Foreground(ColorTextDim)
	keyStyle := lipgloss.NewStyle().Foreground(ColorPurple)
	warn := lipgloss.NewStyle().Foreground(ColorYellow)
	ok := lipgloss.NewStyle().Foreground(ColorGreen)

	boxW := 76
	if width-6 < boxW {
		boxW = width - 6
	}
	if boxW < 30 {
		boxW = width
	}
	inner := boxW - 2 - 4 // border, padding

	agents := h.welcomeAgentList()
	tool := h.welcomeTool()
	agent := onboardingDisplayOf(agents, tool)

	label := func(k string) string {
		if k == "" {
			return "·"
		}
		return k
	}
	acts := []struct{ key, what string }{
		{label(h.actionKey(hotkeyNewSession)), "New session with " + agent + " in "},
		{label(h.actionKey(hotkeyImport)), "Import your tmux sessions"},
		{label(h.actionKey(hotkeyHelp)), "Read the 5 keys"},
	}
	const keyW = 7
	textW := inner - 2 - keyW
	// The folder gives way first, from its left end, so the agent and the
	// end of the path always show.
	folder := displayFolder(welcomeFolder())
	acts[0].what += truncateDisplayLeft(folder, textW-lipgloss.Width(acts[0].what), "…")

	var rows []string
	for i, a := range acts {
		sel := i == h.welcomeSel
		marker, ks, ts := "  ", keyStyle, text
		if sel {
			marker = lipgloss.NewStyle().Foreground(ColorAccent).Render("▶ ")
			ks, ts = keyStyle.Bold(true), text.Bold(true)
		}
		line := marker + ks.Render(padRight(a.key, keyW)) + ts.Render(truncateDisplay(a.what, textW, "…"))
		if sel {
			line = lipgloss.NewStyle().Background(ColorSurface).Width(inner).Render(line)
		}
		rows = append(rows, line)
	}

	about := text.Render(onboardingWrap(inner, "Run many AI coding agents at once, each in its own session. Switch between them with one key."))

	var tail []string
	if tool == "shell" {
		hint := "No agent found yet."
		for _, a := range agents {
			if a.Name == "claude" && a.Install != "" {
				hint += " Install Claude Code: " + a.Install
			}
		}
		tail = append(tail, "", warn.Render(onboardingWrap(inner, hint)))
	}
	if h.welcomeNotice != "" {
		tail = append(tail, "", ok.Render(onboardingWrap(inner, h.welcomeNotice)))
	}
	tail = append(tail, "", dim.Render(truncateDisplay("↑↓ pick · Enter do it · q quit", inner, "…")))

	build := func(withAbout bool, padY int) string {
		lines := []string{title.Render("Welcome to Agent Deck"), ""}
		if withAbout {
			lines = append(lines, about, "")
		}
		lines = append(lines, title.Render("Start here"))
		lines = append(lines, rows...)
		lines = append(lines, tail...)
		return lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorBorder).
			Padding(padY, 2).
			Width(boxW - 2).
			Render(strings.Join(lines, "\n"))
	}
	box := build(true, 1)
	if lipgloss.Height(box) > height {
		box = build(true, 0)
	}
	if lipgloss.Height(box) > height {
		box = build(false, 0)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}
