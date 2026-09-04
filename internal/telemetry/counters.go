package telemetry

import (
	"sort"
	"strings"
)

// Counter names. This list is the payload allowlist: Record drops anything
// else, so a new call site cannot leak a new dimension without editing this
// file and TELEMETRY.md.
const (
	CounterTUILaunches    = "tui_launches"
	CounterCLIInvocations = "cli_invocations"
	CounterRemoteUsed     = "remote_used"
	CounterConductorUsed  = "conductor_used"

	sessionsStartedPrefix = "sessions_started."
	toolOther             = "other"
)

var plainCounters = map[string]bool{
	CounterTUILaunches:    true,
	CounterCLIInvocations: true,
	CounterRemoteUsed:     true,
	CounterConductorUsed:  true,
}

// knownTools mirrors the built-in tool registry in internal/session. Custom
// tool names are user-chosen strings and are reported as "other".
var knownTools = map[string]bool{
	"claude": true, "codex": true, "gemini": true, "opencode": true, "pi": true,
	"copilot": true, "crush": true, "cursor": true, "hermes": true,
	"deepseek": true, "aider": true, "shell": true,
}

// AllowedCounterKeys returns every key that may appear in a payload, sorted.
func AllowedCounterKeys() []string {
	keys := make([]string, 0, len(plainCounters)+len(knownTools)+1)
	for k := range plainCounters {
		keys = append(keys, k)
	}
	for t := range knownTools {
		keys = append(keys, sessionsStartedPrefix+t)
	}
	keys = append(keys, sessionsStartedPrefix+toolOther)
	sort.Strings(keys)
	return keys
}

// SessionStartedKey returns the counter key for a started session, with the tool name normalised to the built-in allowlist.
func SessionStartedKey(tool string) string {
	tool = strings.ToLower(strings.TrimSpace(tool))
	if !knownTools[tool] {
		tool = toolOther
	}
	return sessionsStartedPrefix + tool
}

// allowedKey reports whether key may be counted.
func allowedKey(key string) bool {
	if plainCounters[key] {
		return true
	}
	if t, ok := strings.CutPrefix(key, sessionsStartedPrefix); ok {
		return knownTools[t] || t == toolOther
	}
	return false
}

// Record increments a counter, persisting the state.
func Record(key string) {
	if !allowedKey(key) {
		return
	}
	if HardDisabled() || !Interactive() {
		return
	}
	unlock, err := lockState()
	if err != nil {
		return
	}
	defer unlock()
	s := LoadState()
	if enabled, _ := Enabled(s); !enabled {
		return
	}
	if s.Counters == nil {
		s.Counters = map[string]int{}
	}
	if s.Counters[key] < maxCounter {
		s.Counters[key]++
	}
	_ = saveStateLocked(s)
}

// RecordSessionStarted counts a started session by normalised tool name.
func RecordSessionStarted(tool string) {
	Record(SessionStartedKey(tool))
}
