package ui

import (
	"fmt"
	"log/slog"
	"runtime"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/costs"
	"github.com/asheshgoplani/agent-deck/internal/logging"
	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/sysinfo"
)

// getLayoutMode returns the current layout mode based on terminal width
// and the user's preview-orientation preference. On wide terminals the
// user can opt into the stacked (PREVIEW-below) layout via
// preview_orientation = "below"; narrow terminals always stack.
func (h *Home) getLayoutMode() string {
	if h.embeddedMode {
		if h.width < layoutBreakpointStacked {
			return LayoutModeStacked
		}
		return LayoutModeDual
	}
	switch {
	case h.width < layoutBreakpointSingle:
		return LayoutModeSingle
	case h.width < layoutBreakpointStacked:
		return LayoutModeStacked
	case h.previewOrientation == PreviewOrientationBelow:
		// Wide terminal, but the user prefers PREVIEW stacked below the
		// sessions list (portrait-monitor friendly).
		return LayoutModeStacked
	default:
		return LayoutModeDual
	}
}

// contentChromeTop returns the number of rows rendered ABOVE the main content
// area (the header line + filter bar + optional update/maintenance banners).
// The debug footer is NOT included — it renders below the content, so it
// doesn't shift the content's top edge. Kept in one place so mouse Y-routing
// and the render path agree on where content starts.
func (h *Home) contentChromeTop() int {
	top := 1 // header line
	top++    // filter bar (always shown, matches View())
	if h.shouldRenderUpdateBanner() {
		top++
	}
	if h.maintenanceMsg != "" {
		top++
	}
	return top
}

// stackedPreviewTopY returns the screen Y (0-indexed) of the first row of the
// PREVIEW region in the stacked layout, i.e. the row just below the horizontal
// separator. A mouse-wheel event at or below this Y is over the preview and
// should scroll it rather than move the list cursor. Returns -1 when the
// current layout is not stacked. Mirrors renderStackedLayout's height split:
//
//	[content-top] SESSIONS title (panelTitleLines) + list (listHeight-title)
//	              + separator (1)  <- divider
//	[preview-top] PREVIEW title + preview content
func (h *Home) stackedPreviewTopY() int {
	if h.getLayoutMode() != LayoutModeStacked {
		return -1
	}
	const helpBarHeight = 2
	filterBarHeight := 1
	updateBannerHeight := 0
	if h.shouldRenderUpdateBanner() {
		updateBannerHeight = 1
	}
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
	}
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight - debugBarHeight - h.promptInputDialog.ReservedHeight()
	listHeight := h.stackedListHeight(contentHeight)
	// content top + full list block (title + body) + the 1-row separator.
	return h.contentChromeTop() + listHeight + 1
}

// renderHeaderSSHSegments renders the header's "ssh" field from the local
// collector: the full one-line form and the compact "ssh  N users" form the
// width-aware layout falls back to. Unknown (collector missing or not yet
// polled, `who` failed) is said, never rendered as nobody.
func (h *Home) renderHeaderSSHSegments(now time.Time) (full, compact string) {
	style := lipgloss.NewStyle().Foreground(ColorComment)
	if h.sshCollector == nil {
		return style.Render("ssh  unknown"), ""
	}
	snap := h.sshCollector.Get()
	if !snap.Available {
		if snap.Error != "" {
			return style.Render("ssh  unknown (" + snap.Error + ")"), style.Render("ssh  unknown")
		}
		return style.Render("ssh  unknown"), ""
	}
	sessions := make([]session.RemoteSSHSession, 0, len(snap.Sessions))
	for _, s := range snap.Sessions {
		sessions = append(sessions, session.RemoteSSHSession{User: s.User, Count: s.Count, HasSince: s.HasSince, Since: s.Since, From: s.From})
	}
	full = style.Render(renderSSHPreviewBlock(sessions, now, previewLayout{})[0])
	if len(sessions) == 0 {
		return full, ""
	}
	return full, style.Render(renderSSHSummaryLine(sessions))
}

// countSessionStatuses counts sessions by status for the logo display
// Uses cache to avoid O(n) iteration on every View() call
// Cache expires after 500ms to balance freshness with performance
// PERFORMANCE: Increased from 100ms to 500ms - status changes are rare
// during UI interaction, and longer cache reduces View() overhead
func (h *Home) countSessionStatuses() (running, waiting, idle, stopped, errored int) {
	// Return cached values if valid and not expired
	const cacheDuration = 500 * time.Millisecond
	if h.cachedStatusCounts.valid.Load() &&
		time.Since(h.cachedStatusCounts.timestamp) < cacheDuration {
		return h.cachedStatusCounts.running, h.cachedStatusCounts.waiting,
			h.cachedStatusCounts.idle, h.cachedStatusCounts.stopped,
			h.cachedStatusCounts.errored
	}

	// Compute counts
	snapshot := h.getSessionRenderSnapshot()
	if snapshot == nil {
		h.refreshSessionRenderSnapshot(nil)
		snapshot = h.getSessionRenderSnapshot()
	}
	// Skip every archived session, so this pill agrees with `agent-deck
	// status` and the web menu, which both drop archived sessions. This
	// covers archived cross-harness sources superseded by a "Restart with new
	// session ID" (their old tmux session can still be alive) and archived
	// workers left in "waiting": the background sweep skips archived rows,
	// so their status would never clear and they would count forever.
	for _, state := range snapshot {
		if state.archived {
			continue
		}
		switch statusBucket(state.status) {
		case session.StatusRunning:
			running++
		case session.StatusWaiting:
			waiting++
		case session.StatusIdle:
			idle++
		case session.StatusStopped:
			// Issue #953 (re-opened): manually-stopped sessions get their
			// own bucket so the header counter does not slander them as
			// errors. StatusStopped reaches here only via i.Kill()'s
			// canonical contract (see internal/session/instance.go) —
			// crashes still surface as StatusError below.
			stopped++
		case session.StatusError:
			errored++
		}
	}

	// Include remote sessions (issue #1066). Remotes carry their status as a
	// lowercase string from the remote's `agent-deck list --json` (see
	// internal/session/discovery.go for the canonical mapping). The counter
	// previously only iterated the local-instance snapshot, so the header
	// pill read 0 for users with only remote sessions.
	h.remoteSessionsMu.RLock()
	for name, sessions := range h.remoteSessions {
		if state, known := h.remotePolls[name]; h.remoteFromCache[name] || (known && state.LastPollStatus != "ok") {
			continue
		}
		// A snapshot older than remoteRowStaleAge renders dimmed; its
		// running rows must not feed the green pill either.
		age, known := h.remoteRowAgeLocked(name)
		stale := known && age >= remoteRowStaleAge
		for _, rs := range sessions {
			switch statusBucket(session.Status(rs.Status)) {
			case session.StatusRunning:
				if stale {
					continue
				}
				running++
			case session.StatusWaiting:
				waiting++
			case session.StatusIdle:
				idle++
			case session.StatusStopped:
				stopped++
			case session.StatusError:
				errored++
			}
		}
	}
	h.remoteSessionsMu.RUnlock()

	// Cache results with timestamp
	h.cachedStatusCounts.running = running
	h.cachedStatusCounts.waiting = waiting
	h.cachedStatusCounts.idle = idle
	h.cachedStatusCounts.stopped = stopped
	h.cachedStatusCounts.errored = errored
	h.cachedStatusCounts.valid.Store(true)
	h.cachedStatusCounts.timestamp = time.Now()
	return running, waiting, idle, stopped, errored
}

// renderFilterBar renders the quick filter pills
// Format: [All] [● Running 2] [◐ Waiting 1] [○ Idle 5] [■ Stopped 1] [✕ Error 1]
func (h *Home) renderFilterBar() string {
	running, waiting, idle, stopped, errored := h.countSessionStatuses()

	// Pill styling
	activePillStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)

	dimPillStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Faint(true).
		Padding(0, 1)

	// Build pills
	var pills []string

	// The first pill names the selected status even when its count pill is
	// farther to the right than a narrow terminal can show.
	isActive := h.statusFilter == FilterModeActive
	filterLabel := h.statusFilterLabel()
	// Keep the existing All-pill spacing in the default view.
	if h.statusFilter != "" {
		pills = append(pills, activePillStyle.Render(filterLabel))
	} else {
		pills = append(pills, activePillStyle.Render("All")+" ")
	}

	runningLabel := fmt.Sprintf("● %d", running)
	if h.statusFilter == session.StatusRunning {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorGreen).
			Bold(true).
			Padding(0, 1).Render(runningLabel))
	} else if isActive && h.activeFilterExcludes[session.StatusRunning] {
		pills = append(pills, dimPillStyle.Render(runningLabel))
	} else if running > 0 {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorGreen).
			Background(ColorSurface).
			Padding(0, 1).Render(runningLabel))
	} else {
		pills = append(pills, dimPillStyle.Render(runningLabel))
	}

	waitingLabel := fmt.Sprintf("◐ %d", waiting)
	if h.statusFilter == session.StatusWaiting {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorYellow).
			Bold(true).
			Padding(0, 1).Render(waitingLabel))
	} else if isActive && h.activeFilterExcludes[session.StatusWaiting] {
		pills = append(pills, dimPillStyle.Render(waitingLabel))
	} else if waiting > 0 {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorYellow).
			Background(ColorSurface).
			Padding(0, 1).Render(waitingLabel))
	} else {
		pills = append(pills, dimPillStyle.Render(waitingLabel))
	}

	idleLabel := fmt.Sprintf("○ %d", idle)
	if h.statusFilter == session.StatusIdle {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorTextDim).
			Bold(true).
			Padding(0, 1).Render(idleLabel))
	} else if isActive && h.activeFilterExcludes[session.StatusIdle] {
		pills = append(pills, dimPillStyle.Render(idleLabel))
	} else if idle == 0 {
		pills = append(pills, dimPillStyle.Render(idleLabel))
	} else {
		pills = append(pills, lipgloss.NewStyle().
			Foreground(ColorText).
			Background(ColorSurface).
			Padding(0, 1).Render(idleLabel))
	}

	// Stopped pill (issue #953): manually-stopped sessions deserve their own
	// affordance — they're not errors, they're intentional. Render-only-if
	// non-zero or actively filtered, mirroring the error pill's pattern, so
	// the bar stays compact when no stopped sessions exist.
	if stopped > 0 || h.statusFilter == session.StatusStopped {
		stoppedLabel := fmt.Sprintf("■ %d", stopped)
		if h.statusFilter == session.StatusStopped {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorTextDim).
				Bold(true).
				Padding(0, 1).Render(stoppedLabel))
		} else if isActive && (h.activeFilterExcludes[session.StatusStopped] || h.activeFilterHideStopped) {
			pills = append(pills, dimPillStyle.Render(stoppedLabel))
		} else if stopped > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorTextDim).
				Background(ColorSurface).
				Padding(0, 1).Render(stoppedLabel))
		}
	}

	if errored > 0 || h.statusFilter == session.StatusError {
		errorLabel := fmt.Sprintf("✕ %d", errored)
		if h.statusFilter == session.StatusError {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorBg).
				Background(ColorRed).
				Bold(true).
				Padding(0, 1).Render(errorLabel))
		} else if isActive && h.activeFilterExcludes[session.StatusError] {
			pills = append(pills, dimPillStyle.Render(errorLabel))
		} else if errored > 0 {
			pills = append(pills, lipgloss.NewStyle().
				Foreground(ColorRed).
				Background(ColorSurface).
				Padding(0, 1).Render(errorLabel))
		}
	}

	// Join pills with spaces (leading space replaces Padding)
	pillsRow := " " + strings.Join(pills, " ")
	hint := h.fitFilterBarHint(cellWidth(pillsRow))

	return lipgloss.NewStyle().
		MaxWidth(h.width).
		Render(pillsRow + hint)
}

func (h *Home) statusFilterLabel() string {
	switch h.statusFilter {
	case FilterModeActive:
		if h.activeFilterLabel != "" {
			return h.activeFilterLabel
		}
		return "Open"
	case FilterModeArchived:
		return "Archived"
	case session.StatusRunning:
		return "Running"
	case session.StatusWaiting:
		return "Waiting"
	case session.StatusIdle:
		return "Idle"
	case session.StatusStopped:
		return "Stopped"
	case session.StatusError:
		return "Error"
	default:
		return "All"
	}
}

// fitFilterBarHint fits as many leading filter-bar hint segments as possible
// into the space remaining after usedWidth (the pills already rendered),
// dropping legend segments before selected modes when the full hint would
// overflow h.width. A trailing
// ellipsis is appended only when a segment was actually dropped, and the cut
// always lands on a "• " boundary rather than mid-word.
func (h *Home) fitFilterBarHint(usedWidth int) string {
	dim := lipgloss.NewStyle().Foreground(ColorComment).Faint(true)
	const leadIn = "  "
	const sep = " • "

	segs := h.renderFilterBarHint()
	available := h.width - usedWidth
	// An uninitialized terminal width, or pills that already fill the bar,
	// leave nothing meaningful to budget against: keep every segment and let
	// the caller's MaxWidth clamp.
	unconstrained := h.width <= 0 || available < 0

	// Reserve room for a trailing " • …" up front: a segment that fits on
	// its own but leaves no space for the ellipsis afterward must still be
	// dropped, or the appended ellipsis would itself overflow the width.
	sepWidth := cellWidth(sep)
	ellipsisReserve := sepWidth + cellWidth("…")

	used := cellWidth(leadIn)
	kept := 0
	for i, seg := range segs {
		next := used + cellWidth(seg)
		if kept > 0 {
			next += sepWidth
		}
		if !unconstrained {
			budget := available
			if i < len(segs)-1 {
				budget -= ellipsisReserve
			}
			if next > budget {
				break
			}
		}
		used = next
		kept++
	}

	hint := dim.Render(leadIn) + strings.Join(segs[:kept], dim.Render(sep))
	if kept < len(segs) {
		if kept > 0 {
			hint += dim.Render(sep + "…")
		} else {
			hint = dim.Render(leadIn + "…")
		}
	}
	return hint
}

// updateSizes updates component sizes
func (h *Home) updateSizes() {
	h.search.SetSize(h.width, h.height)
	h.newDialog.SetSize(h.width, h.height)
	h.groupDialog.SetSize(h.width, h.height)
	h.confirmDialog.SetSize(h.width, h.height)
	h.hookTrustDialog.SetSize(h.width, h.height)
	h.geminiModelDialog.SetSize(h.width, h.height)
	if h.sessionSwitcher != nil {
		// Keep the switcher's viewport current so its standalone centered view
		// and the dashboard composite both respond to terminal resizes.
		h.sessionSwitcher.SetSize(h.width, h.height)
	}
	h.worktreeFinishDialog.SetSize(h.width, h.height)
	if h.feedbackDialog != nil {
		h.feedbackDialog.SetSize(h.width, h.height)
	}
	h.reportDialog.SetSize(h.width, h.height)
	if h.telemetryDialog != nil {
		h.telemetryDialog.SetSize(h.width, h.height)
	}
}

// View renders the UI
func (h *Home) View() string {
	// CRITICAL: Do not render fresh output during attach (Bubble Tea Issue #431
	// - View gets printed to stdout during tea.Exec). Historically this
	// returned "", which had two failure modes (#1753 black-screen family):
	//   - the frame Bubble Tea renders between Update returning the tea.Exec
	//     Cmd and the exec actually parking the loop cleared the screen to
	//     black before tmux took over;
	//   - any path that left the flag set when the loop resumed (an exec
	//     ExecCallback racing the first View, a Store(true) with no attach)
	//     kept the screen black until the next message — or forever.
	// Returning the last rendered frame is safe in both directions: while the
	// renderer is still running it diffs against the identical screen content
	// and writes nothing, and if the flag is ever stale after resume the user
	// sees the list (at worst a few frames old) instead of black. Only the
	// very first frame of the process can still be empty here, and no attach
	// can be triggered before the first render.
	if h.isAttaching.Load() { // Atomic read for thread safety
		return h.lastRenderedFrame
	}

	frame := h.renderFrame()
	if frame != "" {
		h.lastRenderedFrame = frame
	}
	if h.pendingFrameOp != "" && h.width > 0 && !h.initialLoading {
		optiming.Since(h.pendingFrameOp, h.pendingFrameStart)
		h.pendingFrameOp = ""
	}
	return frame
}

// renderFrame is the real frame builder behind View. Split out so View can
// cache the last rendered frame for the isAttaching fallback above without
// threading a save through every return site. Runs only on the Bubble Tea
// event-loop goroutine, as View always has.
func (h *Home) renderFrame() string {
	if h.width == 0 {
		return "Loading..."
	}

	var renderStart time.Time
	if logging.IsDebugEnabled() {
		renderStart = time.Now()
	}

	// Check minimum terminal size for usability
	if h.width < minTerminalWidth || h.height < minTerminalHeight {
		return lipgloss.Place(
			h.width, h.height,
			lipgloss.Center, lipgloss.Center,
			lipgloss.NewStyle().
				Foreground(ColorYellow).
				Render(fmt.Sprintf(
					"Terminal too small (%dx%d)\nMinimum: %dx%d",
					h.width, h.height,
					minTerminalWidth, minTerminalHeight,
				)),
		)
	}

	// A pending worktree hook approval is shown above everything else.
	if h.hookTrustDialog.IsVisible() {
		return h.hookTrustDialog.View()
	}

	// Show loading splash during initial session load
	if h.initialLoading {
		return renderLoadingSplash(h.width, h.height, h.animationFrame)
	}

	// Show quitting splash during shutdown
	if h.isQuitting {
		if h.restartRequested {
			return renderShutdownSplash(h.width, h.height, h.animationFrame, "Restarting...")
		}
		return renderQuittingSplash(h.width, h.height, h.animationFrame)
	}

	// Setup wizard takes over entire screen
	if h.setupWizard.IsVisible() {
		return h.setupWizard.View()
	}

	// Delivery and watcher panels are modal (before settings panel)
	if h.triggersPanel.IsVisible() {
		return h.triggersPanel.View()
	}
	if h.deadLetterPanel != nil && h.deadLetterPanel.IsVisible() {
		return h.deadLetterPanel.View()
	}
	if h.agentsPanel.IsVisible() {
		return h.agentsPanel.View()
	}
	if h.watcherPanel.IsVisible() {
		return h.watcherPanel.View()
	}

	if h.toolVisibilityPanel != nil && h.toolVisibilityPanel.IsVisible() {
		return h.toolVisibilityPanel.View()
	}

	// Settings panel is modal
	if h.settingsPanel.IsVisible() {
		return h.settingsPanel.View()
	}

	// Overlays take full screen
	if h.helpOverlay.IsVisible() {
		return h.helpOverlay.View()
	}
	if h.search.IsVisible() {
		return h.search.View()
	}
	if h.globalSearch.IsVisible() {
		return h.globalSearch.View()
	}
	if h.newDialog.IsVisible() {
		return h.newDialog.View()
	}
	if h.groupDialog.IsVisible() {
		return h.groupDialog.View()
	}
	if h.forkDialog.IsVisible() {
		return h.forkDialog.View()
	}
	if h.confirmDialog.IsVisible() {
		return h.confirmDialog.View()
	}
	if h.mcpDialog.IsVisible() {
		return h.mcpDialog.View()
	}
	if h.pluginDialog.IsVisible() {
		return h.pluginDialog.View()
	}
	if h.editSessionDialog.IsVisible() {
		return h.editSessionDialog.View()
	}
	if h.editPathsDialog.IsVisible() {
		return h.editPathsDialog.View()
	}
	if h.skillDialog.IsVisible() {
		return h.skillDialog.View()
	}
	if h.geminiModelDialog.IsVisible() {
		return h.geminiModelDialog.View()
	}
	if h.scrollbackPager.IsVisible() {
		return h.scrollbackPager.View()
	}
	if h.contextPager.IsVisible() {
		return h.contextPager.View()
	}
	if h.sessionPickerDialog.IsVisible() {
		return h.sessionPickerDialog.View()
	}
	if h.codeBlockDialog.IsVisible() {
		return h.codeBlockDialog.View()
	}
	if h.worktreeFinishDialog.IsVisible() {
		return h.worktreeFinishDialog.View()
	}
	if h.reportDialog.IsVisible() {
		return h.reportDialog.View()
	}
	if h.feedbackDialog.IsVisible() {
		return h.feedbackDialog.View()
	}
	if h.telemetryDialog != nil && h.telemetryDialog.IsVisible() {
		return h.telemetryDialog.View()
	}
	if h.zoxidePicker.IsVisible() {
		return h.zoxidePicker.View()
	}
	if h.showCostDashboard {
		return h.costDashboard.View()
	}

	// Reuse viewBuilder to reduce allocations (reset and pre-allocate)
	h.viewBuilder.Reset()
	h.viewBuilder.Grow(32768) // Pre-allocate 32KB for typical view size
	b := &h.viewBuilder

	// ═══════════════════════════════════════════════════════════════════
	// HEADER BAR
	// ═══════════════════════════════════════════════════════════════════
	// Calculate real session status counts for logo and stats
	running, waiting, idle, stopped, errored := h.countSessionStatuses()
	logo := RenderLogoCompact(running, waiting, idle)

	titleStyle := lipgloss.NewStyle().
		Bold(true).
		Foreground(ColorAccent)

	// Show profile in title if not default
	titleText := "Agent Deck"
	if h.profile != "" && h.profile != session.DefaultProfile {
		profileStyle := lipgloss.NewStyle().
			Foreground(ColorCyan).
			Bold(true)
		titleText = "Agent Deck " + profileStyle.Render("["+h.profile+"]")
	}
	if h.groupScope != "" {
		scopeStyle := lipgloss.NewStyle().
			Foreground(ColorPurple).
			Bold(true)
		titleText += " " + scopeStyle.Render("["+h.groupScopeDisplayName()+"]")
	}
	title := titleStyle.Render(titleText)

	// headerFields drives which of the header's optional segments render,
	// in the same shared vocabulary as [ui.remote_preview].fields (see
	// session.UISettings.GetHeaderFields). Unset config keeps every
	// existing segment, so the header is byte-identical to before this
	// config block existed.
	headerFields := session.DefaultHeaderFields
	if headerCfg, err := session.LoadUserConfig(); err == nil && headerCfg != nil {
		headerFields = headerCfg.UI.GetHeaderFields()
	}
	headerFieldSet := make(map[string]bool, len(headerFields))
	for _, f := range headerFields {
		headerFieldSet[f] = true
	}

	// Status-based stats (more useful than group/session counts)
	// Format: ● 2 running • ◐ 1 waiting • ○ 3 idle (• ✕ 1 error)
	var statsParts []string
	statsSep := lipgloss.NewStyle().Foreground(ColorBorder).Render(" • ")

	if headerFieldSet[session.PreviewFieldSessionsByStatus] && running > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorGreen).Render(fmt.Sprintf("● %d running", running)),
		)
	}
	if headerFieldSet[session.PreviewFieldSessionsByStatus] && waiting > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorYellow).Render(fmt.Sprintf("◐ %d waiting", waiting)),
		)
	}
	if headerFieldSet[session.PreviewFieldSessionsByStatus] && idle > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("○ %d idle", idle)),
		)
	}
	if headerFieldSet[session.PreviewFieldSessionsByStatus] && stopped > 0 {
		// Issue #953: stopped sessions get their own segment so users can see
		// at a glance how many sessions are intentionally off vs. errored.
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorTextDim).Render(fmt.Sprintf("■ %d stopped", stopped)),
		)
	}
	if headerFieldSet[session.PreviewFieldSessionsByStatus] && errored > 0 {
		statsParts = append(
			statsParts,
			lipgloss.NewStyle().Foreground(ColorRed).Render(fmt.Sprintf("✕ %d error", errored)),
		)
	}

	// Fallback if no sessions
	stats := ""
	if len(statsParts) > 0 {
		stats = strings.Join(statsParts, statsSep)
	} else if headerFieldSet[session.PreviewFieldSessionsByStatus] {
		stats = lipgloss.NewStyle().Foreground(ColorText).Render("no sessions")
	}

	// Compact fallback for the base status-counts segment: a busy fleet's
	// per-status breakdown (e.g. "● 12 running • ◐ 8 waiting • ○ 24 idle •
	// ■ 6 stopped • ✕ 3 error") can by itself overflow a narrow terminal
	// even with every optional field already off, which the old
	// MaxWidth-only fallback truncated mid-text and dropped the version
	// badge. assembleHeaderLeft collapses to this single total before
	// giving up any further-protected field. Mirrors renderAccountsCompactLine's
	// "N slots" shape.
	statsCompact := stats
	if headerFieldSet[session.PreviewFieldSessionsByStatus] {
		total := running + waiting + idle + stopped + errored
		word := "session"
		if total != 1 {
			word = "sessions"
		}
		statsCompact = lipgloss.NewStyle().Foreground(ColorText).Render(fmt.Sprintf("%d %s", total, word))
	}

	// Cost tracking segment, rendered through the resolved template.
	// See session.ResolveCostLineTemplate for the [costs] / per-profile
	// override chain. RenderCostLine returns "" when hide_when_zero is on
	// and every recognized variable rendered to $0.00.
	// #1101: aggregate remote per-host summaries on top of local totals so the
	// status-line cost segment reflects spend across every configured host,
	// not only events written to the local cost_events table. Remotes whose
	// fetch failed contribute zero — the local figures still render.
	h.remoteCostsMu.RLock()
	remoteAgg := costs.MergeRemoteCostSummaries(h.remoteCosts)
	h.remoteCostsMu.RUnlock()
	costVars := map[string]int64{
		"cost_today":      h.costToday.Load() + remoteAgg.CostTodayMicrodollars,
		"cost_yesterday":  h.costYesterday.Load() + remoteAgg.CostYesterdayMicrodollars,
		"cost_this_week":  h.costWeek.Load() + remoteAgg.CostThisWeekMicrodollars,
		"cost_last_week":  h.costLastWeek.Load() + remoteAgg.CostLastWeekMicrodollars,
		"cost_this_month": h.costThisMonth.Load() + remoteAgg.CostThisMonthMicrodollars,
		"cost_last_month": h.costLastMonth.Load() + remoteAgg.CostLastMonthMicrodollars,
		"cost_projected":  h.costProjected.Load() + remoteAgg.CostProjectedMicrodollars,
	}
	var costSegment string
	if rendered := costs.RenderCostLine(h.costLineTemplate, costVars, h.costLineHideWhenZero); rendered != "" {
		costSegment = lipgloss.NewStyle().Foreground(ColorCyan).Render(rendered)
	}

	// System stats segment (CPU, RAM, etc.) — shown when [ui.header].fields
	// requests any of load/memory/disk; the existing [system_stats] config
	// still governs which of those sysinfo.Format actually renders.
	var sysStatsSegment string
	headerWantsHostStats := headerFieldSet[session.PreviewFieldLoad] || headerFieldSet[session.PreviewFieldMemory] || headerFieldSet[session.PreviewFieldDisk]
	if headerWantsHostStats && h.sysStatsCollector != nil {
		sysStats := h.sysStatsCollector.Get()
		if formatted := sysinfo.Format(sysStats, h.sysStatsConfig.GetFormat(), h.sysStatsConfig.GetShow()); formatted != "" {
			sysStatsSegment = lipgloss.NewStyle().Foreground(ColorComment).Render(formatted)
		}
	}

	// Accounts segment: named Claude account slots on this host with their
	// live 5h/7d usage, opt-in via [ui.header].fields (not part of
	// DefaultHeaderFields — see PreviewFieldAccounts). Reads this host's own
	// quota cache through h.accountsUsageCache, which re-parses a slot's
	// usage file only when its mtime changed (#PROMPT accounts field).
	//
	// Rendered in two forms: the full per-slot line, and a compact
	// "N slots · lowest 5h x%" fallback the width-aware layout below
	// switches to (instead of truncating the full line mid-text) when the
	// header is too narrow to show every optional field in full.
	var accountsSegment, accountsCompactSegment string
	acctStyle := lipgloss.NewStyle().Foreground(ColorComment)
	if headerFieldSet[session.PreviewFieldAccounts] {
		if headerCfg, err := session.LoadUserConfig(); err == nil && headerCfg != nil {
			usage := session.CollectAccountUsage(headerCfg, h.accountsUsageCache, time.Now())
			accountsSegment = acctStyle.Render(renderAccountsPreviewLine(usage, time.Now()))
			accountsCompactSegment = acctStyle.Render(renderAccountsCompactLine(usage))
		}
	}

	// SSH segment: who is connected to this host over SSH, opt-in via
	// [ui.header].fields. Read from the background collector's snapshot
	// (never a `who` on the render path); unknown until its first poll.
	// Full form is the one-line list, the compact fallback is the summary.
	var sshSegment, sshCompactSegment string
	if headerFieldSet[session.PreviewFieldSSH] {
		sshSegment, sshCompactSegment = h.renderHeaderSSHSegments(time.Now())
	}

	// Version badge (right-aligned, subtle inline style - no border to keep single line)
	versionBadge := ""
	if headerFieldSet[session.PreviewFieldVersion] {
		versionStyle := lipgloss.NewStyle().
			Foreground(ColorComment).
			Faint(true)
		versionBadge = versionStyle.Render("v" + Version)
	}

	// Width-aware assembly (issue: optional fields rendering at the far
	// right got cut off by MaxWidth's blind byte-truncation, pushing the
	// version badge off screen). Optional segments are appended in
	// least-protected-first order (dropped first → dropped last): accounts
	// drops to its compact form before being dropped entirely, then cost,
	// then sysStats (load/memory/disk) last — sysStats is protected longest
	// because it is the field an operator most needs on a busy host. The
	// version badge and profile-qualified title are never dropped or
	// truncated; when even every optional field gone still doesn't leave
	// room, the base status-counts segment itself collapses to a single
	// total (statsCompact) rather than falling through to MaxWidth's blind
	// mid-text byte truncation, which used to eat the badge on a busy
	// fleet even with every optional field off (#2301 round-2).
	optionalSegments := []string{sysStatsSegment, costSegment, accountsSegment, sshSegment}
	headerLeft, versionFits := assembleHeaderLeft(logo, title, stats, statsCompact, statsSep, optionalSegments, versionBadge, h.width)
	if !versionFits {
		// Retry with the ssh and accounts fields' compact forms before
		// dropping them outright.
		optionalSegments[3] = sshCompactSegment
		headerLeft, versionFits = assembleHeaderLeft(logo, title, stats, statsCompact, statsSep, optionalSegments, versionBadge, h.width)
	}
	if !versionFits {
		optionalSegments[2] = accountsCompactSegment
		headerLeft, versionFits = assembleHeaderLeft(logo, title, stats, statsCompact, statsSep, optionalSegments, versionBadge, h.width)
	}
	_ = versionFits // best-effort below this point; an unreasonably narrow terminal still gets a legible (if cramped) header

	headerPadding := h.width - lipgloss.Width(headerLeft) - lipgloss.Width(versionBadge) - 2
	if headerPadding < 1 {
		headerPadding = 1
	}
	headerContent := headerLeft + strings.Repeat(" ", headerPadding) + versionBadge

	headerBar := lipgloss.NewStyle().
		Background(ColorSurface).
		MaxWidth(h.width).
		Padding(0, 1).
		Render(headerContent)

	b.WriteString(headerBar)
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════════
	// FILTER BAR (quick status filters)
	// ═══════════════════════════════════════════════════════════════════
	// Always show filter bar for consistent layout (prevents viewport jumping)
	filterBarHeight := 1
	b.WriteString(h.renderFilterBar())
	b.WriteString("\n")

	// ═══════════════════════════════════════════════════════════════════
	// UPDATE BANNER (if update available)
	// ═══════════════════════════════════════════════════════════════════
	updateBannerHeight := 0
	if h.shouldRenderUpdateBanner() {
		updateBannerHeight = 1
		updateStyle := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorYellow).
			Bold(true).
			MaxWidth(h.width).
			Align(lipgloss.Center)
		b.WriteString(updateStyle.Render(h.renderUpdateBannerText()))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// MAINTENANCE BANNER (if maintenance completed recently)
	// ═══════════════════════════════════════════════════════════════════
	maintenanceBannerHeight := 0
	if h.maintenanceMsg != "" {
		maintenanceBannerHeight = 1
		maintStyle := lipgloss.NewStyle().
			Foreground(ColorBg).
			Background(ColorCyan).
			Bold(true).
			MaxWidth(h.width).
			Align(lipgloss.Center)
		b.WriteString(maintStyle.Render(" " + h.maintenanceMsg + " "))
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// MAIN CONTENT AREA - Responsive layout based on terminal width
	// ═══════════════════════════════════════════════════════════════════
	helpBarHeight := 2 // Help bar takes 2 lines (border + content)
	debugBarHeight := 0
	if h.debugMode {
		debugBarHeight = 1
	}
	// Height breakdown: -1 header, -filterBarHeight filter, -updateBannerHeight banner, -maintenanceBannerHeight maintenance, -helpBarHeight help, -debugBarHeight debug
	contentHeight := h.height - 1 - helpBarHeight - updateBannerHeight - maintenanceBannerHeight - filterBarHeight - debugBarHeight - h.promptInputDialog.ReservedHeight()

	var mainContent string
	if h.embeddedMode && h.embeddedSidebarHidden {
		mainContent = h.renderEmbeddedOnlyLayout(contentHeight)
	} else {
		// Route to appropriate layout based on terminal width.
		switch h.getLayoutMode() {
		case LayoutModeSingle:
			mainContent = h.renderSingleColumnLayout(contentHeight)
		case LayoutModeStacked:
			mainContent = h.renderStackedLayout(contentHeight)
		default: // LayoutModeDual
			mainContent = h.renderDualColumnLayout(contentHeight)
		}
	}

	// Ensure mainContent has exact height
	mainContent = ensureExactHeight(mainContent, contentHeight)
	b.WriteString(mainContent)
	b.WriteString("\n")
	if h.promptInputDialog.IsVisible() {
		b.WriteString(h.promptInputDialog.Bar())
		b.WriteString("\n")
	}

	// ═══════════════════════════════════════════════════════════════════
	// HELP BAR (context-aware shortcuts) — replaced by the insert-mode
	// indicator when the user is typing through to a focused session (#1069).
	// ═══════════════════════════════════════════════════════════════════
	var helpBar string
	if h.insertMode {
		helpBar = h.renderInsertModeBar()
	} else {
		helpBar = h.renderHelpBar()
	}
	if warning := h.renderHealthWarning(); warning != "" {
		helpBar = warning
	}
	// A live session-reference pick (ASK-111) owns the bar: its hint line is
	// the only place that says what Enter will do.
	if !h.insertMode && h.sessionRefPicking() {
		helpBar = h.renderSessionRefPickBar()
	}
	// The session-reference toast (ASK-111) takes the help bar's key row: the
	// frame is exactly the viewport, so a line drawn below the help bar is
	// clipped.
	refToast := h.sessionRefToastActive()
	if refToast {
		helpBar = h.renderSessionRefToastOverHelpBar(helpBar)
	}
	b.WriteString(helpBar)

	// Debug performance overlay (AGENTDECK_DEBUG=1 only)
	if h.debugMode {
		b.WriteString("\n")
		b.WriteString(h.renderDebugBar())
	}

	// Error and warning messages are displayed but may be truncated by final height constraint
	if h.err != nil && !refToast {
		remaining := 5*time.Second - time.Since(h.errTime)
		if remaining < 0 {
			remaining = 0
		}
		dismissHint := lipgloss.NewStyle().Foreground(ColorText).Render(
			fmt.Sprintf(" (auto-dismiss in %ds)", int(remaining.Seconds())+1))
		errMsg := ErrorStyle.Render("⚠ "+h.err.Error()) + dismissHint
		b.WriteString("\n")
		b.WriteString(errMsg)
	}

	if h.storageWarning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString("\n")
		b.WriteString(warnStyle.Render(h.storageWarning))
	}

	if h.watcherWarning != "" {
		warnStyle := lipgloss.NewStyle().Foreground(ColorYellow)
		b.WriteString("\n")
		b.WriteString(warnStyle.Render("⚠ " + h.watcherWarning))
	}

	// Performance: log render duration when debug mode is active
	if logging.IsDebugEnabled() {
		elapsed := time.Since(renderStart)
		if elapsed > 50*time.Millisecond {
			perfLog.Warn("slow_view_render", slog.Duration("elapsed", elapsed),
				slog.Int("width", h.width), slog.Int("height", h.height),
				slog.Int("sessions", len(h.flatItems)))
		} else {
			perfLog.Debug("view_render", slog.Duration("elapsed", elapsed))
		}
		h.lastRenderDuration.Store(elapsed.Microseconds())
	}

	// CRITICAL: Use ensureExactHeight for robust, consistent output across all platforms
	// This is the single source of truth for output height - guarantees exactly h.height lines
	// regardless of component content, ANSI codes, or terminal differences
	content := b.String()
	if h.sessionSwitcher.IsVisible() {
		// Overlays compose on rows already fitted to the viewport, and the
		// composite then goes through the same final clamp as every frame
		// (#2334), so their rows get the same width safety net and exactly
		// one auto-wrap off/on bracket each.
		// The final clamp fits these rows a second time; fitting is
		// idempotent, so only the overlay rows change.
		content = fitViewportRows(content, h.width, h.height)
		// Keep the session list and preview visible while Ctrl+S is open.
		// The card is anchored to the left edge of the active-session area,
		// immediately beside the sidebar in a dual layout, so the current
		// sidebar selection remains visible and spatially close to the
		// choices.
		if h.sessionSwitcher.IsVisible() {
			content = h.renderSessionSwitcherOverlay(content)
		}
	}
	return clampViewToViewport(content, h.width, h.height)
}

// renderPanelTitle creates a styled section title with underline
func (h *Home) renderPanelTitle(title string, width int) string {
	// Truncate title if it exceeds width
	title = truncateDisplay(title, width, "...")

	titleStyle := lipgloss.NewStyle().
		Foreground(ColorCyan).
		Bold(true).
		Width(width)

	underlineStyle := lipgloss.NewStyle().
		Foreground(ColorBorder).
		Width(width)

	// Create underline that extends to panel width
	underlineLen := max(0, width)
	underline := underlineStyle.Render(strings.Repeat("─", underlineLen))

	return titleStyle.Render(title) + "\n" + underline
}

// renderLoadingSplash creates a simple centered loading splash screen
// Shows the three status indicators (running/waiting/idle) cycling
func renderLoadingSplash(width, height int, frame int) string {
	// Status indicator cycle: each status lights up in sequence
	// Frame 0-1: Running (green ●)
	// Frame 2-3: Waiting (yellow ◐)
	// Frame 4-5: Idle (gray ○)
	// Frame 6-7: All lit together

	phase := (frame / 2) % 4

	// Active status colors (match the actual TUI colors)
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	yellowStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	grayStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	// Dim style for inactive indicators
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	// Text styles
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorTextDim)

	var content strings.Builder

	if width >= 40 && height >= 10 {
		// Full version - big status indicators in a row
		var running, waiting, idle string

		switch phase {
		case 0: // Running highlighted
			running = greenStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 1: // Waiting highlighted
			running = dimStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 2: // Idle highlighted
			running = dimStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = grayStyle.Render("○")
		case 3: // All lit
			running = greenStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = grayStyle.Render("○")
		}

		content.WriteString("\n")
		content.WriteString("      " + running + "   " + waiting + "   " + idle + "      \n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString("\n")
		content.WriteString(subtitleStyle.Render("Loading sessions..."))
	} else if width >= 25 && height >= 6 {
		// Compact version
		var indicators string
		switch phase {
		case 0:
			indicators = greenStyle.Render("●") + " " + dimStyle.Render("◐") + " " + dimStyle.Render("○")
		case 1:
			indicators = dimStyle.Render("●") + " " + yellowStyle.Render("◐") + " " + dimStyle.Render("○")
		case 2:
			indicators = dimStyle.Render("●") + " " + dimStyle.Render("◐") + " " + grayStyle.Render("○")
		case 3:
			indicators = greenStyle.Render("●") + " " + yellowStyle.Render("◐") + " " + grayStyle.Render("○")
		}
		content.WriteString(indicators + "\n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render("Loading..."))
	} else {
		// Minimal
		content.WriteString(greenStyle.Render("●") + " " + titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render("Loading..."))
	}

	// Center the content
	contentStyle := lipgloss.NewStyle().
		Align(lipgloss.Center).
		Width(width)

	rendered := lipgloss.Place(
		width, height,
		lipgloss.Center, lipgloss.Center,
		contentStyle.Render(content.String()),
	)

	return rendered
}

// renderQuittingSplash renders a splash screen during application shutdown
func renderQuittingSplash(width, height int, frame int) string {
	return renderShutdownSplash(width, height, frame, "Shutting down...")
}

// renderShutdownSplash draws the quit/restart splash with the given subtitle.
func renderShutdownSplash(width, height int, frame int, subtitle string) string {
	// Status indicator cycle (matches loading splash for consistency)
	phase := (frame / 2) % 4

	// Active status colors
	greenStyle := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true)
	yellowStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
	grayStyle := lipgloss.NewStyle().Foreground(ColorTextDim)
	dimStyle := lipgloss.NewStyle().Foreground(ColorComment)

	// Text styles
	titleStyle := lipgloss.NewStyle().Foreground(ColorText).Bold(true)
	subtitleStyle := lipgloss.NewStyle().Foreground(ColorYellow)

	var content strings.Builder

	if width >= 40 && height >= 10 {
		var running, waiting, idle string
		switch phase {
		case 0:
			running = greenStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 1:
			running = dimStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = dimStyle.Render("○")
		case 2:
			running = dimStyle.Render("●")
			waiting = dimStyle.Render("◐")
			idle = grayStyle.Render("○")
		case 3:
			running = greenStyle.Render("●")
			waiting = yellowStyle.Render("◐")
			idle = grayStyle.Render("○")
		}

		content.WriteString("\n")
		content.WriteString("      " + running + "   " + waiting + "   " + idle + "      \n")
		content.WriteString("\n")
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString("\n")
		content.WriteString(subtitleStyle.Render(subtitle))
	} else {
		// Compact/Minimal
		content.WriteString(titleStyle.Render("Agent Deck") + "\n")
		content.WriteString(subtitleStyle.Render(subtitle))
	}

	contentStyle := lipgloss.NewStyle().
		Align(lipgloss.Center).
		Width(width)

	return lipgloss.Place(
		width, height,
		lipgloss.Center, lipgloss.Center,
		contentStyle.Render(content.String()),
	)
}

// EmptyStateConfig holds content for responsive empty state rendering
type EmptyStateConfig struct {
	Icon                string
	Title               string
	Subtitle            string
	ShowSubtitleMinimal bool // Keep the selected filter visible in a narrow list card.
	ShowAllHintsMinimal bool // Combined filters need both recovery keys in the list card.
	// Body is extra plain (non-bulleted) lines shown between Subtitle and
	// Hints, one per element, in "full" and "compact" tiers only (dropped in
	// "minimal", same as Subtitle). Unset by every caller except the remote
	// group preview panel, which uses it for the version/stats block.
	Body  []string
	Hints []string // Full list of hints (will be reduced based on space)
}

// renderEmptyStateResponsive creates a centered empty state that adapts to available space
// Uses progressive disclosure: full → compact → minimal based on width/height
// emptyStateTier picks the content tier for the available space (the more
// restrictive of width and height) and the padding that tier renders with.
func emptyStateTier(width, height int) (tier string, vPad, hPad int) {
	switch {
	case width < emptyStateWidthCompact || height < emptyStateHeightCompact:
		return "minimal", 0, spacingTight
	case width < emptyStateWidthFull || height < emptyStateHeightFull:
		return "compact", spacingTight, spacingNormal
	}
	return "full", spacingNormal, spacingLarge
}

// emptyStateHintCount is how many of hints the tier shows.
func emptyStateHintCount(tier string, hints int) int {
	switch tier {
	case "compact":
		return min(hints, 2)
	case "minimal":
		return min(hints, 1)
	}
	return hints
}

// emptyStateBodyLayout is the room renderEmptyStateResponsive leaves for
// config.Body: the columns one body line may use inside the padding, and the
// rows left once the icon, title, subtitle, hints and padding have taken
// theirs. Callers that build Body from variable-length data (the remote
// preview's stats block) fit it into this so no field can push another off
// the pane. Rows is 0 when the tier drops the body altogether.
func emptyStateBodyLayout(config EmptyStateConfig, width, height int) previewLayout {
	tier, vPad, hPad := emptyStateTier(width, height)
	if tier == "minimal" {
		return previewLayout{width: max(1, width-hPad*2)}
	}
	fixed := 2 // icon, title
	if tier == "full" {
		fixed++ // blank after the icon
	}
	if config.Subtitle != "" {
		fixed++
	}
	fixed++ // blank before the body
	if n := emptyStateHintCount(tier, len(config.Hints)); n > 0 {
		fixed += n + 1 // blank before the hints
	}
	return previewLayout{width: max(1, width-hPad*2), rows: max(0, height-vPad*2-fixed)}
}

func renderEmptyStateResponsive(config EmptyStateConfig, width, height int) string {
	tier, vPad, hPad := emptyStateTier(width, height)

	// Styles
	iconStyle := lipgloss.NewStyle().
		Foreground(ColorAccent).
		Bold(true)
	titleStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Bold(true)
	subtitleStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Italic(true)
	hintStyle := lipgloss.NewStyle().
		Foreground(ColorComment)

	var content strings.Builder

	// Icon - always shown but with adaptive spacing
	content.WriteString(iconStyle.Render(config.Icon))
	if tier == "full" {
		content.WriteString("\n\n")
	} else {
		content.WriteString("\n")
	}

	// Title - always shown
	content.WriteString(titleStyle.Render(config.Title))

	// Subtitle - shown in full and compact modes
	// (padding and margins take hPad*2+4 columns; a pane too narrow for a
	// shortened subtitle leaves it out).
	if config.Subtitle != "" && (tier != "minimal" || config.ShowSubtitleMinimal) {
		if subtitle, ok := fitEmptyStateText(config.Subtitle, width-hPad*2-4); ok {
			content.WriteString("\n")
			content.WriteString(subtitleStyle.Render(subtitle))
		}
	}

	// Body - extra plain lines, same tiers as Subtitle. Rendered as one
	// left-aligned block (every line padded to the widest) that the centred
	// layout below places as a whole: labels and table columns line up, and
	// a line longer than the padded width wraps here rather than widening
	// the block. Without this, Align(Center) centred each line against the
	// longest one and MaxWidth then cut every line from the right, so one
	// over-wide field shifted the entire block off the pane (rc.6 accounts
	// defect).
	if len(config.Body) > 0 && tier != "minimal" {
		var body []string
		for _, line := range config.Body {
			body = append(body, wrapPreviewLine(line, width-hPad*2)...)
		}
		blockWidth := 0
		for _, line := range body {
			blockWidth = max(blockWidth, lipgloss.Width(line))
		}
		content.WriteString("\n")
		for _, line := range body {
			content.WriteString("\n")
			content.WriteString(subtitleStyle.Render(line + strings.Repeat(" ", blockWidth-lipgloss.Width(line))))
		}
	}

	// Hints - progressive disclosure based on tier
	if len(config.Hints) > 0 {
		hintCount := emptyStateHintCount(tier, len(config.Hints))
		if tier == "minimal" && config.ShowAllHintsMinimal {
			hintCount = len(config.Hints)
		}
		// Each hint gets the width minus padding, margins and the "• "
		// prefix (hPad*2+6); one that cannot be shortened usefully is left out.
		var hintsToShow []string
		for _, hint := range config.Hints[:hintCount] {
			if displayHint, ok := fitEmptyStateText(hint, width-hPad*2-6); ok {
				hintsToShow = append(hintsToShow, displayHint)
			}
		}

		if len(hintsToShow) > 0 {
			if tier == "full" {
				content.WriteString("\n\n")
			} else {
				content.WriteString("\n")
			}
		}
		for i, hint := range hintsToShow {
			content.WriteString(hintStyle.Render("• " + hint))
			if i < len(hintsToShow)-1 {
				content.WriteString("\n")
			}
		}
	}

	contentStyle := lipgloss.NewStyle().
		Foreground(ColorText).
		Align(lipgloss.Center).
		Padding(vPad, hPad).
		MaxWidth(width)

	rendered := contentStyle.Render(content.String())

	// Ensure exact height
	return ensureExactHeight(rendered, height)
}

// filteredEmptyState describes an empty view without treating it as a new workspace.
func (h *Home) filteredEmptyState() (EmptyStateConfig, bool) {
	if h.statusFilter == FilterModeArchived {
		hints := []string{FilterKeyArchived + " back to active"}
		if key := h.actionKey(hotkeyArchiveSession); key != "" {
			hints = append(hints, key+" archives a session")
		}
		return EmptyStateConfig{
			Icon:     "◇",
			Title:    "No archived sessions",
			Subtitle: "Archive a session to see it here",
			Hints:    hints,
		}, true
	}
	if h.statusFilter == "" && h.timeFilter == session.TimeFilterAll {
		return EmptyStateConfig{}, false
	}
	parts := make([]string, 0, 2)
	if h.statusFilter != "" {
		parts = append(parts, h.statusFilterLabel())
	}
	if h.timeFilter != session.TimeFilterAll {
		parts = append(parts, h.timeFilter.Label())
	}
	hints := make([]string, 0, 2)
	if h.statusFilter != "" {
		hints = append(hints, "0 show all")
	}
	timeKey := h.actionKey(hotkeyCycleTimeFilter)
	if timeKey == "" {
		timeKey = "*"
	}
	if h.timeFilter != session.TimeFilterAll {
		hints = append(hints, timeKey+" time range")
	}
	return EmptyStateConfig{
		Icon:                "◇",
		Title:               "No sessions match",
		Subtitle:            strings.Join(parts, " · "),
		ShowSubtitleMinimal: true,
		ShowAllHintsMinimal: true,
		Hints:               hints,
	}, true
}

// ensureExactHeight is a critical helper that ensures any content has EXACTLY n lines.
// This is essential for consistent TUI layout across all platforms and terminal sizes.
//
// Behavior:
//   - If content has fewer lines than n: pads with blank lines at the end
//   - If content has more lines than n: truncates from the end (keeps header/start)
//   - Returns content with exactly n lines (n-1 internal newlines, no trailing newline)
//
// This function handles ANSI-styled content correctly by counting \n characters
// rather than visual lines, which works reliably across all terminal emulators.
func ensureExactHeight(content string, n int) string {
	if n <= 0 {
		return ""
	}

	// Split into lines
	lines := strings.Split(content, "\n")

	// Truncate or pad to exactly n lines
	if len(lines) > n {
		// Keep first n lines (preserves header info)
		lines = lines[:n]
	} else if len(lines) < n {
		// Pad with blank lines
		for len(lines) < n {
			lines = append(lines, "")
		}
	}

	// Join back - this creates n-1 newlines for n lines
	return strings.Join(lines, "\n")
}

// clampViewToViewport hard-clamps the rendered frame to the terminal viewport.
// This is the final safety net against any component returning an unexpected
// extra line or a line that still exceeds the viewport width.
func clampViewToViewport(content string, width, height int) string {
	return clampRows(content, width, height, true)
}

// fitViewportRows is clampViewToViewport without the per-row auto-wrap
// toggles: the base overlays are composed on before the final clamp, which
// must be the only place a row gets its ?7l/?7h bracket (#2334).
func fitViewportRows(content string, width, height int) string {
	return clampRows(content, width, height, false)
}

func clampRows(content string, width, height int, autoWrapOff bool) string {
	if width <= 0 || height <= 0 {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > height {
		lines = lines[:height]
	} else if len(lines) < height {
		for len(lines) < height {
			lines = append(lines, "")
		}
	}

	const sgrReset = "\x1b[0m"
	var rendered strings.Builder
	rendered.Grow(len(content) + len(lines)*(2*len(sgrReset)+len(ansi.ResetModeAutoWrap)+len(ansi.SetModeAutoWrap)))

	for i, line := range lines {
		// #937 v2: cellWidth/cellTruncate (not ansi.*) so this final
		// viewport-clamp safety net sees keycap clusters at their true
		// terminal cell count. Any line that slips past upstream gates
		// with a #️⃣ 0️⃣–9️⃣ *️⃣ glyph would otherwise overflow into the
		// next row here — exactly @jennings's pane-content drift report.
		//
		// Also PAD short lines to exactly width (not just truncate long
		// ones): on incremental redraw a shorter line must overwrite the
		// full previous row, else the terminal keeps the stale trailing
		// glyphs — the iTerm2 "ghost line" artifact on session-list scroll
		// (#607 row-offset drift). fitCellWidth does both, on cellWidth so
		// this post-join clamp stays a true terminal-cell net.
		//
		// #699 follow-up: isolate SGR state at BOTH row boundaries. Bubble
		// Tea's incremental renderer skips unchanged rows and repaints only
		// changed ones. A captured preview background can therefore still be
		// active when a later row starts rendering, even though the original
		// fix appended a reset to every preview line. Prefixing and suffixing
		// the final physical rows makes repaint order irrelevant. SGR resets
		// occupy zero terminal cells, so the exact viewport dimensions are
		// unchanged.
		if i > 0 {
			rendered.WriteByte('\n')
		}
		//
		// expandTabs before fitting: a TAB measures zero cells through
		// cellWidth but advances the terminal's cursor to the next
		// multiple-of-8 column, so an un-expanded tab makes this clamp pad a
		// row to width that the terminal then renders wider, wrapping it and
		// scrolling the header off the alternate screen. Every row starts at
		// column 0 here, so per-row expansion lands on the same stops the
		// terminal would use.
		//
		// #2334: fitTerminalRow instead of fitCellWidth so a complex-script
		// row (Devanagari vowel signs, ZWJ emoji) fits under the per code
		// point width convention too, and every row runs with auto-wrap off
		// (DECAWM) and switches it back on at its own end. A row that is still
		// wider on some terminal is clipped at the right margin instead of
		// wrapping onto the next screen row, which would shift every later row
		// against Bubble Tea's line-diff model and leave the duplicated,
		// interleaved preview blocks of the report on screen. Restoring per
		// row keeps auto-wrap on between writes, so no exit, suspend, attach
		// or crash path can leave it off in the user's shell.
		rendered.WriteString(sgrReset)
		if autoWrapOff {
			rendered.WriteString(ansi.ResetModeAutoWrap)
		}
		rendered.WriteString(fitTerminalRow(expandTabs(line), width))
		if autoWrapOff {
			rendered.WriteString(ansi.SetModeAutoWrap)
		}
		rendered.WriteString(sgrReset)
	}

	return rendered.String()
}

// ensureExactWidth ensures each line in content has exactly the specified visual width.
// This is essential for proper horizontal panel alignment in lipgloss.JoinHorizontal.
//
// CRITICAL: Uses lipgloss.Width() for measurement to stay consistent with
// lipgloss.JoinHorizontal's internal width calculation. Using a different
// measurement (e.g. runewidth.StringWidth after custom ANSI stripping) can
// disagree by even 1 character, causing JoinHorizontal to pad all lines to
// the wider measurement. This makes the joined output exceed terminal width,
// lines wrap, and Bubble Tea's renderer loses cursor tracking — producing
// duplicated/stacked content (the "scrolling artifact" bug).
//
// Behavior:
//   - Measures width using lipgloss.Width (same as JoinHorizontal)
//   - Pads short lines with spaces to reach target width
//   - Truncates long lines using lipgloss.MaxWidth (preserves ANSI where possible)
//   - Guarantees every line is exactly `width` visual characters
func ensureExactWidth(content string, width int) string {
	if width <= 0 {
		return content
	}

	lines := strings.Split(content, "\n")
	result := make([]string, len(lines))

	for i, line := range lines {
		// Measure visual width using lipgloss (same measurement as JoinHorizontal)
		displayWidth := lipgloss.Width(line)

		if displayWidth == width {
			result[i] = line
		} else if displayWidth < width {
			// Pad with spaces to reach target width
			result[i] = line + strings.Repeat(" ", width-displayWidth)
		} else {
			// Line too wide — truncate using lipgloss for consistent ANSI handling
			truncated := lipgloss.NewStyle().MaxWidth(width).Render(line)
			// Verify and pad if truncation left it short
			truncWidth := lipgloss.Width(truncated)
			if truncWidth < width {
				truncated += strings.Repeat(" ", width-truncWidth)
			}
			result[i] = truncated
		}
	}

	return strings.Join(result, "\n")
}

// renderDualColumnLayout renders side-by-side panels for wide terminals (80+ cols)
func (h *Home) renderDualColumnLayout(contentHeight int) string {
	var b strings.Builder

	// Calculate panel widths from configurable split (issue #1092 — [ui] preview_pct)
	// with chrome / min-width clamping (issue #1113) so the PREVIEW pane never
	// shrinks below its title width.
	leftWidth, rightWidth := h.splitPaneWidths()

	// Panel title is exactly 2 lines (title + underline)
	// Panel content gets the remaining space: contentHeight - 2
	panelTitleLines := 2
	panelContentHeight := contentHeight - panelTitleLines

	// Build left panel (session list) with styled title.
	// Issue #1092: when the user just adjusted the split, briefly append
	// the new ratio to both titles so the change is visible.
	sessionsTitle := h.sidebarTitle()
	previewTitle := "PREVIEW"
	if !h.previewPctOverlayAt.IsZero() && time.Now().Before(h.previewPctOverlayAt) {
		pct := h.getPreviewPct()
		sessionsTitle = fmt.Sprintf("SESSIONS %d%%", 100-pct)
		previewTitle = fmt.Sprintf("PREVIEW %d%%", pct)
	}
	leftTitle := h.renderPanelTitle(sessionsTitle, leftWidth)
	leftContent := h.renderSessionList(leftWidth, panelContentHeight)
	// CRITICAL: Ensure left content has exactly panelContentHeight lines
	leftContent = ensureExactHeight(leftContent, panelContentHeight)
	leftPanel := leftTitle + "\n" + leftContent

	var rightPanel string
	if h.embeddedMode {
		innerWidth := max(1, rightWidth-2)
		innerHeight := max(1, contentHeight-2)
		rightContent := h.renderPreviewPane(innerWidth, innerHeight)
		rightContent = ensureExactHeight(rightContent, innerHeight)
		rightContent = ensureExactWidth(rightContent, innerWidth)
		rightPanel = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(ColorAccent).
			Width(innerWidth).
			Height(innerHeight).
			Render(rightContent)
	} else {
		rightTitle := h.renderPanelTitle(previewTitle, rightWidth)
		rightContent := h.renderPreviewPane(rightWidth, panelContentHeight)
		rightContent = ensureExactHeight(rightContent, panelContentHeight)
		rightPanel = rightTitle + "\n" + rightContent
	}

	// Build separator - must be exactly contentHeight lines. Brighten it while
	// the user is dragging it so the resize handle reads as active.
	separatorColor := ColorBorder
	if h.draggingDivider || h.embeddedMode {
		separatorColor = ColorAccent
	}
	// A hovered divider brightens and thickens before you press, so the handle
	// tells you it is a handle. This is the affordance every terminal gets;
	// the OSC 22 grab cursor is a bonus where the terminal supports it.
	separatorGlyph := " │ "
	if h.hoverDivider || h.draggingDivider {
		separatorGlyph = " ┃ "
		separatorColor = ColorAccent
	}
	// Every row is the same constant string, so style it once and reuse it
	// instead of paying a lipgloss render per screen row per frame.
	separatorCell := lipgloss.NewStyle().Foreground(separatorColor).Render(separatorGlyph)
	separatorLines := make([]string, contentHeight)
	for i := range separatorLines {
		separatorLines[i] = separatorCell
	}
	separator := strings.Join(separatorLines, "\n")

	// CRITICAL: Ensure both panels have exactly contentHeight lines before joining
	leftPanel = ensureExactHeight(leftPanel, contentHeight)
	rightPanel = ensureExactHeight(rightPanel, contentHeight)

	// CRITICAL: Ensure both panels have exactly the correct width for proper alignment
	// Without this, variable-width lines cause JoinHorizontal to misalign content
	leftPanel = ensureExactWidth(leftPanel, leftWidth)
	rightPanel = ensureExactWidth(rightPanel, rightWidth)

	// Join panels horizontally - all components have exact heights AND widths now
	mainContent := lipgloss.JoinHorizontal(lipgloss.Top, leftPanel, separator, rightPanel)

	// Safety net: enforce per-line MaxWidth on the joined output.
	// Even with ensureExactWidth, JoinHorizontal can produce lines wider than
	// h.width due to separator ANSI codes or rounding. Any line that wraps in the
	// terminal adds a visual line, which shifts Bubble Tea's cursor tracking and
	// causes duplicated/stacked content on scroll.
	//
	// Issue #1753: that pass re-truncates and rebuilds every line of the whole
	// composed frame, and it ran on every View() — every keystroke and every tick.
	// Profiling a switching workload put it at 38% of this function and ~14% of
	// total process CPU, while on a normal terminal it changes nothing. Measuring
	// is far cheaper than rebuilding, so measure first and only pay when there is
	// something to do. lipgloss.Width returns the widest line of a multi-line
	// string using the same ansi.StringWidth basis MaxWidth truncates by, so the
	// two agree by construction on whether the frame overflows.
	//
	// This has to be a MEASUREMENT, not arithmetic: leftWidth +
	// paneSeparatorWidth + rightWidth == h.width does NOT imply the joined frame
	// fits. ensureExactWidth pads a too-short line but never re-truncates a
	// too-wide one, and MaxWidth truncation of an emoji/keycap grapheme cluster
	// can land one cell OVER the requested width. JoinHorizontal then pads every
	// row out to that inflated block width and the frame overflows by a column —
	// see TestIssue1753_NarrowPaneKeycapStillRunsSafetyNet, which is exactly the
	// case an arithmetic guard got wrong.
	if lipgloss.Width(mainContent) > h.width {
		mainContent = lipgloss.NewStyle().MaxWidth(h.width).Render(mainContent)
	}

	b.WriteString(mainContent)

	return b.String()
}

// renderStackedLayout renders list above preview for medium terminals (50-79 cols)
func (h *Home) renderStackedLayout(totalHeight int) string {
	var b strings.Builder

	// Split height by previewPct so < / > adjust the vertical split the
	// same way they adjust the horizontal split in the dual layout.
	// stackedListHeight applies the >=5 list / >=3 preview floors.
	listHeight := h.stackedListHeight(totalHeight)
	previewHeight := totalHeight - listHeight - 1 // -1 for separator
	if previewHeight < 3 {
		previewHeight = 3
	}

	// Session list (full width)
	listTitle := h.renderPanelTitle(h.sidebarTitle(), h.width)
	listContent := h.renderSessionList(h.width, listHeight-2) // -2 for title
	listContent = ensureExactHeight(listContent, listHeight-2)
	b.WriteString(listTitle)
	b.WriteString("\n")
	b.WriteString(listContent)
	b.WriteString("\n")

	// Separator
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	b.WriteString(sepStyle.Render(strings.Repeat("─", max(0, h.width))))
	b.WriteString("\n")

	// Preview (full width)
	previewTitle := h.renderPanelTitle("PREVIEW", h.width)
	previewContent := h.renderPreviewPane(h.width, previewHeight-2) // -2 for title
	previewContent = ensureExactHeight(previewContent, previewHeight-2)
	b.WriteString(previewTitle)
	b.WriteString("\n")
	b.WriteString(previewContent)

	return b.String()
}

// renderSingleColumnLayout renders list only for narrow terminals (<50 cols)
func (h *Home) renderSingleColumnLayout(totalHeight int) string {
	var b strings.Builder

	// Full height for list
	listHeight := totalHeight - 2 // -2 for title

	listTitle := h.renderPanelTitle(h.sidebarTitle(), h.width)
	listContent := h.renderSessionList(h.width, listHeight)
	listContent = ensureExactHeight(listContent, listHeight)

	b.WriteString(listTitle)
	b.WriteString("\n")
	b.WriteString(listContent)

	return b.String()
}

// renderEmbeddedOnlyLayout gives the attached PTY the entire dashboard
// content area while keeping the header and session control bar available.
func (h *Home) renderEmbeddedOnlyLayout(contentHeight int) string {
	innerWidth := max(1, h.width-2)
	innerHeight := max(1, contentHeight-2)
	content := h.renderPreviewPane(innerWidth, innerHeight)
	content = ensureExactWidth(ensureExactHeight(content, innerHeight), innerWidth)
	panel := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(ColorAccent).
		Width(innerWidth).
		Height(innerHeight).
		Render(content)
	return ensureExactWidth(ensureExactHeight(panel, contentHeight), h.width)
}

// renderHelpBar renders context-aware keyboard shortcuts. The style is
// selected by config.toml [ui] footer (cached in h.footerMode):
//
//   - "full" (default): the historic verbose bar with filled key chips, adapting
//     to terminal width across the tiny/minimal/compact/full tiers. This is also
//     the fallback for any unset or unrecognized footerMode, so the historic look
//     is never silently replaced.
//   - "curated" (opt-in): a lighter, dim inline bar that advertises only the
//     actions relevant to the selected row, with the settings and help keys
//     always last. Rarer/global actions live under help (?).
//   - "compact" / "minimal": force a single verbose tier regardless of width.
//
// The very-narrow tiny tier is always used below layoutBreakpointSingle so the
// bar never overflows, whatever the configured style.
func (h *Home) renderHelpBar() string {
	if h.width < layoutBreakpointSingle {
		return h.renderHelpBarTiny()
	}

	switch h.footerMode {
	case session.FooterCurated:
		return h.renderHelpBarCurated()
	case session.FooterCompact:
		return h.renderHelpBarCompact()
	case session.FooterMinimal:
		return h.renderHelpBarMinimal()
	default:
		// FooterFull, plus any unset ("") or unrecognized mode, routes to the
		// historic width-adaptive bar. Only an explicit curated setting selects
		// the curated bar, so the pre-PR default behavior is preserved.
		return h.renderHelpBarWidthAdaptive()
	}
}

// renderHelpBarWidthAdaptive routes to the historic width-based tiers used by
// the "full" footer style.
func (h *Home) renderHelpBarWidthAdaptive() string {
	switch {
	case h.width < 70:
		return h.renderHelpBarMinimal()
	case h.width <= 100:
		return h.renderHelpBarCompact()
	default:
		return h.renderHelpBarFull()
	}
}

// renderHelpBarTiny renders minimal help for very narrow terminals (<50 cols)
func (h *Home) renderHelpBarTiny() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	hintStyle := lipgloss.NewStyle().Foreground(ColorComment)
	helpKey := h.actionKey(hotkeyHelp)
	var hint string
	if h.jumpMode {
		hint = lipgloss.NewStyle().Foreground(ColorYellow).Bold(true).Render("Jump: a-z/esc")
	} else {
		hintText := "Help key unbound"
		if helpKey != "" {
			hintText = helpKey + " for help"
		}
		hint = hintStyle.Render(hintText)
	}

	// Center the hint
	padding := (h.width - lipgloss.Width(hint)) / 2
	if padding < 0 {
		padding = 0
	}
	content := strings.Repeat(" ", padding) + hint

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderHelpBarMinimal renders keys-only help for narrow terminals (50-69 cols)
func (h *Home) renderHelpBarMinimal() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true)
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	renderKeys := func(keys ...string) string {
		parts := make([]string, 0, len(keys))
		for _, key := range keys {
			if strings.TrimSpace(key) == "" {
				continue
			}
			parts = append(parts, keyStyle.Render(key))
		}
		return strings.Join(parts, " ")
	}

	// Context-specific keys (left side)
	var contextKeys string
	newKey := h.actionKey(hotkeyNewSession)
	quickKey := h.actionKey(hotkeyQuickCreate)
	importKey := h.actionKey(hotkeyImport)
	groupKey := h.actionKey(hotkeyCreateGroup)
	restartKey := h.actionKey(hotkeyRestart)
	restartFreshKey := h.actionKey(hotkeyRestartFresh)
	forkKey := h.actionKey(hotkeyQuickFork)
	mcpKey := h.actionKey(hotkeyMCPManager)
	skillsKey := h.actionKey(hotkeySkillsManager)
	notesKey := h.actionKey(hotkeyEditNotes)
	if cfg, _ := session.LoadUserConfig(); cfg != nil && !cfg.GetShowNotes() {
		notesKey = ""
	}
	if h.jumpMode {
		contextKeys = keyStyle.Render("a-z") + " " + keyStyle.Render("esc")
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			contextKeys = bufStyle.Render(h.jumpBuffer+"…") + " " + contextKeys
		}
	} else if len(h.flatItems) == 0 {
		contextKeys = renderKeys(newKey, quickKey, importKey, groupKey)
	} else if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextKeys = renderKeys("⏎", newKey, quickKey, groupKey)
		} else {
			contextKeys = renderKeys("⏎", newKey, quickKey, restartKey)
			if item.Session != nil && item.Session.CanRestartFresh() {
				freshRendered := renderKeys(restartFreshKey)
				if freshRendered != "" {
					contextKeys += " " + freshRendered
				}
			}
			if item.Session != nil && item.Session.CanFork() {
				forkRendered := renderKeys(forkKey)
				if forkRendered != "" {
					contextKeys += " " + forkRendered
				}
			}
			if item.Session != nil && session.ToolSupportsMCPManager(item.Session.Tool) {
				mcpRendered := renderKeys(mcpKey)
				if mcpRendered != "" {
					contextKeys += " " + mcpRendered
				}
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				skillsRendered := renderKeys(skillsKey)
				if skillsRendered != "" {
					contextKeys += " " + skillsRendered
				}
			}
			notesRendered := renderKeys(notesKey)
			if notesRendered != "" {
				contextKeys += " " + notesRendered
			}
		}
	}

	// Global keys (right side)
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalParts := []string{globalStyle.Render("↑↓")}
	if key := h.actionKey(hotkeySearch); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeySettings); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyHelp); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	if key := h.actionKey(hotkeyQuit); key != "" {
		globalParts = append(globalParts, globalStyle.Render(key))
	}
	globalKeys := strings.Join(globalParts, " ")
	if contextKeys == "" {
		contextKeys = globalStyle.Render("No actions bound")
	}

	// Calculate padding
	leftPart := contextKeys
	rightPart := globalKeys
	padding := h.width - lipgloss.Width(leftPart) - lipgloss.Width(rightPart) - 4
	if padding < 2 {
		// Content too wide for one line — drop right part to avoid overflow
		padding = 2
		rightPart = ""
	}

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart
	if rightPart == "" {
		content = leftPart
	}

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderHelpBarCompact renders abbreviated help for medium terminals (70-99 cols)
func (h *Home) renderHelpBarCompact() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	newQuickKey := joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate))
	restartFreshKey := h.actionKey(hotkeyRestartFresh)

	// Abbreviated key+short desc
	var contextHints []string
	if len(h.flatItems) == 0 {
		if newQuickKey != "" {
			contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			contextHints = append(contextHints, h.helpKeyShort(key, "Import"))
		}
	} else if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextHints = append(contextHints, h.helpKeyShort("⏎", "Toggle"))
			if newQuickKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
			}
		} else {
			contextHints = append(contextHints, h.helpKeyShort("⏎", "Attach"))
			if newQuickKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(newQuickKey, "New"))
			}
			if key := h.actionKey(hotkeyRestart); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Restart"))
			}
			if key := h.actionKey(hotkeyPromptSession); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Send"))
			}
			if item.Session != nil && item.Session.CanRestartFresh() && restartFreshKey != "" {
				contextHints = append(contextHints, h.helpKeyShort(restartFreshKey, "Fresh"))
			}
			if item.Session != nil && item.Session.CanFork() {
				if key := h.actionKey(hotkeyQuickFork); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "Fork"))
				}
			}
			if key := h.actionKey(hotkeyQueueMessage); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Queue"))
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				if key := h.actionKey(hotkeySkillsManager); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "Skills"))
				}
			}
			if item.Session != nil && session.ToolSupportsMCPManager(item.Session.Tool) {
				if key := h.actionKey(hotkeyMCPManager); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, "MCP"))
				}
				if key := h.actionKey(hotkeyTogglePreview); key != "" {
					contextHints = append(contextHints, h.helpKeyShort(key, h.previewModeShort()))
				}
			}
			if key := h.actionKey(hotkeyCopyOutput); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Copy"))
			}
			if key := h.actionKey(hotkeyCopyPane); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Copy pane"))
			}
			if key := h.actionKey(hotkeySendOutput); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Send"))
			}
			if key := h.actionKey(hotkeyEditNotes); key != "" {
				contextHints = append(contextHints, h.helpKeyShort(key, "Notes"))
			}
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		if key := h.actionKey(hotkeyUndoDelete); key != "" {
			contextHints = append(contextHints, h.helpKeyShort(key, "Undo"))
		}
	}

	// Jump mode overrides all context hints
	if h.jumpMode {
		contextHints = []string{
			h.helpKeyShort("a-z", "Hint"),
			h.helpKeyShort("esc", "Cancel"),
		}
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			contextHints = append([]string{bufStyle.Render(h.jumpBuffer + "…")}, contextHints...)
		}
	}

	// Global hints (abbreviated), most essential first: "↑↓ Nav" always
	// stays, the rest are dropped from the tail as they stop fitting. Each
	// entry keeps its key and label glued together as a single unit so the
	// width-fit loop below can only drop a whole entry, never strip a label
	// while leaving its bare key behind.
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	globalHints := []string{globalStyle.Render("↑↓ Nav")}
	for _, hint := range []struct {
		action string
		label  string
	}{
		{hotkeySearch, "Search"},
		{hotkeySettings, "Settings"},
		{hotkeyHelp, "Help"},
		{hotkeyQuit, "Quit"},
	} {
		if key := h.actionKey(hint.action); key != "" {
			globalHints = append(globalHints, globalStyle.Render(key+" "+hint.label))
		}
	}

	rightPart := strings.Join(globalHints, " ")
	for len(globalHints) > 1 && lipgloss.Width(rightPart)+6 > h.width {
		globalHints = globalHints[:len(globalHints)-1]
		rightPart = strings.Join(globalHints, " ")
	}
	// A long optional hint must not block a shorter later one from using
	// the gap between the context and global blocks.
	kept := make([]string, 0, len(contextHints))
	for _, hint := range contextHints {
		candidate := strings.Join(kept, " ")
		if candidate != "" {
			candidate += " "
		}
		candidate += hint
		if lipgloss.Width(candidate)+lipgloss.Width(rightPart)+6 <= h.width {
			kept = append(kept, hint)
		}
	}
	leftPart := strings.Join(kept, " ")
	padding := max(2, h.width-lipgloss.Width(leftPart)-lipgloss.Width(rightPart)-4)

	content := leftPart + sep + strings.Repeat(" ", padding) + rightPart

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// helpKeyShort formats a compact keyboard shortcut. The key chip and its
// label need an explicit space between them — the chip's own styling
// provides no separator, so without it they read as "n/NNew", "⏎Toggle".
func (h *Home) helpKeyShort(key, desc string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true)
	descStyle := lipgloss.NewStyle().Foreground(ColorText)
	return keyStyle.Render(key) + " " + descStyle.Render(desc)
}

// previewModeShort returns a short description of current preview mode for help bar
func (h *Home) previewModeShort() string {
	switch h.previewMode {
	case PreviewModeOutput:
		return "Out"
	case PreviewModeAnalytics:
		return "Stats"
	default:
		return "Both"
	}
}

// renderHelpBarFull renders context-aware keyboard shortcuts with visual grouping (100+ cols)
func (h *Home) renderHelpBarFull() string {
	// Separator style for grouping related actions
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	sep := sepStyle.Render(" │ ")
	newQuickKey := joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate))
	renameKey := h.actionKey(hotkeyRename)
	restartKey := h.actionKey(hotkeyRestart)
	restartFreshKey := h.actionKey(hotkeyRestartFresh)
	deleteKey := h.actionKey(hotkeyDelete)
	closeKey := h.actionKey(hotkeyCloseSession)
	groupKey := h.actionKey(hotkeyCreateGroup)
	moveKey := h.actionKey(hotkeyMoveToGroup)
	mcpKey := h.actionKey(hotkeyMCPManager)
	skillsKey := h.actionKey(hotkeySkillsManager)
	previewKey := h.actionKey(hotkeyTogglePreview)
	forkKeys := joinHotkeyLabels(h.actionKey(hotkeyQuickFork), h.actionKey(hotkeyForkWithOptions))
	copyKey := h.actionKey(hotkeyCopyOutput)
	copyPaneKey := h.actionKey(hotkeyCopyPane)
	sendKey := h.actionKey(hotkeySendOutput)
	execShellKey := h.actionKey(hotkeyExecShell)
	openShellHereKey := h.actionKey(hotkeyOpenShellHere)
	notesKey := h.actionKey(hotkeyEditNotes)
	if cfg, _ := session.LoadUserConfig(); cfg != nil && !cfg.GetShowNotes() {
		notesKey = ""
	}
	undoKey := h.actionKey(hotkeyUndoDelete)

	// Determine context-specific hints grouped by action type
	var primaryHints []string   // Main actions (attach, toggle, etc.)
	var secondaryHints []string // Edit actions (rename, move, delete)
	var contextTitle string

	if len(h.flatItems) == 0 {
		contextTitle = "Empty"
		if newQuickKey != "" {
			primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
		}
		if key := h.actionKey(hotkeyImport); key != "" {
			primaryHints = append(primaryHints, h.helpKey(key, "Import"))
		}
		if groupKey != "" {
			primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
		}
	} else if h.cursor >= 0 && h.cursor < len(h.flatItems) {
		item := h.flatItems[h.cursor]
		if item.Type == session.ItemTypeGroup {
			contextTitle = "Group"
			primaryHints = append(primaryHints, h.helpKey("Tab", "Toggle"))
			if newQuickKey != "" {
				primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
			}
			if groupKey != "" {
				primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
			}
			if renameKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(renameKey, "Rename"))
			}
			if deleteKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(deleteKey, "Delete"))
			}
		} else {
			contextTitle = "Session"
			primaryHints = append(primaryHints, h.helpKey("Enter", "Attach"))
			if newQuickKey != "" {
				primaryHints = append(primaryHints, h.helpKey(newQuickKey, "New/Quick"))
			}
			if groupKey != "" {
				primaryHints = append(primaryHints, h.helpKey(groupKey, "Group"))
			}
			if restartKey != "" {
				primaryHints = append(primaryHints, h.helpKey(restartKey, "Restart"))
			}
			if item.Session != nil && item.Session.CanRestartFresh() && restartFreshKey != "" {
				primaryHints = append(primaryHints, h.helpKey(restartFreshKey, "Restart Fresh"))
			}
			// Only show fork hints when the selected tool supports Agent Deck forking.
			if item.Session != nil && item.Session.CanFork() {
				if forkKeys != "" {
					primaryHints = append(primaryHints, h.helpKey(forkKeys, "Fork"))
				}
			}
			// Show MCP Manager and preview mode toggle for Claude and Gemini sessions
			if item.Session != nil && session.ToolSupportsMCPManager(item.Session.Tool) {
				if mcpKey != "" {
					primaryHints = append(primaryHints, h.helpKey(mcpKey, "MCP"))
				}
				if previewKey != "" {
					primaryHints = append(primaryHints, h.helpKey(previewKey, h.previewModeShort()))
				}
			}
			if item.Session != nil && session.SupportsProjectSkills(item.Session.Tool) {
				if skillsKey != "" {
					primaryHints = append(primaryHints, h.helpKey(skillsKey, "Skills"))
				}
			}
			if item.Session != nil && item.Session.IsSandboxed() {
				if execShellKey != "" {
					primaryHints = append(primaryHints, h.helpKey(execShellKey, "Exec"))
				}
			}
			if openShellHereKey != "" && item.Session != nil && item.Type == session.ItemTypeSession {
				primaryHints = append(primaryHints, h.helpKey(openShellHereKey, "Shell"))
			}
			if item.Type == session.ItemTypeSession && item.Session != nil {
				if key := h.actionKey(hotkeyPromptSession); key != "" {
					primaryHints = append(primaryHints, h.helpKey(key, "Send"))
				}
				if key := h.actionKey(hotkeyQueueMessage); key != "" {
					primaryHints = append(primaryHints, h.helpKey(key, "Queue"))
				}
			}
			if item.Session != nil && item.Session.IsMultiRepo() {
				if editPathsKey := h.actionKey(hotkeyEditPaths); editPathsKey != "" {
					primaryHints = append(primaryHints, h.helpKey(editPathsKey, "Paths"))
				}
			}
			if copyKey != "" {
				primaryHints = append(primaryHints, h.helpKey(copyKey, "Copy"))
			}
			if copyPaneKey != "" {
				primaryHints = append(primaryHints, h.helpKey(copyPaneKey, "Copy pane"))
			}
			if sendKey != "" {
				primaryHints = append(primaryHints, h.helpKey(sendKey, "Send"))
			}
			if notesKey != "" {
				primaryHints = append(primaryHints, h.helpKey(notesKey, "Notes"))
			}
			if renameKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(renameKey, "Rename"))
			}
			if moveKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(moveKey, "Move"))
			}
			if deleteKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(deleteKey, "Delete"))
			}
			if closeKey != "" {
				secondaryHints = append(secondaryHints, h.helpKey(closeKey, "Close"))
			}
		}
	}

	// Show undo hint when undo stack is non-empty
	if len(h.undoStack) > 0 {
		if undoKey != "" {
			secondaryHints = append(secondaryHints, h.helpKey(undoKey, "Undo"))
		}
	}

	// Jump mode overrides all context hints
	if h.jumpMode {
		contextTitle = "Jump"
		primaryHints = []string{
			h.helpKey("a-z", "Type hint"),
			h.helpKey("esc", "Cancel"),
		}
		if h.jumpBuffer != "" {
			bufStyle := lipgloss.NewStyle().Foreground(ColorYellow).Bold(true)
			primaryHints = append([]string{bufStyle.Render(h.jumpBuffer + "…")}, primaryHints...)
		}
		secondaryHints = nil
	}

	// Top border
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	// Context indicator with subtle styling
	ctxStyle := lipgloss.NewStyle().
		Foreground(ColorPurple).
		Bold(true)
	contextLabel := ctxStyle.Render(contextTitle + ":")

	// Reload indicator
	var reloadIndicator string
	h.reloadMu.Lock()
	reloading := h.isReloading
	h.reloadMu.Unlock()
	if reloading {
		reloadStyle := lipgloss.NewStyle().
			Foreground(ColorYellow).
			Bold(true)
		reloadIndicator = reloadStyle.Render("⟳ Reloading...")
	}

	// Global shortcuts (right side) - more compact with separators. "↑↓ Nav"
	// and Quit are pinned; the rest drop lowest-priority-first (from the end
	// of this slice) when space is tight, before either pinned key is
	// sacrificed.
	globalStyle := lipgloss.NewStyle().Foreground(ColorComment)
	navHint := globalStyle.Render("↑↓ Nav")
	var quitHint string
	if key := h.actionKey(hotkeyQuit); key != "" {
		quitHint = globalStyle.Render(key + " Quit")
	}
	var droppableGlobal []string
	droppableGlobal = append(droppableGlobal, globalStyle.Render("+/- Move"))
	if key := h.actionKey(hotkeySearch); key != "" {
		droppableGlobal = append(droppableGlobal, globalStyle.Render(key+" Search"))
	}
	droppableGlobal = append(droppableGlobal, globalStyle.Render("G Global"))
	if key := h.actionKey(hotkeySettings); key != "" {
		droppableGlobal = append(droppableGlobal, globalStyle.Render(key+" Settings"))
	}
	helpKey := h.actionKey(hotkeyHelp)
	helpIdx := -1
	if helpKey != "" {
		helpIdx = len(droppableGlobal)
		droppableGlobal = append(droppableGlobal, globalStyle.Render(helpKey+" Help"))
	}
	// "Report a problem" is the lowest-priority global hint: it shows on
	// roomy terminals and is the first to go when space is tight. The help
	// overlay always lists it.
	if key := h.actionKey(hotkeyReportProblem); key != "" {
		droppableGlobal = append(droppableGlobal, globalStyle.Render(key+" Report"))
	}

	leftPrefix := contextLabel
	if reloadIndicator != "" {
		leftPrefix = reloadIndicator + sep + leftPrefix
	}

	parts := fullFooterParts{
		leftPrefix: leftPrefix,
		primary:    primaryHints,
		secondary:  secondaryHints,
		sep:        sep,
		nav:        navHint,
		droppable:  droppableGlobal,
		quit:       quitHint,
	}
	helpContent := h.fitFullFooter(parts)
	// Keep the original order where Help already fits. On crowded 120-column
	// rows, shorter labels make room for Help without losing existing actions.
	if h.width >= 120 && h.width < 160 && helpKey != "" && !strings.Contains(helpContent, helpKey+" Help") {
		shorten := func(hints []string) {
			for i, hint := range hints {
				hint = strings.Replace(hint, "New/Quick", "New", 1)
				hint = strings.Replace(hint, "Restart Fresh", "Fresh", 1)
				hint = strings.Replace(hint, "Enter", "⏎", 1)
				hint = strings.Replace(hint, "Import", "Add", 1)
				hint = strings.Replace(hint, "Group", "Grp", 1)
				hints[i] = hint
			}
		}
		shorten(primaryHints)
		shorten(secondaryHints)
		// Pin Help next to Nav; the rest (Report included) stay droppable.
		parts.nav += sep + droppableGlobal[helpIdx]
		parts.droppable = append(append([]string{}, droppableGlobal[:helpIdx]...), droppableGlobal[helpIdx+1:]...)
		parts.primary = primaryHints
		parts.secondary = secondaryHints
		helpContent = h.fitFullFooter(parts)
	}

	raw := lipgloss.JoinVertical(lipgloss.Left, border, helpContent)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// fullFooterParts is the raw material of the full-tier footer line: the left
// (context) block's fixed prefix plus its two priority tiers of hints, and the
// right (global) block's pinned and droppable hints. sep is the separator used
// between hint groups on both sides.
type fullFooterParts struct {
	leftPrefix string   // context label, optionally prefixed with the reload indicator
	primary    []string // context hints, highest priority
	secondary  []string // context hints, dropped before primary ones
	sep        string
	nav        string   // pinned: never dropped
	droppable  []string // global hints, dropped from the end when space is tight
	quit       string   // pinned: never dropped
}

// fitFullFooter assembles the full-footer's single content line, width-aware
// rather than lipgloss's hard, ellipsis-less MaxWidth cut (finding #1,
// cosmetic TUI review: at 120 cols the left block hard-truncated at the
// terminal edge while the entire right block — including "↑↓ Nav" and Quit —
// vanished with no ellipsis).
//
// It tries, in order: (1) the full left block with progressively fewer
// droppable global hints, dropped lowest-priority-first (from the end of
// droppable, i.e. Help before Settings before ... when Help is not reserved); (2) once even the bare
// Nav+Quit global block doesn't fit alongside the full left block,
// progressively fewer context hints — also dropped lowest-priority-first,
// i.e. from the end of secondary then the end of primary — with a trailing
// ellipsis. nav and quit are never dropped; leftPrefix never is either.
func (h *Home) fitFullFooter(p fullFooterParts) string {
	// buildRight renders the global block keeping the n highest-priority
	// droppable hints, always framed by the pinned nav and quit hints.
	buildRight := func(n int) string {
		parts := []string{p.nav}
		parts = append(parts, p.droppable[:n]...)
		if p.quit != "" {
			parts = append(parts, p.quit)
		}
		return strings.Join(parts, p.sep)
	}

	totalHints := len(p.primary) + len(p.secondary)
	// buildLeft renders the context block keeping the k highest-priority
	// context hints (primary before secondary), with a trailing ellipsis
	// whenever that drops any.
	buildLeft := func(k int) string {
		nPrimary := min(k, len(p.primary))
		nSecondary := min(k-nPrimary, len(p.secondary))
		var groups []string
		if nPrimary > 0 {
			groups = append(groups, strings.Join(p.primary[:nPrimary], " "))
		}
		if nSecondary > 0 {
			groups = append(groups, strings.Join(p.secondary[:nSecondary], " "))
		}
		left := p.leftPrefix
		if len(groups) > 0 {
			left += " " + strings.Join(groups, p.sep)
		}
		if k < totalHints {
			left += " …"
		}
		return left
	}

	fits := func(left, right string) bool {
		if h.width <= 0 {
			return true // uninitialized width: no constraint, keep everything
		}
		total := lipgloss.Width(left) + spacingNormal
		if right != "" {
			total += lipgloss.Width(right)
		}
		return total <= h.width
	}
	assemble := func(left, right string) string {
		if right == "" {
			return left
		}
		padding := spacingNormal
		if h.width > 0 {
			if p := h.width - lipgloss.Width(left) - lipgloss.Width(right) - spacingNormal; p > padding {
				padding = p
			}
		}
		return left + strings.Repeat(" ", padding) + right
	}

	fullLeft := buildLeft(totalHints)

	// 1. Keep the full left block; shrink the global block from its
	// lowest-priority end.
	for n := len(p.droppable); n >= 0; n-- {
		right := buildRight(n)
		if fits(fullLeft, right) {
			return assemble(fullLeft, right)
		}
	}

	// 2. Even bare Nav+Quit doesn't fit alongside the full left block: trim
	// context hints from their lowest-priority end instead.
	minRight := buildRight(0)
	for k := totalHints; k >= 0; k-- {
		left := buildLeft(k)
		if fits(left, minRight) {
			return assemble(left, minRight)
		}
	}

	// 3. Extremely narrow fallback: contextLabel alone, no global block.
	return buildLeft(0)
}

// helpKey formats a keyboard shortcut for the help bar
func (h *Home) helpKey(key, desc string) string {
	keyStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(ColorAccent).
		Bold(true).
		Padding(0, 1)
	descStyle := lipgloss.NewStyle().Foreground(ColorText)
	return keyStyle.Render(key) + " " + descStyle.Render(desc)
}

// footerHint is one rendered key/label pair for the curated footer.
type footerHint struct {
	key   string
	label string
}

// curatedHint formats a single footer hint as dim, plain inline text — the key
// in a slightly emphasized dim, the label in the same dim tone. No filled chip:
// the curated bar should recede rather than compete for attention.
func (h *Home) curatedHint(hint footerHint) string {
	keyStyle := lipgloss.NewStyle().Foreground(ColorComment).Bold(true)
	labelStyle := lipgloss.NewStyle().Foreground(ColorComment)
	return keyStyle.Render(hint.key) + " " + labelStyle.Render(hint.label)
}

// fitCuratedHints returns the global hints (settings/help) preceded by as many
// of the context hints as fit within h.width, dropping the lowest-priority
// context hints (those at the end of the slice) first. The global hints are
// always kept, even if that means dropping every context hint, so the curated
// footer never loses the settings and help keys to right-edge truncation on a
// narrow terminal. Widths are measured with lipgloss.Width to match how the
// joined content is rendered, including the two-space separators.
func (h *Home) fitCuratedHints(contextHints, globalHints []footerHint) []footerHint {
	const sepWidth = 2 // the "  " separator between rendered hints

	width := func(hint footerHint) int {
		return lipgloss.Width(h.curatedHint(hint))
	}

	// Baseline: the global hints always render. Their combined width is the
	// floor we cannot trim below.
	used := 0
	for i, hint := range globalHints {
		used += width(hint)
		if i > 0 {
			used += sepWidth
		}
	}

	// Greedily keep context hints from the front (highest priority) while each
	// still fits. h.width <= 0 (uninitialized) means "no constraint" — keep all.
	rendered := used > 0 // whether anything has been counted yet (for separators)
	kept := make([]footerHint, 0, len(contextHints)+len(globalHints))
	for _, hint := range contextHints {
		next := used + width(hint)
		if rendered {
			next += sepWidth
		}
		if h.width > 0 && next > h.width {
			break
		}
		kept = append(kept, hint)
		used = next
		rendered = true
	}

	return append(kept, globalHints...)
}

// maxCuratedContextHints caps how many context-relevant shortcuts the curated
// footer advertises for the selected row. The settings and help keys are added
// on top of these by renderHelpBarCurated, so the bar stays short while still
// surfacing the two or three most useful actions for what is highlighted.
const maxCuratedContextHints = 3

// curatedContextHints returns up to maxCuratedContextHints context-relevant
// hints for the selected row, in priority order. It advertises the actions most
// likely to be wanted for the current selection; rarer and global actions stay
// under help (?). The settings and help keys are added separately by
// renderHelpBarCurated so they always remain last.
func (h *Home) curatedContextHints(item session.Item) []footerHint {
	var hints []footerHint
	add := func(key, label string) {
		if len(hints) >= maxCuratedContextHints {
			return
		}
		if strings.TrimSpace(key) != "" {
			hints = append(hints, footerHint{key: key, label: label})
		}
	}

	newQuick := joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate))

	switch item.Type {
	case session.ItemTypeGroup:
		// Group row: collapse/expand first (Tab; also l/h), then create/manage.
		if item.Group != nil && item.Group.Expanded {
			add("Tab", "collapse")
		} else {
			add("Tab", "expand")
		}
		add(newQuick, "new")
		add(h.actionKey(hotkeyRename), "rename")

	case session.ItemTypeSession:
		s := item.Session
		if s == nil {
			return hints
		}
		if sessionIsQueued(s) {
			// Queued: waiting for a group slot, no tmux yet — so it is neither
			// attachable (no pane to enter) nor restartable (nothing running to
			// restart). The only meaningful actions are dropping it from the
			// queue or creating something else. Without this branch the curated
			// footer advertised "⏎ attach"+restart for a session that has no
			// tmux (PR #1289 review nit 2b).
			add(h.actionKey(hotkeyDelete), "delete")
			add(newQuick, "new")
		} else if sessionIsDead(s) {
			// Dead (stopped or error): restart, then restart-fresh when the
			// tool tracks a session id, then delete — the actions for a session
			// that broke or was parked, in order of likely intent.
			add(h.actionKey(hotkeyRestart), "restart")
			if s.CanRestartFresh() {
				add(h.actionKey(hotkeyRestartFresh), "restart fresh")
			}
			add(h.actionKey(hotkeyDelete), "delete")
		} else {
			// Live/attachable session: attach first, then restart, then the
			// most relevant follow-up (fork while forkable, else new).
			add("⏎", "attach")
			add(h.actionKey(hotkeyRestart), "restart")
			if s.CanFork() {
				add(h.actionKey(hotkeyQuickFork), "fork")
			} else {
				add(newQuick, "new")
			}
		}

	case session.ItemTypeWindow:
		// A tmux window row attaches just like its session, and 'd' kills
		// the selected window (with confirmation).
		add("⏎", "attach")
		add(h.actionKey(hotkeyDelete), "delete")

	case session.ItemTypeRemoteSession:
		// A remote session row attaches over SSH just like a local session;
		// without this case the curated footer dropped the attach hint for
		// remote rows (PR #1289 review nit 1).
		add("⏎", "attach")
	}

	return hints
}

// sessionIsDead reports whether a session is stopped or errored — the states
// for which restart, rather than attach, is the relevant footer action. Reads
// the status via the thread-safe getter since the render goroutine runs
// concurrently with backgroundStatusUpdate (PR #1289 review nit 2).
func sessionIsDead(s *session.Instance) bool {
	status := s.GetStatusThreadSafe()
	return status == session.StatusStopped || status == session.StatusError
}

// sessionIsQueued reports whether a session is waiting for a group slot and has
// no tmux yet — so it is neither attachable nor restartable. Reads the status
// via the thread-safe getter for the same concurrency reason as sessionIsDead.
func sessionIsQueued(s *session.Instance) bool {
	return s.GetStatusThreadSafe() == session.StatusQueued
}

// renderHelpBarCurated renders the lighter, context-aware footer (the default
// "curated" style). It shows dim, plain inline hints for only the actions
// relevant to the selected row, and always keeps the settings key then the help
// key as the last two items. It advertises nothing that the verbose bar bound —
// every action remains reachable by its key and is fully listed under help (?).
func (h *Home) renderHelpBarCurated() string {
	borderStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	border := borderStyle.Render(strings.Repeat("─", max(0, h.width)))

	// Context hints (jump / empty-list / selected-row actions) are
	// lower-priority and listed in descending priority order, so they are the
	// ones dropped first when the bar is too narrow.
	var contextHints []footerHint

	switch {
	case h.jumpMode:
		contextHints = append(contextHints, footerHint{key: "a-z", label: "jump"}, footerHint{key: "esc", label: "cancel"})
	case len(h.flatItems) == 0:
		// Empty list: the useful actions all create something — new session,
		// import, or a group. Cap at the same budget as context hints.
		add := func(key, label string) {
			if len(contextHints) >= maxCuratedContextHints {
				return
			}
			if strings.TrimSpace(key) != "" {
				contextHints = append(contextHints, footerHint{key: key, label: label})
			}
		}
		add(joinHotkeyLabels(h.actionKey(hotkeyNewSession), h.actionKey(hotkeyQuickCreate)), "new")
		add(h.actionKey(hotkeyImport), "import")
		add(h.actionKey(hotkeyCreateGroup), "group")
	case h.cursor >= 0 && h.cursor < len(h.flatItems):
		contextHints = append(contextHints, h.curatedContextHints(h.flatItems[h.cursor])...)
	}

	// Settings then help are the always-kept global hints, in that order. They
	// are never dropped: when the terminal is too narrow, lower-priority context
	// hints are trimmed from the right instead so these two always survive
	// (PR #1289 review nit 2a — previously MaxWidth truncation could clip them).
	var globalHints []footerHint
	if key := h.actionKey(hotkeySettings); key != "" {
		globalHints = append(globalHints, footerHint{key: key, label: "settings"})
	}
	if key := h.actionKey(hotkeyHelp); key != "" {
		globalHints = append(globalHints, footerHint{key: key, label: "help"})
	}

	hints := h.fitCuratedHints(contextHints, globalHints)

	parts := make([]string, 0, len(hints))
	for _, hint := range hints {
		parts = append(parts, h.curatedHint(hint))
	}
	sepStyle := lipgloss.NewStyle().Foreground(ColorBorder)
	content := strings.Join(parts, sepStyle.Render("  "))

	raw := lipgloss.JoinVertical(lipgloss.Left, border, content)
	return lipgloss.NewStyle().MaxWidth(h.width).Render(raw)
}

// renderDebugBar renders a compact performance overlay for debug mode.
// Shows: render time, goroutine count, heap usage, session count.
func (h *Home) renderDebugBar() string {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	renderUs := h.lastRenderDuration.Load()
	goroutines := runtime.NumGoroutine()
	heapMB := float64(memStats.HeapAlloc) / (1024 * 1024)
	sessionCount := len(h.flatItems)

	debugText := fmt.Sprintf(
		" DEBUG | render: %dus | goroutines: %d | heap: %.1fMB | items: %d ",
		renderUs, goroutines, heapMB, sessionCount,
	)

	debugStyle := lipgloss.NewStyle().
		Foreground(ColorBg).
		Background(lipgloss.Color("#FF6600")).
		Bold(true).
		MaxWidth(h.width)

	return debugStyle.Render(debugText)
}

// renderFilterBarHint returns the filter bar's keyboard-shortcut hint as
// independent segments (status filters, all/open/archived, view mode, time
// range), with the shortcut character of the currently-engaged filter
// highlighted (subtle shade brighter than the surrounding faint hint text).
//
// Segments rather than one pre-joined string, so fitFilterBarHint can drop
// whole segments from the low-priority end when the bar would otherwise
// overflow, instead of lipgloss.MaxWidth hard-cutting mid-word (finding #2,
// cosmetic TUI review: 80x24 cut "t view • * time" down to "t v").
func (h *Home) renderFilterBarHint() []string {
	dim := lipgloss.NewStyle().Foreground(ColorComment).Faint(true)
	hi := lipgloss.NewStyle().Foreground(ColorTextDim) // same hue, no Faint

	mark := func(c string, on bool) string {
		if on {
			return hi.Render(c)
		}
		return dim.Render(c)
	}
	timeFilterKey := h.actionKey(hotkeyCycleTimeFilter)
	if timeFilterKey == "" {
		timeFilterKey = "*"
	}

	// Selected modes lead the legend, so they survive its width budget.
	var segs []string
	if h.timeFilter != session.TimeFilterAll {
		label := h.timeFilter.Label()
		if h.width <= 80 {
			switch h.timeFilter {
			case session.TimeFilter3Days:
				label = "3 days"
			case session.TimeFilter7Days:
				label = "7 days"
			case session.TimeFilter30Days:
				label = "30 days"
			}
		}
		segs = append(segs, mark(timeFilterKey, true)+dim.Render(" "+label))
	}
	if h.groupViewMode != session.GroupViewNormal {
		label := h.groupViewMode.Label()
		if h.width <= 80 {
			switch h.groupViewMode {
			case session.GroupViewActiveTop:
				label = "Active top"
			case session.GroupViewPopulatedTop:
				label = "Populated top"
			}
		}
		segs = append(segs, mark("t", true)+dim.Render(" "+label))
	}
	segs = append(segs, []string{
		mark("!", h.statusFilter == session.StatusRunning) +
			mark("@", h.statusFilter == session.StatusWaiting) +
			mark("#", h.statusFilter == session.StatusIdle) +
			mark(FilterKeyError, h.statusFilter == session.StatusError) +
			dim.Render(" filter"),
		mark("0", h.statusFilter == "") + dim.Render(" all"),
		mark(FilterKeyActive, h.statusFilter == FilterModeActive) + dim.Render(" open"),
		mark(FilterKeyArchived, h.statusFilter == FilterModeArchived) + dim.Render(" archived"),
	}...)

	// View-mode indicator (running-on-top / populated-on-top), only when active.
	if h.groupViewMode == session.GroupViewNormal {
		segs = append(segs, mark("t", false)+dim.Render(" view"))
	}

	// Time-range filter indicator (today / 3 days / 7 days / 30 days), only when active.
	if h.timeFilter == session.TimeFilterAll {
		segs = append(segs, mark(timeFilterKey, false)+dim.Render(" time"))
	}
	return segs
}
