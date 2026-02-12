package tmux

import (
	"os/exec"
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
}

// Pane info cache - one list-panes call per tick instead of per-session queries.
// Mirrors the sessionCacheData pattern (tmux.go:38-42).
var (
	paneCacheMu   sync.RWMutex
	paneCacheData map[string]PaneInfo
	paneCacheTime time.Time
)

// RefreshPaneInfoCache updates the cache of pane titles and commands for all sessions.
// Call this ONCE per tick (from backgroundStatusUpdate), then use GetCachedPaneInfo()
// to read cached values. Tries PipeManager first, falls back to subprocess.
func RefreshPaneInfoCache() {
	if pm := GetPipeManager(); pm != nil {
		if info, err := pm.RefreshAllPaneInfo(); err == nil && len(info) > 0 {
			paneCacheMu.Lock()
			paneCacheData = info
			paneCacheTime = time.Now()
			paneCacheMu.Unlock()
			return
		}
		statusLog.Debug("pane_cache_subprocess_fallback")
	}

	// Subprocess fallback: list-panes -a
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{session_name}\t#{pane_title}\t#{pane_current_command}")
	output, err := cmd.Output()
	if err != nil {
		paneCacheMu.Lock()
		paneCacheData = nil
		paneCacheTime = time.Time{}
		paneCacheMu.Unlock()
		return
	}

	newCache := make(map[string]PaneInfo)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		name := parts[0]
		// Keep last pane info per session (most sessions have one pane)
		newCache[name] = PaneInfo{
			Title:          parts[1],
			CurrentCommand: parts[2],
		}
	}

	paneCacheMu.Lock()
	paneCacheData = newCache
	paneCacheTime = time.Now()
	paneCacheMu.Unlock()
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
