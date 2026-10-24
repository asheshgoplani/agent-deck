package ui

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/asheshgoplani/agent-deck/internal/optiming"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// invalidatePreviewCache removes a session's preview from the cache
// Called when session is deleted, renamed, or moved to ensure stale data is not displayed
func (h *Home) invalidatePreviewCache(sessionID string) {
	h.previewCacheMu.Lock()
	delete(h.previewCache, sessionID)
	delete(h.previewCacheTime, sessionID)
	h.previewCacheMu.Unlock()
}

// pruneAnalyticsCache removes stale entries from analytics and log activity caches.
// Called periodically from the tick handler to prevent unbounded map growth.
func (h *Home) pruneAnalyticsCache() {
	const maxAge = 10 * time.Minute
	now := time.Now()

	h.analyticsCacheMu.Lock()
	for id, t := range h.analyticsCacheTime {
		if now.Sub(t) > maxAge {
			delete(h.analyticsCache, id)
			delete(h.geminiAnalyticsCache, id)
			delete(h.analyticsCacheTime, id)
		}
	}
	h.analyticsCacheMu.Unlock()

	h.logActivityMu.Lock()
	for id, t := range h.lastLogActivity {
		if now.Sub(t) > maxAge {
			delete(h.lastLogActivity, id)
		}
	}
	h.logActivityMu.Unlock()

	// Prune MCP info cache (entries older than 10 minutes)
	session.PruneMCPCache(maxAge)
	session.PruneCursorMCPCache(maxAge)
}

// cleanupExpiredAnimations removes expired entries from an animation map
// Returns list of IDs that were removed (for logging/debugging if needed)
func (h *Home) cleanupExpiredAnimations(
	animMap map[string]time.Time,
	claudeTimeout, defaultTimeout time.Duration,
) []string {
	var toDelete []string
	for sessionID, startTime := range animMap {
		inst := h.instanceByID[sessionID]
		if inst == nil {
			// Session was deleted, clean up
			toDelete = append(toDelete, sessionID)
			continue
		}
		// Use appropriate timeout based on tool
		// Claude and Gemini use longer timeout (MCP loading can be slow)
		timeout := defaultTimeout
		if session.IsClaudeCompatible(inst.Tool) || inst.Tool == "gemini" {
			timeout = claudeTimeout
		}
		if time.Since(startTime) > timeout {
			toDelete = append(toDelete, sessionID)
		}
	}
	for _, id := range toDelete {
		delete(animMap, id)
	}
	return toDelete
}

func launchAnimationMinDuration(tool string) time.Duration {
	if session.IsClaudeCompatible(tool) || tool == "gemini" {
		return minLaunchAnimationDurationClaude
	}
	return minLaunchAnimationDurationDefault
}

// hasActiveAnimation checks if a session has an animation currently being displayed
// Returns true only if the animation is actually showing (not just tracked in the map)
// This MUST match the display logic in renderPreviewPane exactly
func (h *Home) hasActiveAnimation(sessionID string) bool {
	inst := h.instanceByID[sessionID]
	if inst == nil {
		return false
	}

	// Check forking first (always shows while tracked)
	if _, ok := h.forkingSessions[sessionID]; ok {
		return true
	}

	// Determine animation start time and type
	var startTime time.Time
	var hasAnimation bool

	if t, ok := h.launchingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	} else if t, ok := h.resumingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	} else if t, ok := h.mcpLoadingSessions[sessionID]; ok {
		startTime = t
		hasAnimation = true
	}

	if !hasAnimation {
		return false
	}

	// STATUS-BASED ANIMATION: Show animation until session is ready
	// Instead of hardcoded 6-second minimum, use actual session status
	// Status is updated via background polling (2s interval)
	timeSinceStart := time.Since(startTime)

	// Brief minimum to prevent flicker during rapid status changes
	if timeSinceStart < launchAnimationMinDuration(inst.GetToolThreadSafe()) {
		return true
	}

	// Maximum animation time (15s) as safety fallback
	if timeSinceStart >= 15*time.Second {
		return false
	}

	// STATUS-BASED CHECK: Session is ready when status is Running or Waiting
	// - StatusRunning (GREEN): Claude is actively processing
	// - StatusWaiting (YELLOW): Claude is at prompt, waiting for input
	// - StatusIdle (GRAY): Claude has stopped and user acknowledged
	animStatus := inst.GetStatusThreadSafe()
	animTool := inst.GetToolThreadSafe()
	if animStatus == session.StatusRunning ||
		animStatus == session.StatusWaiting ||
		animStatus == session.StatusIdle {
		// Session is ready - stop animation immediately
		return false
	}

	// CONTENT-BASED CHECK: Also check preview content for faster detection
	// This catches cases where status hasn't updated yet but content is visible
	h.previewCacheMu.RLock()
	previewContent := h.previewCache[sessionID]
	h.previewCacheMu.RUnlock()

	// Strip ANSI for reliable pattern matching (preview cache now contains ANSI-rich content)
	plainPreview := ansi.Strip(previewContent)

	if animTool == "claude" || animTool == "gemini" {
		// Claude ready indicators
		agentReady := strings.Contains(plainPreview, "ctrl+c to interrupt") ||
			strings.Contains(plainPreview, "No, and tell Claude what to do differently") ||
			strings.Contains(plainPreview, "\n> ") ||
			strings.Contains(plainPreview, "> \n") ||
			strings.Contains(plainPreview, "esc to interrupt") ||
			strings.Contains(plainPreview, "⠋") || strings.Contains(plainPreview, "⠙") ||
			strings.Contains(plainPreview, "Thinking") ||
			strings.Contains(plainPreview, "╭─") // Claude UI border

		// Gemini prompts
		if animTool == "gemini" {
			agentReady = agentReady ||
				strings.Contains(plainPreview, "▸") ||
				strings.Contains(plainPreview, "gemini>")
		}

		if agentReady {
			return false
		}
	} else {
		// Non-Claude/Gemini: ready if any substantial content (>50 chars)
		if len(strings.TrimSpace(plainPreview)) > 50 {
			return false
		}
	}

	// Not ready yet - keep showing animation
	return true
}

// previewCacheKey returns the cache key for a preview: sessionID or sessionID:windowIndex.
func previewCacheKey(sessionID string, windowIndex int) string {
	if windowIndex < 0 {
		return sessionID
	}
	return fmt.Sprintf("%s:%d", sessionID, windowIndex)
}

func remotePreviewCacheKey(remoteName, sessionID string) string {
	return fmt.Sprintf("remote:%s:%s", remoteName, sessionID)
}

func truncateRemotePreviewContent(content string) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) > remotePreviewMaxLines {
		lines = lines[len(lines)-remotePreviewMaxLines:]
		content = strings.Join(lines, "\n")
	}

	if len(content) <= remotePreviewMaxBytes {
		return content
	}

	trimmed := []byte(content)
	trimmed = trimmed[len(trimmed)-remotePreviewMaxBytes:]
	for len(trimmed) > 0 && !utf8.Valid(trimmed) {
		trimmed = trimmed[1:]
	}
	if idx := bytes.IndexByte(trimmed, '\n'); idx >= 0 && idx < len(trimmed)-1 {
		trimmed = trimmed[idx+1:]
	}

	return string(trimmed)
}

// fetchPreview returns a command that asynchronously fetches preview content.
// windowIndex < 0 captures the session's primary pane; >= 0 captures a specific window.
func (h *Home) fetchPreview(inst *session.Instance, key string, windowIndex int) tea.Cmd {
	if inst == nil {
		return nil
	}
	return func() tea.Msg {
		var started time.Time
		if optiming.Enabled() {
			started = time.Now()
		}
		var content string
		var err error
		if windowIndex >= 0 {
			content, err = inst.PreviewWindowFull(windowIndex)
		} else {
			content, err = inst.PreviewFull()
		}
		return previewFetchedMsg{
			previewKey: key,
			content:    content,
			err:        err,
			started:    started,
		}
	}
}

// fetchPreviewDebounced returns a command that triggers preview fetch after debounce delay.
// PERFORMANCE: Prevents rapid subprocess spawning during keyboard navigation.
func (h *Home) fetchPreviewDebounced(sessionID string, windowIndex int) tea.Cmd {
	const debounceDelay = 150 * time.Millisecond

	key := previewCacheKey(sessionID, windowIndex)
	h.previewDebounceMu.Lock()
	h.pendingPreviewKey = key
	h.previewDebounceMu.Unlock()

	return func() tea.Msg {
		time.Sleep(debounceDelay)
		return previewDebounceMsg{previewKey: key, sessionID: sessionID, windowIndex: windowIndex}
	}
}

func (h *Home) fetchRemotePreviewDebounced(remoteName, sessionID string) tea.Cmd {
	const debounceDelay = 150 * time.Millisecond

	key := remotePreviewCacheKey(remoteName, sessionID)
	h.previewDebounceMu.Lock()
	h.pendingPreviewKey = key
	h.previewDebounceMu.Unlock()

	return func() tea.Msg {
		time.Sleep(debounceDelay)
		return previewDebounceMsg{previewKey: key, sessionID: sessionID, windowIndex: -1, remoteName: remoteName}
	}
}

func (h *Home) fetchRemotePreview(remoteName, sessionID, key string) tea.Cmd {
	return func() tea.Msg {
		config, err := session.LoadUserConfig()
		if err != nil || config == nil || config.Remotes == nil {
			return previewFetchedMsg{previewKey: key, err: fmt.Errorf("failed to load remote config")}
		}

		rc, ok := config.Remotes[remoteName]
		if !ok {
			return previewFetchedMsg{previewKey: key, err: fmt.Errorf("remote '%s' not found", remoteName)}
		}

		if h.remoteAuthBlocked(remoteName, rc) {
			return previewFetchedMsg{previewKey: key, err: fmt.Errorf("auth failed; run agent-deck remote list --retry")}
		}
		runner := session.NewSSHRunner(remoteName, rc)
		ctx, cancel := context.WithTimeout(h.ctx, rc.GetCommandTimeout())
		defer cancel()

		// #1101: use FetchSessionPane (raw capture-pane content with ANSI +
		// tool UI chrome) instead of FetchSessionOutput (parsed transcript
		// text) so claude-formatted previews render the same way local
		// sessions do. If the remote agent-deck predates --pane, fall back
		// to the transcript path so the preview is at least non-empty, and
		// remember that for the remote so later cycles go there directly.
		var content string
		var fetchErr error
		if h.remotePaneIsUnsupported(remoteName) {
			content, fetchErr = runner.FetchSessionOutput(ctx, sessionID)
		} else {
			content, fetchErr = runner.FetchSessionPane(ctx, sessionID)
			if fetchErr != nil && remotePaneFlagUnknown(fetchErr) {
				h.setRemotePaneUnsupported(remoteName)
			}
			if fetchErr != nil || strings.TrimSpace(content) == "" {
				if fallback, fbErr := runner.FetchSessionOutput(ctx, sessionID); fbErr == nil && strings.TrimSpace(fallback) != "" {
					content = fallback
					fetchErr = nil
				}
			}
		}
		content = truncateRemotePreviewContent(content)
		return previewFetchedMsg{previewKey: key, content: content, err: fetchErr}
	}
}

// remotePaneFlagUnknown recognises the flag package's complaint from a remote
// build that predates `session output --pane`.
func remotePaneFlagUnknown(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "flag provided but not defined") && strings.Contains(msg, "pane")
}

func (h *Home) remotePaneIsUnsupported(remoteName string) bool {
	h.remotePaneUnsupportedMu.Lock()
	defer h.remotePaneUnsupportedMu.Unlock()
	return h.remotePaneUnsupported[remoteName]
}

func (h *Home) setRemotePaneUnsupported(remoteName string) {
	h.remotePaneUnsupportedMu.Lock()
	defer h.remotePaneUnsupportedMu.Unlock()
	if h.remotePaneUnsupported == nil {
		h.remotePaneUnsupported = map[string]bool{}
	}
	h.remotePaneUnsupported[remoteName] = true
}

// remotePaneWatchActive reports whether the remote agent is pushing the
// pane of this remote session over a live channel, in which case the
// preview needs no poll.
func remotePaneWatchActive(remoteName, sessionID string) bool {
	ch := session.RemoteChannelFor(remoteName)
	return ch != nil && ch.Connected() && ch.Watching() == sessionID
}

// syncRemotePaneWatch makes the remote agents' pane watch follow the cursor:
// the focused remote session is watched over its channel (when the channel
// is up and the agent knows how), any previously watched session on another
// remote is released, and a channel that reconnected (its watch died with
// the transport) is asked again. Returns the commands that talk to the
// remotes; callers batch them.
func (h *Home) syncRemotePaneWatch() tea.Cmd {
	var want remotePaneWatchTarget
	if remoteName, sessionID, _, ok := h.selectedRemotePreviewTarget(); ok && h.getLayoutMode() != LayoutModeSingle {
		want = remotePaneWatchTarget{remote: remoteName, session: sessionID}
	}
	var cmds []tea.Cmd
	have := h.remotePaneWatch
	h.remotePaneWatch = remotePaneWatchTarget{}
	// Another session on the same remote needs no release: the agent
	// replaces its watch when the new one arrives.
	if have.remote != "" && have.remote != want.remote {
		if ch := session.RemoteChannelFor(have.remote); ch != nil {
			cmds = append(cmds, h.remotePaneUnwatchCmd(ch))
		}
	}
	if want.remote == "" {
		return batchCmds(cmds)
	}
	ch := session.RemoteChannelFor(want.remote)
	if ch == nil || !ch.Connected() || !ch.PaneWatchSupported() {
		return batchCmds(cmds)
	}
	h.remotePaneWatch = want
	if ch.Watching() != want.session {
		cmds = append(cmds, h.remotePaneWatchCmd(ch, want))
	}
	return batchCmds(cmds)
}

func batchCmds(cmds []tea.Cmd) tea.Cmd {
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (h *Home) remotePaneWatchCmd(ch *session.RemoteChannel, target remotePaneWatchTarget) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		defer cancel()
		err := ch.Watch(ctx, target.session, remotePreviewMaxLines)
		if err != nil {
			uiLog.Debug("remote_pane_watch_failed", slog.String("remote", target.remote), slog.String("session", target.session), slog.String("err", err.Error()))
		}
		return remotePaneWatchMsg{target: target, err: err}
	}
}

func (h *Home) remotePaneUnwatchCmd(ch *session.RemoteChannel) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
		defer cancel()
		_ = ch.Unwatch(ctx)
		return nil
	}
}

// applyRemotePane stores a pushed pane capture as the preview of that
// remote session, the same way a fetched preview lands, so the renderer and
// the TTL logic see no difference. A capture failure or an empty screen
// counts as a fetch failure, as on the poll path: the last content stays,
// the TTL advances. It reports whether the preview is blank afterwards
// (nothing cached and no pane text: the session is stopped, or its screen
// is empty), in which case the caller polls once so the transcript fallback
// of the poll path still shows something, as it did before pushes existed.
func (h *Home) applyRemotePane(remoteName string, pane *session.RemotePaneEvent) (blank bool) {
	key := remotePreviewCacheKey(remoteName, pane.Session)
	h.previewCacheMu.Lock()
	defer h.previewCacheMu.Unlock()
	if h.previewFetchingID == key {
		h.previewFetchingID = ""
	}
	h.previewCacheTime[key] = time.Now()
	if content := expandTabs(truncateRemotePreviewContent(pane.Content)); pane.Err == "" && strings.TrimSpace(content) != "" {
		h.previewCache[key] = content
	}
	return strings.TrimSpace(h.previewCache[key]) == ""
}

// pollRemotePreviewIfSelected starts one ssh poll of the remote session's
// preview when that session is still under the cursor and no poll for it
// is in flight; nil otherwise.
func (h *Home) pollRemotePreviewIfSelected(target remotePaneWatchTarget) tea.Cmd {
	_, _, key, ok := h.selectedRemotePreviewTarget()
	if !ok || key != remotePreviewCacheKey(target.remote, target.session) {
		return nil
	}
	h.previewCacheMu.Lock()
	needsFetch := h.previewFetchingID != key
	if needsFetch {
		h.previewFetchingID = key
	}
	h.previewCacheMu.Unlock()
	if !needsFetch {
		return nil
	}
	return h.fetchRemotePreview(target.remote, target.session, key)
}

// selectedPreviewTarget returns the instance, cache key, and window index for the currently
// selected flat item. windowIndex is -1 for session items.
func (h *Home) selectedPreviewTarget() (*session.Instance, string, int) {
	if h.cursor >= len(h.flatItems) {
		return nil, "", -1
	}
	item := h.flatItems[h.cursor]
	switch item.Type {
	case session.ItemTypeSession:
		if item.Session == nil {
			return nil, "", -1
		}
		return item.Session, item.Session.ID, -1
	case session.ItemTypeWindow:
		inst := h.getInstanceByID(item.WindowSessionID)
		if inst == nil {
			return nil, "", -1
		}
		return inst, previewCacheKey(inst.ID, item.WindowIndex), item.WindowIndex
	}
	return nil, "", -1
}

func (h *Home) selectedRemotePreviewTarget() (string, string, string, bool) {
	if h.cursor >= len(h.flatItems) {
		return "", "", "", false
	}
	item := h.flatItems[h.cursor]
	if item.Type != session.ItemTypeRemoteSession || item.RemoteSession == nil {
		return "", "", "", false
	}
	if item.RemoteName == "" || item.RemoteSession.ID == "" {
		return "", "", "", false
	}

	key := remotePreviewCacheKey(item.RemoteName, item.RemoteSession.ID)
	return item.RemoteName, item.RemoteSession.ID, key, true
}

// fetchSelectedPreview debounces a preview fetch for the currently selected item.
// Handles both session and window items transparently.
func (h *Home) fetchSelectedPreview() tea.Cmd {
	// Issue #1366: in single-column layout there is no preview pane, so there is
	// nothing to fill — skip the `tmux capture-pane` entirely. The preview is
	// re-fetched on resize back into a preview layout (see WindowSizeMsg).
	if h.getLayoutMode() == LayoutModeSingle {
		return nil
	}
	inst, _, winIdx := h.selectedPreviewTarget()
	if inst == nil {
		remoteName, remoteSessionID, _, ok := h.selectedRemotePreviewTarget()
		if !ok {
			return nil
		}
		return h.fetchRemotePreviewDebounced(remoteName, remoteSessionID)
	}
	return h.fetchPreviewDebounced(inst.ID, winIdx)
}

// detectOpenCodeSessionCmd returns a command that asynchronously detects
// the OpenCode session ID for a restored session and signals completion.
// This follows the Bubble Tea pattern of returning a tea.Cmd for async work.
func (h *Home) detectOpenCodeSessionCmd(inst *session.Instance) tea.Cmd {
	if inst == nil {
		return nil
	}

	instanceID := inst.ID

	return func() tea.Msg {
		// Run detection (this blocks until complete or timeout)
		inst.DetectOpenCodeSession()

		// Return message to trigger save
		return openCodeDetectionCompleteMsg{
			instanceID: instanceID,
			sessionID:  inst.OpenCodeSessionID,
		}
	}
}

// getAnalyticsForSession returns cached analytics if still valid (within TTL)
// Returns nil if cache miss or expired, triggering async fetch
func (h *Home) getAnalyticsForSession(inst *session.Instance) *session.SessionAnalytics {
	if inst == nil {
		return nil
	}

	// Check cache under lock (background status worker also reads this path).
	h.analyticsCacheMu.RLock()
	cached, ok := h.analyticsCache[inst.ID]
	cacheTime, hasCacheTime := h.analyticsCacheTime[inst.ID]
	h.analyticsCacheMu.RUnlock()
	if ok && hasCacheTime && time.Since(cacheTime) < analyticsCacheTTL {
		return cached
	}

	return nil // Will trigger async fetch
}

// fetchAnalytics returns a command that asynchronously parses session analytics
// This keeps View() pure (no blocking I/O) as per Bubble Tea best practices
func (h *Home) fetchAnalytics(inst *session.Instance) tea.Cmd {
	if inst == nil {
		return nil
	}
	sessionID := inst.ID

	fetchTool := inst.GetToolThreadSafe()
	switch fetchTool {
	case "claude":
		claudeSessionID := inst.ClaudeSessionID
		return func() tea.Msg {
			// Get JSONL path for this session
			jsonlPath := inst.GetJSONLPath()
			if jsonlPath == "" {
				// No JSONL path available - return empty analytics
				return analyticsFetchedMsg{
					sessionID: sessionID,
					analytics: nil,
					err:       nil,
				}
			}

			// Parse the JSONL file
			analytics, err := session.ParseSessionJSONL(jsonlPath)
			if err != nil {
				uiLog.Warn(
					"analytics_parse_failed",
					slog.String("session_id", sessionID),
					slog.String("claude_session_id", claudeSessionID),
					slog.String("error", err.Error()),
				)
				return analyticsFetchedMsg{
					sessionID: sessionID,
					analytics: nil,
					err:       err,
				}
			}

			return analyticsFetchedMsg{
				sessionID: sessionID,
				analytics: analytics,
				err:       nil,
			}
		}
	case "gemini":
		return func() tea.Msg {
			// Gemini analytics are updated via UpdateGeminiSession which is called in background
			// during UpdateStatus(). We just return the current snapshot.
			return analyticsFetchedMsg{
				sessionID:       sessionID,
				geminiAnalytics: inst.GeminiAnalytics,
				err:             nil,
			}
		}
	}

	return nil
}
