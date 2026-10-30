// Package trace records what AI agents do inside agent-deck sessions (ASK-129):
// one trace per agent session, spans for turns, tool calls and waits, and an
// event row per observed hook edge, in a per-profile SQLite file (trace.db).
//
// Write path. The hook handlers (Claude's hook-handler, Codex's hooks through
// the same handler, codex-notify) never open SQLite: they normalize the hook
// payload into one small, already-redacted SpoolRecord and append it as one
// JSON line to a per-profile spool file (AppendSpool). Ingest moves spool
// lines into trace.db in one transaction, idempotently, and is called by
// `agent-deck trace ingest` (and later the daemon).
//
// Privacy. Nothing is written unless the level is metadata or summaries
// (default off). Arguments and results are never stored: at summaries level
// a tool call carries a short shape summary (Summarize) that is passed
// through Scrub before it reaches the spool.
package trace

import (
	"strings"
)

// Level is the opt-in tracing level.
type Level int

const (
	// LevelOff writes nothing (the default).
	LevelOff Level = iota
	// LevelMetadata records ids, tool names, timings and status only.
	LevelMetadata
	// LevelSummaries adds a short, scrubbed shape summary per tool call.
	LevelSummaries
)

// EnvLevel is the environment override for the level. It wins over the
// [trace] level key in config.toml.
const EnvLevel = "AGENTDECK_TRACE"

// ParseLevel reads a level name. Unknown or empty values are off, so a typo
// can never turn tracing on.
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "metadata", "meta", "1":
		return LevelMetadata
	case "summaries", "summary", "2":
		return LevelSummaries
	default:
		return LevelOff
	}
}

// ResolveLevel applies the env-over-config rule: a non-empty env value wins,
// otherwise the config value applies.
func ResolveLevel(envValue, configValue string) Level {
	if strings.TrimSpace(envValue) != "" {
		return ParseLevel(envValue)
	}
	return ParseLevel(configValue)
}

func (l Level) String() string {
	switch l {
	case LevelMetadata:
		return "metadata"
	case LevelSummaries:
		return "summaries"
	default:
		return "off"
	}
}
