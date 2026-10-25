package ui

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// renderSectionDivider creates a modern section divider with optional centered label
// Format: ─────────── Label ─────────── (lines extend to fill width)
func renderSectionDivider(label string, width int) string {
	lineStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	if label == "" {
		return lineStyle.Render(strings.Repeat("─", max(0, width)))
	}

	// Label with subtle background for better visibility
	labelStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)

	// Calculate side widths
	labelWidth := len(label) + 2 // +2 for spacing on each side of label
	sideWidth := (width - labelWidth) / 2
	if sideWidth < 3 {
		sideWidth = 3
	}

	return lineStyle.Render(strings.Repeat("─", sideWidth)) +
		" " + labelStyle.Render(label) + " " +
		lineStyle.Render(strings.Repeat("─", sideWidth))
}

// renderToolStatusLine renders a Status + Session line for a tool section.
// sessionID is the detected session ID (empty = not connected).
// detectedAt is when detection ran (zero = still detecting, used only when threeState is true).
// threeState enables the "Detecting..." intermediate state (for tools like OpenCode/Codex).
// archived/status drive the honest connected-vs-archived-vs-stopped label when a
// session id is on record (the id outlives the live pane).
func renderToolStatusLine(b *strings.Builder, sessionID string, detectedAt time.Time, threeState, archived bool, status session.Status) {
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	if sessionID != "" {
		statusText, statusStyle := connectionStatusLine(archived, status)
		b.WriteString(labelStyle.Render("Status:  "))
		b.WriteString(statusStyle.Render(statusText))
		b.WriteString("\n")

		b.WriteString(labelStyle.Render("Session: "))
		b.WriteString(valueStyle.Render(sessionID))
		b.WriteString("\n")
	} else if threeState && detectedAt.IsZero() {
		statusStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString(labelStyle.Render("Status:  "))
		b.WriteString(statusStyle.Render("◐ Detecting session..."))
		b.WriteString("\n")
	} else {
		statusStyle := lipgloss.NewStyle().Foreground(ColorText)
		b.WriteString(labelStyle.Render("Status:  "))
		if threeState {
			b.WriteString(statusStyle.Render("○ No session found"))
		} else {
			b.WriteString(statusStyle.Render("○ Not connected"))
		}
		b.WriteString("\n")
	}
}

// renderDetectedAtLine renders a "Detected: X ago" line.
func renderDetectedAtLine(b *strings.Builder, detectedAt time.Time) {
	if detectedAt.IsZero() {
		return
	}
	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	dimStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	b.WriteString(labelStyle.Render("Detected:"))
	b.WriteString(dimStyle.Render(" " + formatRelativeTime(detectedAt)))
	b.WriteString("\n")
}

// renderLaunchModelInfoLines renders the per-session model/version override,
// or an explicit tool-default marker when the tool supports model selection.
func renderLaunchModelInfoLines(b *strings.Builder, inst *session.Instance) {
	if inst == nil || !session.SupportsLaunchModel(inst.Tool) {
		return
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorAccent)
	dimStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)

	info := inst.LaunchModelInfo()
	if info.ModelID == "" {
		b.WriteString(labelStyle.Render("Model:   "))
		b.WriteString(dimStyle.Render("tool default"))
		b.WriteString("\n")
		return
	}

	model := info.Model
	if model == "" {
		model = info.ModelID
	}
	b.WriteString(labelStyle.Render("Model:   "))
	b.WriteString(valueStyle.Render(model))
	b.WriteString("\n")

	if info.Version != "" {
		b.WriteString(labelStyle.Render("Version: "))
		b.WriteString(valueStyle.Render(info.Version))
		b.WriteString("\n")
	}

	b.WriteString(labelStyle.Render("Model ID:"))
	b.WriteString(valueStyle.Render(" " + info.ModelID))
	b.WriteString("\n")
}

// renderForkHintLine renders the fork keyboard hint line.
func (h *Home) renderForkHintLine(b *strings.Builder) {
	quickForkKey := h.actionKey(hotkeyQuickFork)
	forkWithOptionsKey := h.actionKey(hotkeyForkWithOptions)
	if quickForkKey == "" && forkWithOptionsKey == "" {
		return
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
	keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	b.WriteString(hintStyle.Render("Fork:    "))
	if quickForkKey != "" {
		b.WriteString(keyStyle.Render(quickForkKey))
		b.WriteString(hintStyle.Render(" quick fork"))
	}
	if quickForkKey != "" && forkWithOptionsKey != "" {
		b.WriteString(hintStyle.Render(", "))
	}
	if forkWithOptionsKey != "" {
		b.WriteString(keyStyle.Render(forkWithOptionsKey))
		b.WriteString(hintStyle.Render(" fork with options"))
	}
	b.WriteString("\n")
}

// renderSimpleMCPLine renders MCPs without sync status (for Gemini and other tools).
// Width-aware truncation shows "(+N more)" when MCPs don't fit.
func renderSimpleMCPLine(b *strings.Builder, mcpInfo *session.MCPInfo, width int) {
	if mcpInfo == nil || !mcpInfo.HasAny() {
		return
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorText)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	var mcpParts []string
	for _, name := range mcpInfo.Global {
		mcpParts = append(mcpParts, valueStyle.Render(name+" (g)"))
	}
	for _, name := range mcpInfo.Project {
		mcpParts = append(mcpParts, valueStyle.Render(name+" (p)"))
	}
	for _, mcp := range mcpInfo.LocalMCPs {
		mcpParts = append(mcpParts, valueStyle.Render(mcp.Name+" (l)"))
	}

	if len(mcpParts) == 0 {
		return
	}

	b.WriteString(labelStyle.Render("MCPs:    "))

	mcpMaxWidth := width - 4 - 9
	if mcpMaxWidth < 20 {
		mcpMaxWidth = 20
	}

	var mcpResult strings.Builder
	mcpCount := 0
	currentWidth := 0

	for i, part := range mcpParts {
		plainPart := tmux.StripANSI(part)
		// #937 v2: cellWidth promotes keycap clusters (#️⃣ 0️⃣–9️⃣ *️⃣) to 2
		// cells; ansi.StringWidth reports them at 1 and let MCP rows drift
		// past the right edge — see internal/ui/cellwidth.go for the
		// uniseg/terminal disagreement that motivates this shim.
		partWidth := cellWidth(plainPart)

		addedWidth := partWidth
		if mcpCount > 0 {
			addedWidth += 2
		}

		remaining := len(mcpParts) - i
		isLast := remaining == 1

		var wouldExceed bool
		if isLast {
			wouldExceed = currentWidth+addedWidth > mcpMaxWidth
		} else {
			moreIndicator := fmt.Sprintf(" (+%d more)", remaining)
			moreWidth := cellWidth(moreIndicator)
			wouldExceed = currentWidth+addedWidth+moreWidth > mcpMaxWidth
		}

		if wouldExceed {
			moreStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
			if mcpCount > 0 {
				mcpResult.WriteString(moreStyle.Render(fmt.Sprintf(" (+%d more)", remaining)))
			} else {
				mcpResult.WriteString(moreStyle.Render(fmt.Sprintf("(%d MCPs)", len(mcpParts))))
			}
			break
		}

		if mcpCount > 0 {
			mcpResult.WriteString(", ")
		}
		mcpResult.WriteString(part)
		currentWidth += addedWidth
		mcpCount++
	}

	b.WriteString(mcpResult.String())
	b.WriteString("\n")
}

func (h *Home) renderRemotePreview(item session.Item, width, height int) string {
	if item.Type == session.ItemTypeRemoteGroup {
		h.remoteSessionsMu.RLock()
		sessions := h.remoteSessionsInView(item.RemoteName)
		count := len(sessions)
		h.remoteSessionsMu.RUnlock()

		config, _ := session.LoadUserConfig()
		host := item.RemoteName
		if config != nil && config.Remotes != nil {
			if rc, ok := config.Remotes[item.RemoteName]; ok {
				host = rc.Host
			}
		}

		versionState, _ := h.remoteVersionState(item.RemoteName)
		statsResult, hasStats := h.remoteHostStatsState(item.RemoteName)
		h.remoteSessionsMu.RLock()
		poll := h.remotePolls[item.RemoteName]
		h.remoteSessionsMu.RUnlock()
		fields := session.DefaultRemotePreviewFields
		if config != nil {
			fields = config.UI.GetRemotePreviewFields()
		}
		state := EmptyStateConfig{
			Icon:     "⬡",
			Title:    "Remote: " + item.RemoteName,
			Subtitle: fmt.Sprintf("Host: %s — %d sessions", host, count),
			Hints:    []string{"Press Enter on a session to attach via SSH"},
		}
		state.Body = remotePreviewFieldLines(versionState, Version, sessions, statsResult, hasStats, fields, time.Now(), emptyStateBodyLayout(state, width, height))
		if poll.LastPollError != "" {
			state.Subtitle = ""
			state.Body = append([]string{
				ErrorStyle.Render("Unreachable: " + poll.LastPollError),
				fmt.Sprintf("Host: %s · %d sessions", host, count),
			}, state.Body...)
		}
		return renderEmptyStateResponsive(state, width, height)
	}

	// Remote session preview
	rs := item.RemoteSession
	if rs == nil {
		return ""
	}

	var b strings.Builder
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(nameStyle.Render(rs.Title))
	b.WriteString("  ")

	statusIcon, statusStyle := remoteRowStatusGlyph(rs.Status, rs.Substate, rs.Archived)
	// The archived override swaps the glyph to ■ regardless of the stale live
	// Status, so the label has to follow it or the row reads "■ running".
	statusLabel := processExitLabel(rs.Status, session.Substate(rs.Substate), rs.ExitCode)
	if rs.Archived {
		statusLabel = "archived"
	}
	// The preview mirrors the list row's freshness rule: it must never show
	// a green "● running" for a remote whose poll failed or whose snapshot
	// is stale (status-detection audit 2026-09-23).
	if h.remotePollUnavailable(item.RemoteName) {
		statusIcon, statusStyle = "?", DimStyle
		statusLabel += " (last known)"
	} else if age, stale := h.remoteRowStale(item.RemoteName); stale {
		statusStyle = DimStyle
		statusLabel += " (status " + formatRemoteAge(age) + " old)"
	}
	b.WriteString(statusStyle.Render(statusIcon + " " + statusLabel))
	b.WriteString("\n\n")

	dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	b.WriteString(dimStyle.Render("Remote:  ") + item.RemoteName + "\n")
	b.WriteString(dimStyle.Render("Path:    ") + rs.Path + "\n")
	if rs.Tool != "" {
		b.WriteString(dimStyle.Render("Tool:    ") + rs.DisplayTool() + "\n")
	}
	if rs.Group != "" {
		b.WriteString(dimStyle.Render("Group:   ") + rs.Group + "\n")
	}
	remoteViewers, remoteViewersKnown := rs.ViewerList()
	unknownReason := ""
	if !remoteViewersKnown {
		versionState, _ := h.remoteVersionState(item.RemoteName)
		unknownReason = remoteViewersUnknownReason(versionState, Version)
	}
	b.WriteString(dimStyle.Render("Viewers: ") + viewersText(remoteViewers, remoteViewersKnown, unknownReason, time.Now()) + "\n")
	// Account usage comes from the host stats already polled for the group
	// header, so this adds no SSH round trip.
	if line := h.remoteAccountsLine(item.RemoteName, rs.Account, time.Now()); line != "" {
		b.WriteString(dimStyle.Render(line) + "\n")
	}
	b.WriteString("\n")

	pvKey := remotePreviewCacheKey(item.RemoteName, rs.ID)
	h.previewCacheMu.RLock()
	previewContent, hasPreview := h.previewCache[pvKey]
	_, hasFetched := h.previewCacheTime[pvKey]
	h.previewCacheMu.RUnlock()

	b.WriteString(dimStyle.Render("Last response") + "\n")
	b.WriteString(strings.Repeat("-", max(1, min(width-4, 40))))
	b.WriteString("\n")
	previewContent = truncateRemotePreviewContent(previewContent)
	// The pane's tail fits the rows left above the blank line and the attach
	// hint: ensureExactHeight keeps the first rows of this block, so a longer
	// body lost exactly the pane's bottom (the status line and footer).
	rows := height - strings.Count(b.String(), "\n") - 2
	fitted := fitRemotePreviewTail(previewContent, rows, width)
	switch {
	case hasPreview && len(fitted) > 0:
		b.WriteString(strings.Join(fitted, "\n"))
		b.WriteString("\n\n")
	case hasPreview && strings.TrimSpace(previewContent) != "":
		// No row left for the body: the header and the hint keep their places.
	case hasFetched:
		b.WriteString(dimStyle.Render("No response available yet."))
		b.WriteString("\n\n")
	case rs.Status == "running" || rs.Status == "waiting":
		b.WriteString(dimStyle.Render("Fetching remote preview..."))
		b.WriteString("\n\n")
	default:
		b.WriteString(dimStyle.Render("No response available yet."))
		b.WriteString("\n\n")
	}

	b.WriteString(dimStyle.Render("Press Enter to attach via SSH"))

	return b.String()
}

func (h *Home) renderLaunchingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Check if this is a resume operation (vs new launch)
	_, isResuming := h.resumingSessions[inst.ID]

	// Braille spinner frames - creates smooth rotation effect
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Tool-specific messaging with emoji
	var toolName, toolDesc, emoji string
	if isResuming {
		emoji = "🔄"
	} else {
		emoji = "🚀"
	}

	switch inst.Tool {
	case "claude":
		toolName = "Claude Code"
		if isResuming {
			toolDesc = "Resuming Claude session..."
		} else {
			toolDesc = "Starting Claude session..."
		}
	case "gemini":
		toolName = "Gemini"
		if isResuming {
			toolDesc = "Resuming Gemini session..."
		} else {
			toolDesc = "Connecting to Gemini..."
		}
	case "aider":
		toolName = "Aider"
		if isResuming {
			toolDesc = "Resuming Aider session..."
		} else {
			toolDesc = "Starting Aider..."
		}
	case "codex":
		toolName = "Codex"
		if isResuming {
			toolDesc = "Resuming Codex session..."
		} else {
			toolDesc = "Starting Codex..."
		}
	case "opencode":
		toolName = "OpenCode"
		if isResuming {
			toolDesc = "Resuming OpenCode session..."
		} else {
			toolDesc = "Starting OpenCode..."
		}
	case "cursor":
		toolName = "Cursor Agent"
		if isResuming {
			toolDesc = "Resuming Cursor session..."
		} else {
			toolDesc = "Starting Cursor Agent..."
		}
	default:
		toolName = "Shell"
		if isResuming {
			toolDesc = "Resuming shell session..."
		} else {
			toolDesc = "Launching shell session..."
		}
	}

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner with tool color
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Title with emoji
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	var actionVerb string
	if isResuming {
		actionVerb = "Resuming"
	} else {
		actionVerb = "Launching"
	}
	b.WriteString(centerStyle.Render(titleStyle.Render(emoji + " " + actionVerb + " " + toolName)))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render(toolDesc)))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorAccent)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time (consistent with MCP and Fork animations)
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderMcpLoadingState renders the MCP loading animation in the preview pane
func (h *Home) renderMcpLoadingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Braille spinner frames - creates smooth rotation effect
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Spinner with cyan color (MCP-themed)
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// MCP loading title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔌 Reloading MCPs")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Restarting session with updated MCP configuration...")))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorCyan)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderForkingState renders the forking animation when session is being forked
func (h *Home) renderSetupRunningState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	titleStyle := lipgloss.NewStyle().
		Foreground(ColorGreen).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("Running Worktree Setup")))
	b.WriteString("\n\n")

	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Executing .agent-deck/worktree-setup.sh...")))
	b.WriteString("\n\n")

	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorGreen)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Running... %s", elapsed))))

	return b.String()
}

func (h *Home) renderForkingState(inst *session.Instance, width int, startTime time.Time) string {
	var b strings.Builder

	// Centered layout
	centerStyle := lipgloss.NewStyle().
		Width(width - 4).
		Align(lipgloss.Center)

	// Braille spinner frames
	spinnerFrames := []string{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"}
	spinner := spinnerFrames[h.animationFrame]

	// Spinner with purple color (fork-themed)
	spinnerStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	spinnerLine := spinnerStyle.Render(spinner + "  " + spinner + "  " + spinner)
	b.WriteString(centerStyle.Render(spinnerLine))
	b.WriteString("\n\n")

	// Forking title
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	b.WriteString(centerStyle.Render(titleStyle.Render("🔀 Forking Session")))
	b.WriteString("\n\n")

	// Description
	descStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	b.WriteString(centerStyle.Render(descStyle.Render("Creating a new Claude session from this conversation...")))
	b.WriteString("\n\n")

	// Progress dots animation
	dotsCount := (h.animationFrame % 4) + 1
	dots := strings.Repeat("●", dotsCount) + strings.Repeat("○", 4-dotsCount)
	dotsStyle := lipgloss.NewStyle().
		Foreground(ColorPurple)
	b.WriteString(centerStyle.Render(dotsStyle.Render(dots)))
	b.WriteString("\n\n")

	// Elapsed time (consistent with other animations)
	elapsed := time.Since(startTime).Round(time.Second)
	timeStyle := lipgloss.NewStyle().
		Foreground(ColorYellow).
		Italic(true)
	b.WriteString(centerStyle.Render(timeStyle.Render(fmt.Sprintf("Loading... %s", elapsed))))

	return b.String()
}

// renderSessionInfoCard renders a simple session info card as fallback view
// Used when both show_output and show_analytics are disabled
func (h *Home) renderSessionInfoCard(inst *session.Instance, width, height int) string {
	if inst == nil {
		dimStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)
		return dimStyle.Render("No session selected")
	}

	var b strings.Builder

	// Snapshot status/tool under read lock for thread safety
	cardStatus := inst.GetStatusThreadSafe()
	cardTool := inst.DisplayToolThreadSafe()

	// Header with tool icon
	icon := ToolIcon(cardTool)
	header := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent).
		Render(fmt.Sprintf("%s %s", icon, inst.Title))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString(strings.Repeat("─", max(0, min(width-4, 40))))
	b.WriteString("\n\n")

	labelStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	valueStyle := lipgloss.NewStyle().Foreground(ColorText)

	// Path
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Path:"), valueStyle.Render(inst.ProjectPath)))

	// Status with color
	var statusColor lipgloss.Color
	switch cardStatus {
	case session.StatusRunning:
		statusColor = ColorGreen
	case session.StatusWaiting:
		statusColor = ColorYellow
	case session.StatusError:
		statusColor = ColorRed
	default:
		statusColor = ColorTextDim
	}
	statusStyle := lipgloss.NewStyle().Foreground(statusColor)
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Status:"), statusStyle.Render(processExitLabel(string(cardStatus), inst.CachedSubstate(), inst.ExitCode()))))

	// Tool
	b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Tool:"), valueStyle.Render(cardTool)))

	// Use the same cached metadata as the row; no account/config resolution.
	// An empty label means the row suppressed the badge (single-login machine,
	// inherited slot) — drop the whole line rather than print an empty value.
	if account := h.getSessionRenderState(inst).accountDisplay.label; account != "" {
		account = cellTruncate(account, max(0, width-cellWidth("Account slot: ")), "…")
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Account slot:"), valueStyle.Render(account)))
	}

	// Session ID (if available) - Claude, Gemini, OpenCode, or generic (Hermes/custom tools)
	sessionID := inst.ClaudeSessionID
	if sessionID == "" {
		sessionID = inst.GeminiSessionID
	}
	if sessionID == "" {
		sessionID = inst.OpenCodeSessionID
	}
	if sessionID == "" {
		sessionID = inst.GetGenericSessionID()
	}
	if sessionID != "" {
		shortID := sessionID
		if len(shortID) > 12 {
			shortID = shortID[:12] + "..."
		}
		b.WriteString(fmt.Sprintf("%s %s\n", labelStyle.Render("Session:"), valueStyle.Render(shortID)))
	}

	// Created date
	b.WriteString(
		fmt.Sprintf("%s %s\n", labelStyle.Render("Created:"), valueStyle.Render(inst.CreatedAt.Format("Jan 2 15:04"))),
	)

	return b.String()
}

// renderPreviewPane renders the right panel with live preview
func (h *Home) renderPreviewPane(width, height int) string {
	var b strings.Builder
	if h.embeddedMode && h.sessionInput != nil {
		return h.renderEmbeddedTerminalContent(width, height)
	}

	if len(h.flatItems) == 0 || h.cursor >= len(h.flatItems) {
		// Show different message when there are no sessions vs just no selection
		if len(h.flatItems) == 0 {
			if config, filtered := h.filteredEmptyState(); filtered {
				return renderEmptyStateResponsive(config, width, height)
			}
			// Group-scoped empty preview
			if h.groupScope != "" {
				return renderEmptyStateResponsive(EmptyStateConfig{
					Icon:     "✦",
					Title:    h.groupScopeDisplayName(),
					Subtitle: "Group scope active",
					Hints:    []string{"Only sessions in this group are shown"},
				}, width, height)
			}
			hints := make([]string, 0, 2)
			if key := h.actionKey(hotkeyNewSession); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to create your first session", key))
			}
			if key := h.actionKey(hotkeyImport); key != "" {
				hints = append(hints, fmt.Sprintf("Press %s to import tmux sessions", key))
			}
			if len(hints) == 0 {
				hints = append(hints, "Create or import sessions to get started")
			}
			content := renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "✦",
				Title:    "Ready to Go",
				Subtitle: "Your workspace is set up",
				Hints:    hints,
			}, width, height)
			if statsBlock := h.renderSystemStatsBlock(width); statsBlock != "" {
				content += "\n" + statsBlock
			}
			return content
		}
		content := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "◇",
			Title:    "No Selection",
			Subtitle: "Select a session to preview",
			Hints:    nil,
		}, width, height)
		if statsBlock := h.renderSystemStatsBlock(width); statsBlock != "" {
			content += "\n" + statsBlock
		}
		return content
	}

	item := h.flatItems[h.cursor]

	// If group is selected, show group info
	if item.Type == session.ItemTypeGroup {
		return h.renderGroupPreview(item.Group, width, height)
	}

	// Remote items: show simple preview
	if item.Type == session.ItemTypeRemoteGroup || item.Type == session.ItemTypeRemoteSession {
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil &&
			h.embeddedRemoteSessionIs(item.RemoteName, item.RemoteSession.ID) {
			key := remotePreviewCacheKey(item.RemoteName, item.RemoteSession.ID)
			h.previewCacheMu.RLock()
			preview := h.previewCache[key]
			h.previewCacheMu.RUnlock()
			return renderEmbeddedTerminal(
				item.RemoteSession.Title,
				item.RemoteSession.Status,
				item.RemoteSession.DisplayTool()+" · "+item.RemoteName,
				preview,
				width,
				height,
				h.previewScrollOffset,
			)
		}
		if item.Type == session.ItemTypeRemoteSession && item.RemoteSession != nil && h.embeddedLayout &&
			item.RemoteSession.Status != string(session.StatusStopped) &&
			item.RemoteSession.Status != string(session.StatusError) {
			key := remotePreviewCacheKey(item.RemoteName, item.RemoteSession.ID)
			h.previewCacheMu.RLock()
			preview := h.previewCache[key]
			h.previewCacheMu.RUnlock()
			return h.renderDetachedTerminalContent(preview, width, height)
		}
		return h.renderRemotePreview(item, width, height)
	}

	// Window items: resolve parent session for preview
	if item.Type == session.ItemTypeWindow {
		parentInst := h.getInstanceByID(item.WindowSessionID)
		if parentInst == nil {
			return renderEmptyStateResponsive(EmptyStateConfig{
				Icon:     "◇",
				Title:    fmt.Sprintf("Window %d: %s", item.WindowIndex, item.WindowName),
				Subtitle: "Parent session not found",
			}, width, height)
		}
		item.Session = parentInst
	}

	// Creating session placeholder: show dedicated animation
	if item.CreatingID != "" {
		if creating, ok := h.creatingSessions[item.CreatingID]; ok {
			return h.renderCreatingPreview(creating, width, height)
		}
		return ""
	}

	// Defensive: dividers and any other non-session rows carry a nil Session.
	// The cursor should never come to rest on one (skipDivider on navigation,
	// and the restore/clamp below nudges off dividers), but View() must be total
	// over every flatItems state and must never panic. Mirror the "No Selection"
	// empty state used when the cursor is out of range.
	if item.Session == nil {
		content := renderEmptyStateResponsive(EmptyStateConfig{
			Icon:     "◇",
			Title:    "No Selection",
			Subtitle: "Select a session to preview",
			Hints:    nil,
		}, width, height)
		if statsBlock := h.renderSystemStatsBlock(width); statsBlock != "" {
			content += "\n" + statsBlock
		}
		return content
	}

	// Session preview
	selected := item.Session

	// Compute preview cache key (window-aware)
	pvKey := selected.ID
	if item.Type == session.ItemTypeWindow {
		pvKey = previewCacheKey(selected.ID, item.WindowIndex)
	}
	if h.embeddedSessionIs(selected.ID) {
		h.previewCacheMu.RLock()
		preview := h.previewCache[pvKey]
		h.previewCacheMu.RUnlock()
		return renderEmbeddedTerminal(
			selected.Title,
			string(selected.GetStatusThreadSafe()),
			selected.DisplayToolThreadSafe(),
			preview,
			width,
			height,
			h.previewScrollOffset,
		)
	}
	selectedStatus := selected.GetStatusThreadSafe()
	if h.embeddedLayout && selectedStatus != session.StatusStopped && selectedStatus != session.StatusError {
		h.previewCacheMu.RLock()
		preview := h.previewCache[pvKey]
		h.previewCacheMu.RUnlock()
		return h.renderDetachedTerminalContent(preview, width, height)
	}

	// Session info header box
	// Cache status once to avoid races with background status updates
	statusIcon := "○"
	statusColor := ColorTextDim
	switch selectedStatus {
	case session.StatusRunning:
		statusIcon = "●"
		statusColor = ColorGreen
	case session.StatusWaiting:
		statusIcon = "◐"
		statusColor = ColorYellow
	case session.StatusError:
		statusIcon = "✕"
		statusColor = ColorRed
	case session.StatusStopped:
		statusIcon = "■"
		statusColor = ColorTextDim
	}

	// Header with session name and status
	statusBadge := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon + " " + processExitLabel(string(selectedStatus), selected.CachedSubstate(), selected.ExitCode()))
	nameStyle := lipgloss.NewStyle().Bold(true).Foreground(ColorAccent)
	b.WriteString(nameStyle.Render(selected.Title))
	b.WriteString("  ")
	b.WriteString(statusBadge)
	b.WriteString("\n")

	// Issue #2473: a session running because of background work (a Workflow,
	// background agents, shells, a Monitor) at an empty prompt says what is
	// running, right under the status. In-memory read; no capture.
	if line := backgroundWorkLine(selected.BackgroundWork()); line != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(ColorGreen).Render(truncateVisible(line, width-4)))
		b.WriteString("\n")
	}

	// Auth hold banner. A session whose agent exited on a 401 shows a bare
	// "error" status that no amount of restarting will clear, and during a
	// fleet-wide credential failure that reads as unexplained mass death (the
	// 2026-07-26 incident). Say what happened and what to do, right under the
	// status, before anything else in the preview. Reads the in-memory mirror so
	// the render path never touches the filesystem.
	if selected.AuthHeldCached() {
		b.WriteString(authHoldBannerLines(width))
	}

	// Info lines: path and activity time
	infoStyle := lipgloss.NewStyle().Foreground(ColorText)
	pathStr := truncatePath(selected.ProjectPath, width-4)
	b.WriteString(infoStyle.Render("📁 " + pathStr))
	b.WriteString("\n")

	// Activity time - shows when session was last active. Composed with
	// sessionActivityTime — the SAME formula as the row badge (issue #1846:
	// the preview used to read DisplayLastActivityTime while the badge used
	// pickBadgeTime, so the two surfaces showed different ages for the same
	// session; both were stale once the underlying evidence was destroyed).
	var previewHookStatus *session.HookStatus
	if h.hookWatcher != nil {
		previewHookStatus = h.hookWatcher.GetHookStatus(selected.ID)
	}
	confirmedTs, confirmedObserved := selected.LastObservedActivity()
	activityTime := sessionActivityTime(selected.CreatedAt, selected.LastStartedAt, selected.LastActivityAt(), selected.LastAccessedAt, confirmedTs, confirmedObserved, previewHookStatus)
	activityStr := formatRelativeTime(activityTime)
	if selectedStatus == session.StatusRunning {
		activityStr = "active now"
	}
	b.WriteString(infoStyle.Render("⏱ " + activityStr))
	b.WriteString("\n")

	// Who else has this session open (shared attach), from the per-socket
	// viewer cache so the render path never spawns tmux. A stopped session
	// has no tmux to view, so the line is for live ones.
	if selectedStatus != session.StatusStopped {
		previewViewers, previewViewersKnown := selected.ViewersCached()
		b.WriteString(infoStyle.Render(viewersLine(previewViewers, previewViewersKnown, time.Now())))
		b.WriteString("\n")
	}

	toolBadge := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorPurple).
		Padding(0, 1).
		Render(selected.DisplayToolThreadSafe())
	groupBadge := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorCyan).
		Padding(0, 1).
		Render(selected.GroupPath)
	b.WriteString(toolBadge)
	b.WriteString(" ")
	b.WriteString(groupBadge)
	b.WriteString("\n")

	// Agent card. When the selected session belongs to an adopted agent, its
	// role, triggers, connector health and recent ledger entries render here
	// — high in the preview, where there is room to be accurate — instead of
	// being crushed into the session row, which carries only the ⚙ marker.
	// Sessions with no agent, and decks with nothing adopted, render nothing.
	if agentRow, owned := h.agentRowForSession(selected.ID); owned {
		b.WriteString(h.renderAgentCard(agentRow, width))
	}

	// Worktree info section (for sessions running in git worktrees)
	if selected.IsWorktree() {
		wtHeader := renderSectionDivider("Worktree", width-4)
		b.WriteString(wtHeader)
		b.WriteString("\n")

		wtLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		wtBranchStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)
		wtValueStyle := lipgloss.NewStyle().Foreground(ColorText)
		wtHintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
		wtKeyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		// Branch
		if selected.WorktreeBranch != "" {
			b.WriteString(wtLabelStyle.Render("Branch:  "))
			b.WriteString(wtBranchStyle.Render(selected.WorktreeBranch))
			b.WriteString("\n")
		}

		// Repo root (truncated)
		if selected.WorktreeRepoRoot != "" {
			repoPath := truncatePath(selected.WorktreeRepoRoot, width-4-9)
			b.WriteString(wtLabelStyle.Render("Repo:    "))
			b.WriteString(wtValueStyle.Render(repoPath))
			b.WriteString("\n")
		}

		// Worktree path (truncated)
		if selected.WorktreePath != "" {
			wtPath := truncatePath(selected.WorktreePath, width-4-9)
			b.WriteString(wtLabelStyle.Render("Path:    "))
			b.WriteString(wtValueStyle.Render(wtPath))
			b.WriteString("\n")
		}

		// Dirty status (lazy-cached, fetched via previewDebounce handler with 10s TTL)
		h.worktreeDirtyMu.Lock()
		isDirty, hasCached := h.worktreeDirtyCache[selected.ID]
		h.worktreeDirtyMu.Unlock()

		dirtyLabel := "checking..."
		dirtyStyle := wtValueStyle
		if hasCached {
			if isDirty {
				dirtyLabel = "dirty (uncommitted changes)"
				dirtyStyle = lipgloss.NewStyle().Foreground(ColorYellow)
			} else {
				dirtyLabel = "clean"
				dirtyStyle = lipgloss.NewStyle().Foreground(ColorGreen)
			}
		}
		b.WriteString(wtLabelStyle.Render("Status:  "))
		b.WriteString(dirtyStyle.Render(dirtyLabel))
		b.WriteString("\n")

		// Setup hint
		if setupKey := h.actionKey(hotkeyWorktreeSetup); setupKey != "" {
			b.WriteString(wtHintStyle.Render("Setup:   "))
			b.WriteString(wtKeyStyle.Render(setupKey))
			b.WriteString(wtHintStyle.Render(" re-run setup script"))
			b.WriteString("\n")
		}

		// Finish hint
		if finishKey := h.actionKey(hotkeyWorktreeFinish); finishKey != "" {
			b.WriteString(wtHintStyle.Render("Finish:  "))
			b.WriteString(wtKeyStyle.Render(finishKey))
			b.WriteString(wtHintStyle.Render(" merge + cleanup"))
			b.WriteString("\n")
		}
	}

	// Multi-repo info section
	if selected.IsMultiRepo() {
		mrHeader := renderSectionDivider("Multi-Repo", width-4)
		b.WriteString(mrHeader)
		b.WriteString("\n")

		mrLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		mrValueStyle := lipgloss.NewStyle().Foreground(ColorText)

		for i, p := range selected.AllProjectPaths() {
			label := fmt.Sprintf("  %d. ", i+1)
			b.WriteString(mrLabelStyle.Render(label))
			b.WriteString(mrValueStyle.Render(truncatePath(p, width-4-len(label))))
			b.WriteString("\n")
		}

		editPathsKey := h.actionKey(hotkeyEditPaths)
		if editPathsKey != "" {
			hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
			b.WriteString(hintStyle.Render(fmt.Sprintf("  %s: edit paths", editPathsKey)))
			b.WriteString("\n")
		}
	}

	// Claude-specific info (session ID and MCPs)
	if session.IsClaudeCompatible(selected.Tool) {
		// Section divider for Claude info
		claudeHeader := renderSectionDivider("Claude", width-4)
		b.WriteString(claudeHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		// Status line
		if selected.ClaudeSessionID != "" {
			statusText, statusStyle := connectionStatusLine(selected.IsArchived(), selectedStatus)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render(statusText))
			b.WriteString("\n")

			// Full session ID on its own line (kept even when archived/stopped so
			// the conversation can be resumed)
			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.ClaudeSessionID))
			b.WriteString("\n")
		} else {
			statusStyle := lipgloss.NewStyle().Foreground(ColorText)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("○ Not connected"))
			b.WriteString("\n")
		}
		renderLaunchModelInfoLines(&b, selected)

		// MCP servers - compact format with source indicators and sync status
		mcpInfo := selected.GetMCPInfo()
		hasLoadedMCPs := len(selected.LoadedMCPNames) > 0
		hasMCPs := mcpInfo != nil && mcpInfo.HasAny()

		if hasMCPs || hasLoadedMCPs {
			b.WriteString(labelStyle.Render("MCPs:    "))

			// Build set of loaded MCPs for comparison
			loadedSet := make(map[string]bool)
			for _, name := range selected.LoadedMCPNames {
				loadedSet[name] = true
			}

			// Build set of current MCPs (from config)
			currentSet := make(map[string]bool)
			if mcpInfo != nil {
				for _, name := range mcpInfo.Global {
					currentSet[name] = true
				}
				for _, name := range mcpInfo.Project {
					currentSet[name] = true
				}
				for _, mcp := range mcpInfo.LocalMCPs {
					currentSet[mcp.Name] = true
				}
			}

			// Styles for different MCP states
			pendingStyle := lipgloss.NewStyle().Foreground(ColorYellow)
			staleStyle := lipgloss.NewStyle().Foreground(ColorText)

			var mcpParts []string

			// Helper to add MCP with appropriate styling
			addMCP := func(name, source string) {
				label := name + " (" + source + ")"
				if !hasLoadedMCPs {
					// Old session without LoadedMCPNames - show all as normal (no sync info)
					mcpParts = append(mcpParts, valueStyle.Render(label))
				} else if loadedSet[name] {
					// In both loaded and current - active (normal style)
					mcpParts = append(mcpParts, valueStyle.Render(label))
				} else {
					// In current but not loaded - pending (needs restart)
					mcpParts = append(mcpParts, pendingStyle.Render(label+" ⟳"))
				}
			}

			// Add MCPs from current config with source indicators
			if mcpInfo != nil {
				for _, name := range mcpInfo.Global {
					addMCP(name, "g")
				}
				for _, name := range mcpInfo.Project {
					addMCP(name, "p")
				}
				for _, mcp := range mcpInfo.LocalMCPs {
					// Show source path if different from project path
					sourceIndicator := "l"
					if mcp.SourcePath != selected.ProjectPath {
						// Show abbreviated path (just directory name)
						sourceIndicator = "l:" + filepath.Base(mcp.SourcePath)
					}
					addMCP(mcp.Name, sourceIndicator)
				}
			}

			// Add stale MCPs (loaded but no longer in config)
			if hasLoadedMCPs {
				for _, name := range selected.LoadedMCPNames {
					if !currentSet[name] {
						// Still running but removed from config
						mcpParts = append(mcpParts, staleStyle.Render(name+" ✕"))
					}
				}
			}

			// Calculate available width for MCPs (width - 4 for panel padding - 9 for "MCPs:    " label)
			mcpMaxWidth := width - 4 - 9
			if mcpMaxWidth < 20 {
				mcpMaxWidth = 20 // Minimum sensible width
			}

			// Build MCPs progressively to fit within available width
			var mcpResult strings.Builder
			mcpCount := 0
			currentWidth := 0

			for i, part := range mcpParts {
				// Strip ANSI codes to measure actual display width
				plainPart := tmux.StripANSI(part)
				// #937 v2: cellWidth (not ansi.StringWidth) so keycap
				// clusters in MCP names — see cellwidth.go — are sized
				// at the cell count terminals actually render.
				partWidth := cellWidth(plainPart)

				// Calculate width including separator if not first
				addedWidth := partWidth
				if mcpCount > 0 {
					addedWidth += 2 // ", " separator
				}

				remaining := len(mcpParts) - i
				isLast := remaining == 1

				// For non-last MCPs: reserve space for "+N more" indicator
				// For last MCP: just check if it fits without indicator
				var wouldExceed bool
				if isLast {
					// Last MCP - just check if it fits
					wouldExceed = currentWidth+addedWidth > mcpMaxWidth
				} else {
					// Not last - check with indicator space reserved
					moreIndicator := fmt.Sprintf(" (+%d more)", remaining)
					moreWidth := cellWidth(moreIndicator)
					wouldExceed = currentWidth+addedWidth+moreWidth > mcpMaxWidth
				}

				if wouldExceed {
					// Would exceed - show indicator for remaining
					moreStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					if mcpCount > 0 {
						mcpResult.WriteString(moreStyle.Render(fmt.Sprintf(" (+%d more)", remaining)))
					} else {
						// No MCPs fit - just show count
						mcpResult.WriteString(moreStyle.Render(fmt.Sprintf("(%d MCPs)", len(mcpParts))))
					}
					break
				}

				// Add separator if not first
				if mcpCount > 0 {
					mcpResult.WriteString(", ")
				}
				mcpResult.WriteString(part)
				currentWidth += addedWidth
				mcpCount++
			}

			b.WriteString(mcpResult.String())
			b.WriteString("\n")
		}

		// Fork hint when session can be forked
		if selected.CanFork() {
			quickForkKey := h.actionKey(hotkeyQuickFork)
			forkWithOptionsKey := h.actionKey(hotkeyForkWithOptions)
			if quickForkKey != "" || forkWithOptionsKey != "" {
				hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
				keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
				b.WriteString(hintStyle.Render("Fork:    "))
				if quickForkKey != "" {
					b.WriteString(keyStyle.Render(quickForkKey))
					b.WriteString(hintStyle.Render(" quick fork"))
				}
				if quickForkKey != "" && forkWithOptionsKey != "" {
					b.WriteString(hintStyle.Render(", "))
				}
				if forkWithOptionsKey != "" {
					b.WriteString(keyStyle.Render(forkWithOptionsKey))
					b.WriteString(hintStyle.Render(" fork with options"))
				}
				b.WriteString("\n")
			}
		}
	}

	// Gemini-specific info (session ID)
	if selected.Tool == "gemini" {
		geminiHeader := renderSectionDivider("Gemini", width-4)
		b.WriteString(geminiHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		if selected.GeminiSessionID != "" {
			statusText, statusStyle := connectionStatusLine(selected.IsArchived(), selectedStatus)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render(statusText))
			b.WriteString("\n")

			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.GeminiSessionID))
			b.WriteString("\n")
			renderLaunchModelInfoLines(&b, selected)

			// MCPs for Gemini (global only)
			mcpInfo := selected.GetMCPInfo()
			renderSimpleMCPLine(&b, mcpInfo, width)
		} else {
			statusStyle := lipgloss.NewStyle().Foreground(ColorText)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render("○ Not connected"))
			b.WriteString("\n")
			renderLaunchModelInfoLines(&b, selected)
		}
	}

	// Cursor Agent CLI — MCP configuration for `cursor agent`
	if selected.Tool == "cursor" {
		cursorHeader := renderSectionDivider("Cursor", width-4)
		b.WriteString(cursorHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)
		b.WriteString(labelStyle.Render("Tool:    "))
		b.WriteString(valueStyle.Render("Cursor Agent CLI"))
		b.WriteString("\n")
		renderLaunchModelInfoLines(&b, selected)

		mcpInfo := selected.GetMCPInfo()
		renderSimpleMCPLine(&b, mcpInfo, width)
	}

	// OpenCode-specific info (session ID)
	if selected.Tool == "opencode" {
		opencodeHeader := renderSectionDivider("OpenCode", width-4)
		b.WriteString(opencodeHeader)
		b.WriteString("\n")

		labelStyle := lipgloss.NewStyle().Foreground(ColorText)
		valueStyle := lipgloss.NewStyle().Foreground(ColorText)

		// Debug: log what value we're seeing
		uiLog.Debug(
			"opencode_rendering_preview",
			slog.String("title", selected.Title),
			slog.String("session_id", selected.OpenCodeSessionID),
		)

		if selected.OpenCodeSessionID != "" {
			statusText, statusStyle := connectionStatusLine(selected.IsArchived(), selectedStatus)
			b.WriteString(labelStyle.Render("Status:  "))
			b.WriteString(statusStyle.Render(statusText))
			b.WriteString("\n")

			b.WriteString(labelStyle.Render("Session: "))
			b.WriteString(valueStyle.Render(selected.OpenCodeSessionID))
			b.WriteString("\n")
			renderLaunchModelInfoLines(&b, selected)

			// Show when session was detected
			if !selected.OpenCodeDetectedAt.IsZero() {
				detectedAgo := formatRelativeTime(selected.OpenCodeDetectedAt)
				dimStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
				b.WriteString(labelStyle.Render("Detected:"))
				b.WriteString(dimStyle.Render(" " + detectedAgo))
				b.WriteString("\n")
			}

			// Fork hint for OpenCode
			if selected.CanFork() {
				h.renderForkHintLine(&b)
			}
		} else {
			// Check if detection has completed (OpenCodeDetectedAt is set even when no session found)
			if selected.OpenCodeDetectedAt.IsZero() {
				// Detection not yet completed - show detecting state
				statusStyle := lipgloss.NewStyle().Foreground(ColorYellow)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("◐ Detecting session..."))
				b.WriteString("\n")
				renderLaunchModelInfoLines(&b, selected)
			} else {
				// Detection completed but no session found
				statusStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("○ No session found"))
				b.WriteString("\n")
				renderLaunchModelInfoLines(&b, selected)
			}
		}
	}

	// Codex-specific info (session ID, detection)
	if selected.Tool == "codex" {
		codexHeader := renderSectionDivider("Codex", width-4)
		b.WriteString(codexHeader)
		b.WriteString("\n")

		renderToolStatusLine(&b, selected.CodexSessionID, selected.CodexDetectedAt, true, selected.IsArchived(), selected.Status)
		renderLaunchModelInfoLines(&b, selected)
		if selected.CodexSessionID != "" {
			renderDetectedAtLine(&b, selected.CodexDetectedAt)
		}
	}

	// Custom tool info (tools defined in config.toml that aren't built-in)
	if !session.IsClaudeCompatible(selected.Tool) && selected.Tool != "gemini" && selected.Tool != "opencode" &&
		selected.Tool != "codex" {
		if toolDef := session.GetToolDef(selected.Tool); toolDef != nil {
			toolName := selected.Tool
			if toolDef.Icon != "" {
				toolName = toolDef.Icon + " " + toolName
			}
			customHeader := renderSectionDivider(toolName, width-4)
			b.WriteString(customHeader)
			b.WriteString("\n")

			labelStyle := lipgloss.NewStyle().Foreground(ColorText)

			genericID := selected.GetGenericSessionID()
			if genericID != "" {
				statusText, statusStyle := connectionStatusLine(selected.IsArchived(), selectedStatus)
				valueStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render(statusText))
				b.WriteString("\n")

				b.WriteString(labelStyle.Render("Session: "))
				b.WriteString(valueStyle.Render(genericID))
				b.WriteString("\n")
			} else {
				statusStyle := lipgloss.NewStyle().Foreground(ColorText)
				b.WriteString(labelStyle.Render("Status:  "))
				b.WriteString(statusStyle.Render("○ Not connected"))
				b.WriteString("\n")
			}

			// Resume hint when tool supports restart with session resume
			if selected.CanRestartGeneric() {
				if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
					hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
					b.WriteString(hintStyle.Render("Resume:  "))
					b.WriteString(keyStyle.Render(restartKey))
					b.WriteString(hintStyle.Render(" restart with session resume"))
					b.WriteString("\n")
				}
			}
			if selected.CanRestartFresh() {
				if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
					hintStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
					keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
					b.WriteString(hintStyle.Render("Fresh:   "))
					b.WriteString(keyStyle.Render(restartFreshKey))
					b.WriteString(hintStyle.Render(" restart with a new session ID"))
					b.WriteString("\n")
				}
			}
		}
	}

	b.WriteString("\n")

	// Check preview settings for what to show
	config, _ := session.LoadUserConfig()
	showAnalytics := config != nil && config.GetShowAnalytics() &&
		(session.IsClaudeCompatible(selected.Tool) || selected.Tool == "gemini")
	showOutput := config == nil || config.GetShowOutput() // Default to true if config fails
	showNotes := config != nil && config.GetShowNotes()   // Default to false if config fails
	notesOutputSplit := 0.33
	if config != nil {
		notesOutputSplit = config.Preview.GetNotesOutputSplit()
	}

	// Apply preview mode override (v key cycles through modes)
	switch h.previewMode {
	case PreviewModeOutput:
		showAnalytics = false
		showOutput = true
	case PreviewModeAnalytics:
		// showAnalytics keeps its default value (only available for Claude/Gemini)
		showOutput = false
		// PreviewModeBoth: use config settings (default)
	}

	// A stopped status can also follow a clean process exit. Do not infer intent.
	if selectedStatus == session.StatusStopped {
		stoppedHeader := renderSectionDivider("Process Exited", width-4)
		b.WriteString(stoppedHeader)
		b.WriteString("\n\n")

		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle := lipgloss.NewStyle().Foreground(ColorText)
		keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		b.WriteString(warnStyle.Render("■ Process exited"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("The process ended or was stopped."))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("The session record is preserved for resuming."))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Actions:"))
		b.WriteString("\n")
		if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(restartKey))
			b.WriteString(dimStyle.Render(" Resume  - restart with session resume"))
			b.WriteString("\n")
		}
		if selected.CanRestartFresh() {
			if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(restartFreshKey))
				b.WriteString(dimStyle.Render(" Fresh   - restart with a new session ID"))
				b.WriteString("\n")
			}
		}
		if deleteKey := h.actionKey(hotkeyDelete); deleteKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(deleteKey))
			b.WriteString(dimStyle.Render(" Delete  - remove from list"))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("Enter"))
		b.WriteString(dimStyle.Render(" - attach (will auto-start)"))
		b.WriteString("\n")
		if selected.IsMultiRepo() {
			if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(editPathsKey))
				b.WriteString(dimStyle.Render(" Paths   - edit multi-repo paths"))
				b.WriteString("\n")
			}
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)

		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}

		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}

		return content
	}

	// Special handling for error state - crash/unexpected failure with diagnostic guidance
	if selectedStatus == session.StatusError {
		errorHeader := renderSectionDivider("Session Error", width-4)
		b.WriteString(errorHeader)
		b.WriteString("\n\n")

		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		dimStyle := lipgloss.NewStyle().Foreground(ColorText)
		keyStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)

		b.WriteString(warnStyle.Render("✕ No tmux session running"))
		if restartKey := h.actionKey(hotkeyRestart); restartKey != "" {
			b.WriteString("   ")
			b.WriteString(keyStyle.Render(restartKey))
			b.WriteString(dimStyle.Render(" Restart"))
		}
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("This can happen if:"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - Session was added but not yet started"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - tmux server was restarted"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  - Terminal was closed or system rebooted"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Actions:"))
		b.WriteString("\n")
		if selected.CanRestartFresh() {
			if restartFreshKey := h.actionKey(hotkeyRestartFresh); restartFreshKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(restartFreshKey))
				b.WriteString(dimStyle.Render(" Fresh   - start without resuming the prior session"))
				b.WriteString("\n")
			}
		}
		if deleteKey := h.actionKey(hotkeyDelete); deleteKey != "" {
			b.WriteString("  ")
			b.WriteString(keyStyle.Render(deleteKey))
			b.WriteString(dimStyle.Render(" Delete  - remove from list"))
			b.WriteString("\n")
		}
		b.WriteString("  ")
		b.WriteString(keyStyle.Render("Enter"))
		b.WriteString(dimStyle.Render(" - attach (will auto-start)"))
		b.WriteString("\n")
		if selected.IsMultiRepo() {
			if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
				b.WriteString("  ")
				b.WriteString(keyStyle.Render(editPathsKey))
				b.WriteString(dimStyle.Render(" Paths   - edit multi-repo paths"))
				b.WriteString("\n")
			}
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)

		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}

		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}

		return content
	}

	// Check if session is launching/resuming (for animation priority)
	_, isSessionLaunching := h.launchingSessions[selected.ID]
	_, isSessionResuming := h.resumingSessions[selected.ID]
	_, isSessionForking := h.forkingSessions[selected.ID]
	isStartingUp := isSessionLaunching || isSessionResuming || isSessionForking

	// Analytics panel (for Claude/Gemini sessions with analytics enabled)
	// Skip showing "Loading analytics..." during startup - let the launch animation take focus
	if showAnalytics && !isStartingUp {
		analyticsHeader := renderSectionDivider("Analytics", width-4)
		b.WriteString(analyticsHeader)
		b.WriteString("\n")

		// Check if we have analytics for this session
		if h.analyticsSessionID == selected.ID && (h.currentAnalytics != nil || h.currentGeminiAnalytics != nil) {
			// Pass display settings from config
			if config != nil {
				h.analyticsPanel.SetDisplaySettings(config.Preview.GetAnalyticsSettings())
			}
			h.analyticsPanel.SetSize(width-4, height/2)
			b.WriteString(h.analyticsPanel.View())
			b.WriteString("\n")
		} else {
			// Analytics not yet loaded
			loadingStyle := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true)
			b.WriteString(loadingStyle.Render("Loading analytics..."))
			b.WriteString("\n\n")
		}
	}

	remainingLines := height - (strings.Count(b.String(), "\n") + 1)
	if showNotes {
		notesLines := notesSectionLineBudget(remainingLines, showOutput || isStartingUp, notesOutputSplit)
		if notesLines > 0 {
			b.WriteString(h.renderNotesSection(selected, width, notesLines))
			b.WriteString("\n")
		}
	}

	// If output is disabled AND not starting up, return early
	// (We want to show the launch animation even if output is normally disabled)
	if !showOutput && !isStartingUp {
		// If analytics was also not shown, display session info card as fallback
		hasNotesContent := strings.TrimSpace(selected.Notes) != "" ||
			(h.notesEditing && h.notesEditingSessionID == selected.ID)
		if !showAnalytics && !hasNotesContent {
			infoCard := h.renderSessionInfoCard(selected, width, height)
			b.WriteString("\n")
			b.WriteString(infoCard)
		}

		// Pad output to exact height to prevent layout shifts
		content := b.String()
		lines := strings.Split(content, "\n")
		lineCount := len(lines)
		if lineCount < height {
			for i := lineCount; i < height; i++ {
				content += "\n"
			}
		}
		if len(content) > 0 && content[len(content)-1] == '\n' {
			content = content[:len(content)-1]
		}
		return content
	}

	// Terminal output header
	termHeader := renderSectionDivider("Output", width-4)
	b.WriteString(termHeader)
	b.WriteString("\n")

	// Check if this session is launching (newly created), resuming (restarted), or forking
	launchTime, isLaunching := h.launchingSessions[selected.ID]
	resumeTime, isResuming := h.resumingSessions[selected.ID]
	mcpLoadTime, isMcpLoading := h.mcpLoadingSessions[selected.ID]
	forkTime, isForking := h.forkingSessions[selected.ID]

	// Determine if we should show animation (launch, resume, MCP loading, or forking)
	// For Claude: show for minimum 6 seconds, then check for ready indicators
	// For others: show for first 3 seconds after creation
	showLaunchingAnimation := false
	showMcpLoadingAnimation := false
	showForkingAnimation := isForking // Show forking animation immediately
	var animationStartTime time.Time
	if isLaunching {
		animationStartTime = launchTime
	} else if isResuming {
		animationStartTime = resumeTime
	} else if isMcpLoading {
		animationStartTime = mcpLoadTime
	}

	// Apply STATUS-BASED animation logic (matches hasActiveAnimation exactly)
	// Animation shows until session is ready, detected via status or content
	if isLaunching || isResuming || isMcpLoading {
		timeSinceStart := time.Since(animationStartTime)

		// Brief minimum to prevent flicker
		if timeSinceStart < launchAnimationMinDuration(selected.Tool) {
			if isMcpLoading {
				showMcpLoadingAnimation = true
			} else {
				showLaunchingAnimation = true
			}
		} else if timeSinceStart < 15*time.Second {
			// STATUS-BASED CHECK: Session ready when Running/Waiting/Idle
			sessionReady := selectedStatus == session.StatusRunning ||
				selectedStatus == session.StatusWaiting ||
				selectedStatus == session.StatusIdle

			if !sessionReady {
				// Also check content for faster detection
				h.previewCacheMu.RLock()
				previewContent := h.previewCache[pvKey]
				h.previewCacheMu.RUnlock()

				// Strip ANSI for reliable pattern matching
				plainPreview := ansi.Strip(previewContent)

				if session.IsClaudeCompatible(selected.Tool) || selected.Tool == "gemini" {
					// Claude/Gemini ready indicators
					agentReady := strings.Contains(plainPreview, "ctrl+c to interrupt") ||
						strings.Contains(plainPreview, "No, and tell Claude what to do differently") ||
						strings.Contains(plainPreview, "\n> ") ||
						strings.Contains(plainPreview, "> \n") ||
						strings.Contains(plainPreview, "esc to interrupt") ||
						strings.Contains(plainPreview, "⠋") || strings.Contains(plainPreview, "⠙") ||
						strings.Contains(plainPreview, "Thinking") ||
						strings.Contains(plainPreview, "╭─")

					if selected.Tool == "gemini" {
						agentReady = agentReady ||
							strings.Contains(plainPreview, "▸") ||
							strings.Contains(plainPreview, "gemini>")
					}

					if !agentReady {
						if isMcpLoading {
							showMcpLoadingAnimation = true
						} else {
							showLaunchingAnimation = true
						}
					}
				} else {
					// Non-Claude/Gemini: ready if substantial content
					if len(strings.TrimSpace(plainPreview)) <= 50 {
						if isMcpLoading {
							showMcpLoadingAnimation = true
						} else {
							showLaunchingAnimation = true
						}
					}
				}
			}
		}
		// After 15 seconds, animation stops regardless
	}

	// Terminal preview - use cached content (async fetching keeps View() pure)
	h.previewCacheMu.RLock()
	preview, hasCached := h.previewCache[pvKey]
	h.previewCacheMu.RUnlock()

	// Show worktree setup animation when setup script is running
	setupTime, isSetupRunning := h.setupRunningSessions[selected.ID]
	if isSetupRunning {
		b.WriteString("\n")
		b.WriteString(h.renderSetupRunningState(selected, width, setupTime))
	} else if showForkingAnimation {
		b.WriteString("\n")
		b.WriteString(h.renderForkingState(selected, width, forkTime))
	} else if showMcpLoadingAnimation {
		// Show MCP loading animation when reloading MCPs
		b.WriteString("\n")
		b.WriteString(h.renderMcpLoadingState(selected, width, mcpLoadTime))
	} else if showLaunchingAnimation {
		// Show launching animation for new sessions
		b.WriteString("\n")
		b.WriteString(h.renderLaunchingState(selected, width, animationStartTime))
	} else if !hasCached {
		// Show loading indicator while waiting for async fetch
		loadingStyle := lipgloss.NewStyle().
			Foreground(ColorText).
			Italic(true)
		b.WriteString(loadingStyle.Render("Loading preview..."))
	} else if preview == "" {
		emptyTerm := lipgloss.NewStyle().
			Foreground(ColorText).
			Italic(true).
			Render("(terminal is empty)")
		b.WriteString(emptyTerm)
	} else {
		// Calculate maxLines dynamically based on how many header lines we've already written
		// This accounts for Claude sessions having more header lines than other sessions
		currentContent := b.String()
		headerLines := strings.Count(currentContent, "\n") + 1 // +1 for the current line
		lines := strings.Split(preview, "\n")

		// Strip trailing empty lines BEFORE truncation
		// This ensures we show actual content, not empty trailing lines when space is limited
		// (Terminal output often ends with empty lines at cursor position)
		for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
			lines = lines[:len(lines)-1]
		}

		// If all lines were empty, show empty indicator
		if len(lines) == 0 {
			emptyTerm := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true).
				Render("(terminal is empty)")
			b.WriteString(emptyTerm)
			return b.String()
		}

		maxLines := height - headerLines - 1 // -1 for potential truncation indicator
		if maxLines < 1 {
			maxLines = 1
		}

		// Track if we're truncating from the top (for indicator)
		truncatedFromTop := len(lines) > maxLines
		truncatedCount := 0
		scrolledBelow := 0
		if truncatedFromTop {
			// Reserve one line for the "⋮ N more above" indicator
			maxLines--
			if maxLines < 1 {
				maxLines = 1
			}
			// #574: slide the visible window up by previewScrollOffset lines
			// so the user can see older output instead of the tail. Clamp
			// the offset to the valid range so arbitrary values don't go
			// out of bounds or below zero.
			maxOffset := len(lines) - maxLines
			if maxOffset < 0 {
				maxOffset = 0
			}
			if h.previewScrollOffset > maxOffset {
				h.previewScrollOffset = maxOffset
			}
			if h.previewScrollOffset < 0 {
				h.previewScrollOffset = 0
			}
			endIdx := len(lines) - h.previewScrollOffset
			startIdx := endIdx - maxLines
			if startIdx < 0 {
				startIdx = 0
			}
			truncatedCount = startIdx
			scrolledBelow = len(lines) - endIdx
			lines = lines[startIdx:endIdx]
		} else {
			// Content fits without truncation — offset has no effect, keep state consistent.
			h.previewScrollOffset = 0
		}
		_ = scrolledBelow // reserved for a future "⋮ N below" indicator; offset clamp already prevents stale state

		maxWidth := width - 4
		if maxWidth < 10 {
			maxWidth = 10
		}

		// Show truncation indicator if content was cut from top
		if truncatedFromTop {
			truncIndicator := lipgloss.NewStyle().
				Foreground(ColorText).
				Italic(true).
				Render(fmt.Sprintf("⋮ %d more lines above", truncatedCount))
			b.WriteString(truncIndicator)
			b.WriteString("\n")
		}

		// Track consecutive empty lines to preserve some spacing
		consecutiveEmpty := 0
		const maxConsecutiveEmpty = 2 // Allow up to 2 consecutive empty lines

		isLightTheme := GetCurrentTheme() == ThemeLight
		for _, line := range lines {
			// Strip dangerous control characters (\r, \b, etc.) but preserve
			// ANSI escape sequences (ESC = 0x1b) so colors and formatting
			// from the captured terminal output pass through to display.
			safeLine := stripControlCharsPreserveANSI(line)

			// Strip CSI K (Erase in Line) and CSI J (Erase in Display).
			// Without this, captured content (e.g. Neovim mini.statusline)
			// instructs the outer terminal to paint the active SGR
			// background beyond the pane's truncation point. See #579.
			safeLine = stripDisplayErasingEscapes(safeLine)

			// In light theme, remap captured dark ANSI background colors to
			// the current preview surface instead of stripping them. Dark
			// bands (e.g. Codex, #322) stop bleeding through, and light
			// backgrounds a tool already draws pass through unchanged (#2449).
			if isLightTheme {
				safeLine = remapANSIBackground(safeLine, previewSurfaceANSI())
			}

			// Check if visually empty (strip ANSI for this check)
			stripped := ansi.Strip(safeLine)
			trimmed := strings.TrimSpace(stripped)
			if trimmed == "" {
				consecutiveEmpty++
				if consecutiveEmpty <= maxConsecutiveEmpty {
					b.WriteString("\n") // Preserve empty line
				}
				continue
			}
			consecutiveEmpty = 0 // Reset counter on non-empty line

			// Truncate based on display width using ANSI-aware measurement.
			// #937 v2: cellWidth/cellTruncate so pane-content lines from
			// the tmux capture-pane buffer — which is where @jennings's
			// keycap glyphs live — are sized at the cell count terminals
			// actually render.
			displayWidth := cellWidth(safeLine)
			if displayWidth > maxWidth {
				safeLine = cellTruncate(safeLine, maxWidth-3, "...")
			}

			b.WriteString(safeLine)
			b.WriteString("\n")
		}
	}

	// CRITICAL: Enforce width constraint on ALL lines to prevent overflow into left panel
	// When lipgloss.JoinHorizontal combines panels, any line exceeding rightWidth
	// will wrap and corrupt the layout
	maxWidth := width - 2 // Small margin for safety
	if maxWidth < 20 {
		maxWidth = 20
	}

	result := b.String()
	lines := strings.Split(result, "\n")
	var truncatedLines []string
	for _, line := range lines {
		// #937 v2: cellWidth/cellTruncate so the right-panel width
		// enforcement before lipgloss.JoinHorizontal handles keycap
		// clusters; ansi.* alone under-counted them and let oversized
		// lines bleed into the left panel.
		displayWidth := cellWidth(line)
		if displayWidth > maxWidth {
			line = cellTruncate(line, maxWidth-3, "...")
		}
		// Issue #699: captured Claude output (e.g., highlighted input line) can
		// contain an unclosed SGR whose reset was off-screen or clipped by
		// truncation. Without a hard reset at each newline boundary, the
		// highlight persists across the row — and when lipgloss.JoinHorizontal
		// lays down the next row (left_pane + separator + right_pane), the
		// left pane inherits the right pane's dangling SGR state. Close every
		// line that carries ANSI so state never leaks past the pane boundary.
		if strings.ContainsRune(line, 0x1b) {
			line += "\x1b[0m"
		}
		truncatedLines = append(truncatedLines, line)
	}

	return strings.Join(truncatedLines, "\n")
}

func notesSectionLineBudget(remaining int, reserveOutput bool, split float64) int {
	if remaining <= 0 {
		return 0
	}

	if !reserveOutput {
		return remaining
	}

	if split <= 0 {
		split = 0.33
	}

	notesLines := int(float64(remaining) * split)
	if notesLines < 3 {
		notesLines = 3
	}

	maxNotes := remaining - 3
	if maxNotes < 1 {
		maxNotes = 1
	}
	if notesLines > maxNotes {
		notesLines = maxNotes
	}

	return notesLines
}

func (h *Home) renderNotesSection(inst *session.Instance, width, maxLines int) string {
	if inst == nil || maxLines <= 0 {
		return ""
	}
	if maxLines < 2 {
		maxLines = 2
	}

	contentWidth := width - 4
	if contentWidth < 10 {
		contentWidth = 10
	}

	lines := make([]string, 0, maxLines)
	lines = append(lines, renderSectionDivider("Notes", width-4))
	bodyLines := maxLines - 1
	if bodyLines < 1 {
		bodyLines = 1
	}

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Italic(true)
	notesStyle := lipgloss.NewStyle().Foreground(ColorText)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorTextDim).Italic(true)

	if h.notesEditing && h.notesEditingSessionID == inst.ID {
		editorLines := bodyLines - 1
		if editorLines < 1 {
			editorLines = 1
		}

		h.notesEditor.SetWidth(contentWidth)
		h.notesEditor.SetHeight(editorLines)
		h.notesEditor.Focus()

		editorView := h.notesEditor.View()
		viewLines := strings.Split(editorView, "\n")
		if len(viewLines) > editorLines {
			viewLines = viewLines[:editorLines]
		}
		for len(viewLines) < editorLines {
			viewLines = append(viewLines, "")
		}

		for _, line := range viewLines {
			// #937 v2: cellTruncate for notes-editor lines so a keycap
			// glyph the user typed into the notes editor doesn't overflow
			// the panel — same drift class as the renderNotesSection path
			// just below.
			lines = append(lines, cellTruncate(line, contentWidth, "..."))
		}

		lines = append(lines, hintStyle.Render("Ctrl+S save • Esc cancel"))
	} else {
		notesBodyLines := bodyLines - 1
		if notesBodyLines < 1 {
			notesBodyLines = 1
		}

		rawNotes := strings.TrimRight(inst.Notes, "\n")
		rawLines := strings.Split(rawNotes, "\n")
		if len(rawLines) == 1 && strings.TrimSpace(rawLines[0]) == "" {
			rawLines = nil
		}

		if len(rawLines) == 0 {
			lines = append(lines, emptyStyle.Render("No notes"))
		} else {
			overflow := len(rawLines) > notesBodyLines
			displayLines := rawLines
			if overflow {
				displayLines = rawLines[:notesBodyLines]
			}
			for _, line := range displayLines {
				safe := stripControlCharsPreserveANSI(line)
				// #937 v2: cellTruncate (not ansi.Truncate) is the truncation
				// gate for pane content. ansi/uniseg miss keycap clusters
				// (#️⃣ 0️⃣–9️⃣ *️⃣) — exactly the emoji @jennings reported
				// against v1.9.3 — so PR #948's swap to ansi.Truncate alone
				// still let oversized lines past the gate and reproduced
				// #937's per-frame row-offset drift. See cellwidth.go.
				safe = cellTruncate(safe, contentWidth, "...")
				lines = append(lines, notesStyle.Render(safe))
			}
			if overflow && len(lines) > 0 {
				more := len(rawLines) - notesBodyLines
				lines[len(lines)-1] = hintStyle.Render(fmt.Sprintf("... +%d more lines", more))
			}
		}

		hintText := "Notes hotkey unbound"
		if editKey := h.actionKey(hotkeyEditNotes); editKey != "" {
			hintText = fmt.Sprintf("%s edit notes", editKey)
		}
		lines = append(lines, hintStyle.Render(hintText))
	}

	if len(lines) > maxLines {
		lines = lines[:maxLines]
	}
	for len(lines) < maxLines {
		lines = append(lines, "")
	}

	return strings.Join(lines, "\n")
}

// stripControlCharsPreserveANSI removes dangerous C0 control characters while
// preserving ANSI escape sequences (ESC = 0x1b). This allows terminal colors
// and formatting from capture-pane -e output to pass through to display, while
// still stripping \r, \b, and other control chars that corrupt TUI layout.
func stripControlCharsPreserveANSI(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' && r != '\x1b' {
			return -1 // Drop the character
		}
		return r
	}, s)
}

// ansiBackgroundRE matches ANSI background color escape sequences:
//   - ESC[40m..ESC[47m  — standard 8-color backgrounds
//   - ESC[100m..ESC[107m — bright/high-intensity backgrounds
//   - ESC[48;...m        — 256-color and true-color backgrounds (ESC[48;5;Nm / ESC[48;2;R;G;Bm)
var ansiBackgroundRE = regexp.MustCompile(`\x1b\[(?:4[0-7]|10[0-7]|48;[0-9;]+)m`)

// eraseEscapesRE matches CSI "Erase in Line" (ESC [ ... K) and CSI
// "Erase in Display" (ESC [ ... J). These tell the outer terminal to
// paint the currently-active SGR background across the remainder of
// the physical row (K) or screen (J). When captured content is
// rendered inside the preview pane, that paint extends past the pane
// boundary — the regression reported as #579, where Neovim's
// mini.statusline leaks its green bar past the right edge. Stripping
// K/J makes every cell the preview emits explicit, so the outer
// terminal paints only what fits the truncation budget.
var eraseEscapesRE = regexp.MustCompile(`\x1b\[[0-9;?]*[KJ]`)

// stripDisplayErasingEscapes removes CSI K / CSI J sequences from
// captured terminal content while preserving SGR (color/attribute)
// sequences.
func stripDisplayErasingEscapes(s string) string {
	return eraseEscapesRE.ReplaceAllString(s, "")
}

// previewSurfaceANSI returns a truecolor ANSI background sequence matching
// the current preview surface. Falls back to empty string if the color is not
// a hex RGB value.
func previewSurfaceANSI() string {
	hex := strings.TrimPrefix(string(ColorSurface), "#")
	if len(hex) != 6 {
		return ""
	}
	r, err := strconv.ParseUint(hex[0:2], 16, 8)
	if err != nil {
		return ""
	}
	g, err := strconv.ParseUint(hex[2:4], 16, 8)
	if err != nil {
		return ""
	}
	b, err := strconv.ParseUint(hex[4:6], 16, 8)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r, g, b)
}

// remapANSIBackground replaces dark ANSI background color sequences with the
// provided replacement while preserving all other ANSI sequences (foreground
// colors, bold, italic, underline) and light backgrounds. Used in light theme
// so dark bands from captured terminal output (#322) do not bleed through,
// while a tool's own light backgrounds survive unchanged (#2449).
func remapANSIBackground(s, replacement string) string {
	return ansiBackgroundRE.ReplaceAllStringFunc(s, func(seq string) string {
		if !isDarkANSIBackground(seq) {
			return seq
		}
		return replacement
	})
}

// truncatePath shortens a path to fit within maxLen display width.
//
// #937 v2: width and truncate route through cellWidth/cellTruncate (which
// promote keycap clusters such as #️⃣ to 2 cells on top of ansi/uniseg's
// VS16 handling). ansi.* alone — what PR #948 shipped — still let
// keycap-prefixed titles past the truncation gate and reproduced #937's
// row-offset drift on titles like "#️⃣ /Users/foo/keycap-channel".
// See internal/ui/cellwidth.go for the upstream disagreement that
// motivates this shim.
func truncatePath(path string, maxLen int) string {
	pathWidth := cellWidth(path)
	if pathWidth <= maxLen {
		return path
	}
	if maxLen < 10 {
		maxLen = 10
	}
	// Show beginning and end: /Users/.../project
	// Both ends are cut by display width, so wide runes (CJK) keep fewer runes.
	runes := []rune(path)
	startLen := maxLen / 3
	endLen := maxLen*2/3 - 3
	if startLen+endLen+3 > len(runes) {
		// Path is short in runes but wide in display - use width-aware truncation
		return cellTruncate(path, maxLen-3, "...")
	}
	return cellTruncate(path, startLen, "") + "..." + cellTruncateLeft(path, endLen)
}

// pickBadgeTime returns the most recent of the five signals the session-row
// timestamp badge layers over, ignoring any signal that is unset / not
// observed. Pure function — kept out of renderSessionItem so the 5-layer
// composition can be unit-tested without faking renderer dependencies.
//
// lastActivityAt is the durable record from tool_data.last_activity_at
// (issue #1846): hook-evidenced activity that survives attach-return
// deleting the hook file and TUI restarts losing the tmux tracker.
//
// LastAccessedAt is deliberately not a parameter: peeking at a quiet
// session isn't an "update". (sessionActivityTime layers it in as a
// fallback when no activity evidence exists at all.)
func pickBadgeTime(createdAt, lastStartedAt time.Time, hookEvent *session.HookStatus, lastActivityAt time.Time, confirmedActivity time.Time, confirmedObserved bool) time.Time {
	ts := createdAt
	if lastStartedAt.After(ts) {
		ts = lastStartedAt
	}
	if hookEvent != nil && hookEvent.UpdatedAt.After(ts) {
		ts = hookEvent.UpdatedAt
	}
	if lastActivityAt.After(ts) {
		ts = lastActivityAt
	}
	if confirmedObserved && confirmedActivity.After(ts) {
		ts = confirmedActivity
	}
	return ts
}

// sessionActivityTime is the single "last activity" composition BOTH the
// session-row badge and the preview's "⏱" line render from (issue #1846:
// the two surfaces used different formulas over different stale fallbacks
// and showed two different wrong ages for the same session).
//
// Layering: pickBadgeTime's activity evidence wins outright; LastAccessedAt
// participates only as a fallback when no evidence beyond CreatedAt exists —
// a TUI attach is a better floor than creation time, but a peek at a quiet
// session must never override recorded activity.
func sessionActivityTime(createdAt, lastStartedAt, lastActivityAt, lastAccessedAt, confirmedActivity time.Time, confirmedObserved bool, hookEvent *session.HookStatus) time.Time {
	ts := pickBadgeTime(createdAt, lastStartedAt, hookEvent, lastActivityAt, confirmedActivity, confirmedObserved)
	if ts.Equal(createdAt) && lastAccessedAt.After(ts) {
		return lastAccessedAt
	}
	return ts
}

// formatRelativeTime formats a time as a human-readable relative string using
// the shared compact two-component formatter (see humanizeSince). Examples:
// "just now", "45m ago", "3h 20m ago", "2d 5h ago", "5mo 1w ago".
func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return humanizeSince(time.Since(t))
}

// renderGroupPreview renders the preview pane for a group
func (h *Home) renderGroupPreview(group *session.Group, width, height int) string {
	var b strings.Builder

	// Group header with folder icon
	headerStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true)
	b.WriteString(headerStyle.Render("📁 " + group.Name))
	b.WriteString("\n\n")

	// Archived cross-harness sources superseded by a "Restart with new
	// session ID" stay in the group tree (so toggling to the archived view
	// still finds them) but must not leak into this preview: their old tmux
	// session can still be alive, and list --json already hides them, so
	// this panel has to derive from the same tracked set or it overcounts
	// and shows phantom rows for sessions the enumerable list doesn't have.
	visibleSessions := session.VisibleInstances(group.Sessions)

	// Session count
	countStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)
	b.WriteString(countStyle.Render(fmt.Sprintf("%d sessions", len(visibleSessions))))
	b.WriteString("\n\n")

	// Status breakdown with inline badges
	running, waiting, idle, stopped, errored := 0, 0, 0, 0, 0
	for _, sess := range visibleSessions {
		switch statusBucket(sess.Status) {
		case session.StatusRunning:
			running++
		case session.StatusWaiting:
			waiting++
		case session.StatusIdle:
			idle++
		case session.StatusStopped:
			// Issue #953: keep stopped separate from errored in the group
			// preview panel for the same reason as the header counter.
			stopped++
		case session.StatusError:
			errored++
		}
	}

	// Compact status line (inline, not badges)
	var statuses []string
	if running > 0 {
		statuses = append(
			statuses,
			lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)),
		)
	}
	if waiting > 0 {
		statuses = append(
			statuses,
			lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)),
		)
	}
	if idle > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("○ %d idle", idle)))
	}
	if stopped > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorTextDim).Render(fmt.Sprintf("■ %d stopped", stopped)))
	}
	if errored > 0 {
		statuses = append(statuses, lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("✕ %d error", errored)))
	}

	if len(statuses) > 0 {
		b.WriteString(strings.Join(statuses, "  "))
		b.WriteString("\n\n")
	}

	// Repository worktree summary (when all sessions share the same repo root)
	if repoInfo := h.getGroupWorktreeInfo(group); repoInfo != nil {
		b.WriteString(renderSectionDivider("Repository", width-4))
		b.WriteString("\n")

		repoLabelStyle := lipgloss.NewStyle().Foreground(ColorText)
		repoValueStyle := lipgloss.NewStyle().Foreground(ColorText)
		repoBranchStyle := lipgloss.NewStyle().Foreground(ColorCyan).Bold(true)

		b.WriteString(repoLabelStyle.Render("Repo:       "))
		b.WriteString(repoValueStyle.Render(truncatePath(repoInfo.repoRoot, width-4-12)))
		b.WriteString("\n")

		b.WriteString(repoLabelStyle.Render("Worktrees:  "))
		b.WriteString(repoValueStyle.Render(fmt.Sprintf("%d active", len(repoInfo.branches))))
		b.WriteString("\n")

		for _, br := range repoInfo.branches {
			dirtyMark := ""
			if br.dirtyChecked {
				if br.isDirty {
					dirtyMark = lipgloss.NewStyle().Foreground(ColorYellow).Render(" (dirty)")
				} else {
					dirtyMark = lipgloss.NewStyle().Foreground(ColorGreen).Render(" (clean)")
				}
			}
			b.WriteString("  ")
			b.WriteString(repoBranchStyle.Render("• " + br.branch))
			b.WriteString(dirtyMark)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Sessions divider
	b.WriteString(renderSectionDivider("Sessions", width-4))
	b.WriteString("\n")

	// Session list (compact)
	if len(visibleSessions) == 0 {
		emptyStyle := lipgloss.NewStyle().Foreground(ColorText).Italic(true)
		b.WriteString(emptyStyle.Render("  No sessions in this group"))
		b.WriteString("\n")
	} else {
		maxShow := height - 12
		if maxShow < 3 {
			maxShow = 3
		}
		for i, sess := range visibleSessions {
			if i >= maxShow {
				remaining := len(visibleSessions) - i
				b.WriteString(DimStyle.Render(fmt.Sprintf("  ... +%d more", remaining)))
				break
			}

			// Status icon
			statusIcon := "○"
			statusColor := ColorTextDim
			switch sess.Status {
			case session.StatusRunning:
				statusIcon, statusColor = "●", ColorGreen
			case session.StatusWaiting:
				statusIcon, statusColor = "◐", ColorYellow
			case session.StatusError:
				statusIcon, statusColor = "✕", ColorRed
			case session.StatusStopped:
				statusIcon, statusColor = "■", ColorTextDim
			}
			status := lipgloss.NewStyle().Foreground(statusColor).Render(statusIcon)
			name := lipgloss.NewStyle().Foreground(ColorText).Render(sess.Title)
			tool := lipgloss.NewStyle().Foreground(ColorPurple).Faint(true).Render(sess.Tool)

			b.WriteString(fmt.Sprintf("  %s %s %s\n", status, name, tool))
		}
	}

	// Keyboard hints at bottom
	b.WriteString("\n")
	hintStyle := lipgloss.NewStyle().Foreground(ColorComment).Italic(true)
	hints := []string{"Tab toggle"}
	if key := h.actionKey(hotkeyRename); key != "" {
		hints = append(hints, key+" rename")
	}
	if key := h.actionKey(hotkeyDelete); key != "" {
		hints = append(hints, key+" delete")
	}
	if key := h.actionKey(hotkeyCreateGroup); key != "" {
		hints = append(hints, key+" subgroup")
	}
	b.WriteString(hintStyle.Render(strings.Join(hints, " • ")))

	// CRITICAL: Enforce width constraint on ALL lines to prevent overflow into left panel
	maxWidth := max(width-2, 20)

	result := b.String()
	lines := strings.Split(result, "\n")
	var truncatedLines []string
	for _, line := range lines {
		cleanLine := tmux.StripANSI(line)
		// #937 v2: cellWidth + cellTruncate add keycap-cluster handling on
		// top of ansi/uniseg's VS16 awareness, so group-preview lines
		// containing #️⃣ 0️⃣–9️⃣ *️⃣ (jennings's pane-content reopen) stay
		// inside the panel budget. See cellwidth.go.
		displayWidth := cellWidth(cleanLine)
		if displayWidth > maxWidth {
			truncated := cellTruncate(cleanLine, maxWidth-3, "...")
			truncatedLines = append(truncatedLines, truncated)
		} else {
			truncatedLines = append(truncatedLines, line)
		}
	}

	return strings.Join(truncatedLines, "\n")
}

// groupWorktreeBranch holds info about a single worktree branch in a group
type groupWorktreeBranch struct {
	branch       string
	isDirty      bool
	dirtyChecked bool
}

// groupWorktreeInfo holds aggregated worktree info for a group sharing a common repo
type groupWorktreeInfo struct {
	repoRoot string
	branches []groupWorktreeBranch
}

// getGroupWorktreeInfo returns worktree summary if all sessions in the group
// share the same repo root and at least one is a worktree. Returns nil otherwise.
func (h *Home) getGroupWorktreeInfo(group *session.Group) *groupWorktreeInfo {
	if len(group.Sessions) < 2 {
		return nil
	}

	// Check if all sessions share a common repo root and count worktrees
	var commonRepo string
	var branches []groupWorktreeBranch
	for _, sess := range group.Sessions {
		if !sess.IsWorktree() {
			continue
		}
		if commonRepo == "" {
			commonRepo = sess.WorktreeRepoRoot
		} else if sess.WorktreeRepoRoot != commonRepo {
			return nil // Different repos, skip
		}

		// Get dirty status from cache
		h.worktreeDirtyMu.Lock()
		isDirty, hasCached := h.worktreeDirtyCache[sess.ID]
		h.worktreeDirtyMu.Unlock()

		branches = append(branches, groupWorktreeBranch{
			branch:       sess.WorktreeBranch,
			isDirty:      isDirty,
			dirtyChecked: hasCached,
		})
	}

	if len(branches) == 0 {
		return nil
	}

	return &groupWorktreeInfo{
		repoRoot: commonRepo,
		branches: branches,
	}
}

// renderSystemStatsBlock renders a detailed system stats block for the empty state preview pane.
func (h *Home) renderSystemStatsBlock(width int) string {
	if h.sysStatsCollector == nil {
		return ""
	}

	stats := h.sysStatsCollector.Get()
	show := h.sysStatsConfig.GetShow()
	showSet := make(map[string]bool, len(show))
	for _, s := range show {
		showSet[s] = true
	}

	labelStyle := lipgloss.NewStyle().Foreground(ColorComment).Width(8)
	valStyle := lipgloss.NewStyle().Foreground(ColorText)
	barWidth := width - 20
	if barWidth < 10 {
		barWidth = 10
	}
	if barWidth > 40 {
		barWidth = 40
	}

	var lines []string

	titleStyle := lipgloss.NewStyle().Foreground(ColorAccent).Bold(true)
	lines = append(lines, titleStyle.Render("  System"))
	lines = append(lines, "")

	if showSet["cpu"] && stats.CPU.Available {
		bar := renderBar(stats.CPU.UsagePercent, barWidth)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("CPU"), bar, valStyle.Render(fmt.Sprintf("%.0f%%", stats.CPU.UsagePercent))))
	}
	if showSet["ram"] && stats.Memory.Available {
		bar := renderBar(stats.Memory.UsagePercent, barWidth)
		used := sysinfo.FormatBytes(stats.Memory.UsedBytes)
		total := sysinfo.FormatBytes(stats.Memory.TotalBytes)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("RAM"), bar, valStyle.Render(fmt.Sprintf("%s/%s", used, total))))
	}
	if showSet["disk"] && stats.Disk.Available {
		bar := renderBar(stats.Disk.UsagePercent, barWidth)
		used := sysinfo.FormatBytes(stats.Disk.UsedBytes)
		total := sysinfo.FormatBytes(stats.Disk.TotalBytes)
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render("Disk"), bar, valStyle.Render(fmt.Sprintf("%s/%s", used, total))))
	}
	if showSet["load"] && stats.Load.Available {
		lines = append(lines, fmt.Sprintf("  %s %s", labelStyle.Render("Load"), valStyle.Render(fmt.Sprintf("%.2f  %.2f  %.2f", stats.Load.Load1, stats.Load.Load5, stats.Load.Load15))))
	}
	if showSet["gpu"] && stats.GPU.Available {
		bar := renderBar(stats.GPU.UsagePercent, barWidth)
		label := "GPU"
		if stats.GPU.Name != "" {
			label = "GPU"
		}
		lines = append(lines, fmt.Sprintf("  %s %s %s", labelStyle.Render(label), bar, valStyle.Render(fmt.Sprintf("%.0f%%", stats.GPU.UsagePercent))))
	}
	if showSet["network"] && stats.Network.Available {
		rx := sysinfo.FormatBytesPerSec(stats.Network.RxBytesPerSec)
		tx := sysinfo.FormatBytesPerSec(stats.Network.TxBytesPerSec)
		lines = append(lines, fmt.Sprintf("  %s %s", labelStyle.Render("Net"), valStyle.Render(fmt.Sprintf("↓ %s  ↑ %s", rx, tx))))
	}

	if len(lines) <= 2 {
		return ""
	}

	return strings.Join(lines, "\n")
}

// renderBar creates an ASCII progress bar: [████░░░░░░]
func renderBar(percent float64, width int) string {
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}

	filled := int(percent / 100 * float64(width))
	empty := width - filled

	var color lipgloss.Color
	switch {
	case percent >= 90:
		color = ColorRed
	case percent >= 70:
		color = ColorYellow
	default:
		color = ColorGreen
	}

	filledStyle := lipgloss.NewStyle().Foreground(color)
	emptyStyle := lipgloss.NewStyle().Foreground(ColorBorder)

	return filledStyle.Render(strings.Repeat("█", filled)) + emptyStyle.Render(strings.Repeat("░", empty))
}

// remoteAccountsLine is the per-session preview's account usage line: the
// session's own slot (rs.Account, "" = default) from the cached host stats,
// with the poll age. Never blank: an unpolled host or older remote says so.
func (h *Home) remoteAccountsLine(remoteName, account string, now time.Time) string {
	result, ok := h.remoteHostStatsState(remoteName)
	if !ok {
		// No stats entry: either never polled or the remote cannot report
		// them. Only blame the remote when it is known to be older.
		versionState, _ := h.remoteVersionState(remoteName)
		if versionState.Compare(Version) == session.RemoteVersionOlder {
			return remoteStatsUnknownLine(versionState, Version)
		}
		return "accounts not polled yet"
	}
	if !result.Stats.AccountsAvailable {
		return "accounts unknown (remote does not report accounts)"
	}
	slot := account
	if slot == "" {
		slot = "default"
	}
	for _, u := range result.Stats.Accounts {
		if u.Name == slot {
			return fmt.Sprintf("account   %s · polled %s", renderAccountUsageEntry(u, now), remoteStatsPolledLabel(result.FetchedAt))
		}
	}
	return fmt.Sprintf("account unknown (slot %q not reported by remote)", slot)
}
