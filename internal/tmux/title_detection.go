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
	cmd := tmuxExecContext(ctx, DefaultSocketName(),
		"list-panes", "-a", "-F",
		"#{session_name}\t#{pane_title}\t#{pane_current_command}\t#{pane_dead}\t#{window_index}\t#{pane_index}")
	output, err := cmd.Output()
	if err != nil {
		paneCacheMu.Lock()
		paneCacheData = nil
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
		return
	}

	newCache := make(map[string]PaneInfo)
	windowTools := make(map[string]map[int]string) // session -> windowIndex -> tool
	seenWindowTool := make(map[string]bool)        // "session\twinIdx" -> already processed
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
		if len(parts) != 6 {
			continue
		}
		name := parts[0]

		// Collect tool info for the first pane of each window (handles any base-index).
		// list-panes outputs panes sorted by window then pane index, so first hit = primary.
		windowKey := name + "\t" + parts[4]
		if !seenWindowTool[windowKey] {
			seenWindowTool[windowKey] = true
			var winIdx int
			_, _ = fmt.Sscanf(parts[4], "%d", &winIdx)
			// Try pane_current_command first, then pane_title (Claude shows as "bash"
			// in command but "Claude Code" in title via OSC escape sequences).
			tool := detectToolFromCommand(parts[2])
			if tool == "" {
				tool = detectToolFromCommand(parts[1])
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
				Title:          parts[1],
				CurrentCommand: parts[2],
				Dead:           parts[3] == "1",
			}
		}
	}

	paneCacheMu.Lock()
	paneCacheData = newCache
	paneCacheTime = time.Now()
	paneCacheMu.Unlock()

	updateWindowToolCache(windowTools)
}

// GetCachedPaneInfo returns cached pane info for a session.
// Returns (info, true) if found and cache is fresh, (zero, false) otherwise.
func GetCachedPaneInfo(sessionName string) (PaneInfo, bool) {
	paneCacheMu.RLock()
	defer paneCacheMu.RUnlock()

	if paneCacheData == nil || time.Since(paneCacheTime) > 4*time.Second {
		return PaneInfo{}, false
	}

	info, ok := paneCacheData[sessionName]
	return info, ok
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
