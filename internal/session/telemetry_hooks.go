package session

import (
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
