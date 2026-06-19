package tmux

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// TitleState represents the state inferred from the tmux pane title.
// Claude Code sets pane titles via OSC escape sequences:
//   - Braille spinner chars (U+2800-28FF) while actively working
//   - Done markers (✳✻✽✶✢) when a task completes
type TitleState int

const (
	TitleStateUnknown TitleState = iota // No recognizable pattern (non-Claude tools)
	TitleStateWorking                   // Braille spinner detected = actively working
	TitleStateDone                      // Done marker detected, fall through to prompt detection
)

// PaneInfo holds pane title and current command for a tmux session.
type PaneInfo struct {
	Title          string
	CurrentCommand string
	Dead           bool
}

// WindowInfo holds basic info about a tmux window within a session.
type WindowInfo struct {
	Index    int
	Name     string
	Activity int64
	Tool     string // Detected tool (claude, gemini, etc.) or empty
}

// Pane info cache - one list-panes call per tick instead of per-session queries.
// Mirrors the sessionCacheData pattern (tmux.go:38-42).
var (
	paneCacheMu   sync.RWMutex
	paneCacheData map[string]PaneInfo
	paneCacheTime time.Time
)

// Window cache - populated alongside session cache from the same list-windows call.
var (
	windowCacheMu   sync.RWMutex
	windowCacheData map[string][]WindowInfo // session_name -> sorted windows
	windowCacheTime time.Time
)

// Separate tool cache - written ONLY by RefreshPaneInfoCache, read by GetCachedWindows.
// This eliminates the race where RefreshSessionCache overwrites windowCacheData
// (losing tool info) and RefreshPaneInfoCache tries to add it back.
var (
	windowToolCacheMu   sync.RWMutex
	windowToolCacheData map[string]map[int]string // session -> winIndex -> tool
)

// GetCachedWindows returns cached window info for a session with tool data merged in.
// Returns a copy — callers cannot mutate the cache.
// Returns nil if not found or cache is stale.
func GetCachedWindows(sessionName string) []WindowInfo {
	// Read window data
	windowCacheMu.RLock()
	stale := windowCacheData == nil || time.Since(windowCacheTime) > 4*time.Second
	var src []WindowInfo
	if !stale {
		src = windowCacheData[sessionName]
	}
	windowCacheMu.RUnlock()

	if stale || src == nil {
		return nil
	}

	// Copy the slice so callers can't mutate the cache
	result := make([]WindowInfo, len(src))
	copy(result, src)

	// Merge tool data from the separate tool cache
	windowToolCacheMu.RLock()
	tools := windowToolCacheData[sessionName]
	windowToolCacheMu.RUnlock()

	if len(tools) > 0 {
		for i := range result {
			if tool, ok := tools[result[i].Index]; ok {
				result[i].Tool = tool
			}
		}
	}

	return result
}

// updateWindowToolCache replaces the entire tool cache with new data.
// Called ONLY by RefreshPaneInfoCache — tool detection lives there.
func updateWindowToolCache(windowTools map[string]map[int]string) {
	windowToolCacheMu.Lock()
	windowToolCacheData = windowTools
	windowToolCacheMu.Unlock()
}

// RefreshPaneInfoCache updates the cache of pane titles and commands for all sessions.
// Call this ONCE per tick (from backgroundStatusUpdate), then use GetCachedPaneInfo()
// to read cached values. Tries PipeManager first, falls back to subprocess.
func RefreshPaneInfoCache() {
	if pm := GetPipeManager(); pm != nil {
		if info, windowTools, err := pm.RefreshAllPaneInfo(); err == nil && len(info) > 0 {
			paneCacheMu.Lock()
			paneCacheData = info
			paneCacheTime = time.Now()
			paneCacheMu.Unlock()
			updateWindowToolCache(windowTools)
			return
		}
		statusLog.Debug("pane_cache_subprocess_fallback")
	}

	// Subprocess fallback: list-panes -a (3s timeout to prevent freeze when server is dead).
	// Package-level probe: routes through tmuxExecContext + DefaultSocketName()
	// so installations with [tmux].socket_name set see their isolated server
	// (#687 follow-up, v1.7.55).
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	// pane_title is free-text (apps set it via OSC) so it goes LAST; every
	// other field is a sanitized name, integer, or 0/1 flag that cannot
	// contain tmuxFieldSep. See tmuxFieldSep for why TAB is unusable here.
	cmd := tmuxExecContext(ctx, DefaultSocketName(),
		"list-panes", "-a", "-F",
		tmuxFmt("#{session_name}", "#{pane_current_command}", "#{pane_dead}", "#{window_index}", "#{pane_index}", "#{pane_title}"))
	output, err := cmd.Output()
	if err != nil {
		paneCacheMu.Lock()
		paneCacheData = nil
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
		return
	}

	newCache, windowTools := parseListPanesOutput(string(output))

	paneCacheMu.Lock()
	paneCacheData = newCache
	paneCacheTime = time.Now()
	paneCacheMu.Unlock()

	updateWindowToolCache(windowTools)
}

// parseListPanesOutput parses `tmux list-panes -a` output in the format
// tmuxFmt("#{session_name}", "#{pane_current_command}", "#{pane_dead}",
// "#{window_index}", "#{pane_index}", "#{pane_title}"). pane_title is last so a
// tmuxFieldSep inside it survives SplitN. Returns the per-session primary
// PaneInfo and the session→windowIndex→tool map. Extracted from
// RefreshPaneInfoCache so the no-client delimiter handling is unit-testable.
func parseListPanesOutput(output string) (map[string]PaneInfo, map[string]map[int]string) {
	newCache := make(map[string]PaneInfo)
	windowTools := make(map[string]map[int]string) // session -> windowIndex -> tool
	seenWindowTool := make(map[string]bool)        // "session|winIdx" -> already processed
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		// Field order: session_name | pane_current_command | pane_dead |
		// window_index | pane_index | pane_title (pane_title last, free-text).
		parts := strings.SplitN(line, tmuxFieldSep, 6)
		if len(parts) != 6 {
			continue
		}
		name := parts[0]
		paneCommand := parts[1]
		paneDead := parts[2]
		windowIndex := parts[3]
		paneTitle := parts[5]

		// Collect tool info for the first pane of each window (handles any base-index).
		// list-panes outputs panes sorted by window then pane index, so first hit = primary.
		windowKey := name + tmuxFieldSep + windowIndex
		if !seenWindowTool[windowKey] {
			seenWindowTool[windowKey] = true
			var winIdx int
			_, _ = fmt.Sscanf(windowIndex, "%d", &winIdx)
			// Try pane_current_command first, then pane_title (Claude shows as "bash"
			// in command but "Claude Code" in title via OSC escape sequences).
			tool := detectToolFromCommand(paneCommand)
			if tool == "" {
				tool = detectToolFromCommand(paneTitle)
			}
			if tool != "" {
				if windowTools[name] == nil {
					windowTools[name] = make(map[int]string)
				}
				windowTools[name][winIdx] = tool
			}
		}

		// Cache the first pane seen per session (primary window+pane).
		// Handles any base-index config — first entry in sorted list-panes output is primary.
		if _, seen := newCache[name]; !seen {
			newCache[name] = PaneInfo{
				Title:          paneTitle,
				CurrentCommand: paneCommand,
				Dead:           paneDead == "1",
			}
		}
	}
	return newCache, windowTools
}

// GetCachedPaneInfo returns cached pane info for a session.
// Returns (info, true) if found and cache is fresh, (zero, false) otherwise.
func GetCachedPaneInfo(sessionName string) (PaneInfo, bool) {
	info, _, ok := GetCachedPaneInfoSnapshot(sessionName)
	return info, ok
}

// GetCachedPaneInfoSnapshot is GetCachedPaneInfo plus the time the cache
// snapshot was taken. Callers that promote state based on pane info (e.g. the
// shell foreground running indicator) use the snapshot time to reject entries
// that predate an event of interest — a pane snapshot taken before a session
// was (re)started describes the previous same-name session, not the current
// one. The same 4s freshness window applies (2 refresh ticks).
func GetCachedPaneInfoSnapshot(sessionName string) (PaneInfo, time.Time, bool) {
	paneCacheMu.RLock()
	defer paneCacheMu.RUnlock()

	if paneCacheData == nil || time.Since(paneCacheTime) > 4*time.Second {
		return PaneInfo{}, time.Time{}, false
	}

	info, ok := paneCacheData[sessionName]
	return info, paneCacheTime, ok
}

// AnalyzePaneTitle determines session state from the pane title.
// Priority: Braille spinner > Done marker > Unknown.
//
// NOTE: We intentionally do NOT use pane_current_command to detect "exited" state.
// Claude Code frequently spawns bash subprocesses for tool execution, and tmux
// reports that child process as pane_current_command. This means a waiting Claude
// session often shows "bash" as the command, making it indistinguishable from
// "Claude exited and shell is showing". The existing Exists() check handles
// truly dead sessions reliably.
func AnalyzePaneTitle(title, _ string) TitleState {
	if title == "" {
		return TitleStateUnknown
	}

	// Braille spinner in title = Claude is actively working
	if containsBrailleChar(title) {
		return TitleStateWorking
	}

	// Done marker (✳✻✽✶✢) = Claude finished a task, fall through to prompt detection
	if containsDoneMarker(title) {
		return TitleStateDone
	}

	return TitleStateUnknown
}

// CleanPaneTitle strips spinner/done-marker characters from a tmux pane title
// and returns the task description. Returns "" for empty or generic tool titles
// ("Claude Code", "Gemini CLI", "Codex CLI").
//
// This is the canonical implementation shared by internal/ui (TUI) and
// internal/web (web server). Both packages import internal/tmux, so placing
// the logic here avoids a circular dependency.
func CleanPaneTitle(title string) string {
	if title == "" {
		return ""
	}
	// Strip known spinner/done-marker runes (·✳✽✶✻✢ and braille ⠋⠙⠹…).
	cleaned := StripSpinnerRunes(title)
	// Also strip any remaining Braille characters (U+2800-28FF) that Claude Code
	// may use as spinner frames beyond the canonical set.
	cleaned = strings.TrimLeftFunc(cleaned, func(r rune) bool {
		return r >= 0x2800 && r <= 0x28FF
	})
	cleaned = strings.TrimSpace(cleaned)
	switch cleaned {
	case "", "Claude Code", "Gemini CLI", "Codex CLI":
		return ""
	}
	return cleaned
}

// containsBrailleChar returns true if the string contains any Unicode Braille
// character (U+2800 to U+28FF). Claude Code uses these as spinner frames
// in the pane title while actively processing.
func containsBrailleChar(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

// containsDoneMarker returns true if the string contains any of the "done"
// asterisk markers that Claude Code sets when a task completes.
func containsDoneMarker(s string) bool {
	for _, r := range s {
		switch r {
		case '✳', '✻', '✽', '✶', '✢':
			return true
		}
	}
	return false
}
