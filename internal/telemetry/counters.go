package telemetry

import (
	"sort"
	"strings"
	"syscall"
)

// Counters are allowlisted here and in TELEMETRY.md; all other keys are dropped.
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

// Built-in tool names only; user-chosen names are reported as "other".
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

func allowedKey(key string) bool {
	if plainCounters[key] {
		return true
	}
	if t, ok := strings.CutPrefix(key, sessionsStartedPrefix); ok {
		return knownTools[t] || t == toolOther
	}
	return false
}

// Record persists a best-effort counter, skipping contention to avoid blocking the UI.
func Record(key string) {
	if !allowedKey(key) {
		return
	}
	if HardDisabled() || !Interactive() {
		return
	}
	unlock, err := lockStateWithFlags(syscall.LOCK_EX | syscall.LOCK_NB)
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
