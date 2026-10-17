package session

import (
	"errors"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/telemetry"
)

// RecordTelemetryCreate records session.create for a newly created and
// started session (no-op without consent). The tool name is normalised and
// the session id hashed inside the telemetry package.
func (i *Instance) RecordTelemetryCreate(via telemetry.CreateVia) {
	telemetry.SessionCreated(telemetry.SessionCreateInfo{
		Tool:      i.Tool,
		Via:       via,
		Worktree:  i.WorktreePath != "",
		MCPs:      len(i.LoadedMCPNames),
		InGroup:   i.GroupPath != "" && i.GroupPath != DefaultGroupPath,
		Remote:    i.SSHHost != "",
		Parented:  i.ParentSessionID != "",
		SessionID: i.ID,
	})
}

// RecordTelemetryEnd records session.end (no-op without consent).
func (i *Instance) RecordTelemetryEnd(kind telemetry.EndKind) {
	i.RecordTelemetryEndFrom(kind, "")
}

// RecordTelemetryEndFrom records session.end from surface sf ("" = the
// process surface), for web requests served by a TUI process.
func (i *Instance) RecordTelemetryEndFrom(kind telemetry.EndKind, sf telemetry.Surface) {
	var lifetime time.Duration
	if !i.CreatedAt.IsZero() {
		lifetime = time.Since(i.CreatedAt)
	}
	telemetry.SessionEnded(telemetry.SessionEndInfo{
		Tool:      i.Tool,
		Kind:      kind,
		Lifetime:  lifetime,
		SessionID: i.ID,
		Surface:   sf,
	})
}

// recordTelemetryStartError records error area=session_start for a failed
// start or spawn read-back (no-op without consent, or when err is nil). Only
// the error class and the normalised tool are recorded.
func (i *Instance) recordTelemetryStartError(err error) {
	if err == nil {
		return
	}
	telemetry.ErrorOccurred(telemetry.AreaSessionStart, startErrKind(err), i.Tool)
}

// startErrKind classifies a start failure; a spawn whose tool was not on
// PATH is tool_not_found.
func startErrKind(err error) telemetry.ErrKind {
	var spawn *SpawnFailedError
	if errors.As(err, &spawn) && spawn.Record.IsToolNotFound() {
		return telemetry.KindToolNotFound
	}
	return telemetry.ErrKindOf(err)
}

// recordTelemetryMCPError records error area=mcp for a failed MCP attach or
// detach write (no-op without consent, or when err is nil).
func (i *Instance) recordTelemetryMCPError(err error) {
	if err != nil {
		telemetry.ErrorOccurred(telemetry.AreaMCP, telemetry.ErrKindOf(err), i.Tool)
	}
}
