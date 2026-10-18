package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// checkArchivedClaudeIDConflict returns the (message, code) pair to report
// when an ARCHIVED record already holds the Claude conversation id about to be
// bound to inst, and ("", "") otherwise.
//
// Issue #2683: the duplicate-id guard keeps the id on the older record, so an
// archived holder silently won. `add --resume-session` started a fresh
// conversation instead, and `session set ... claude-session-id` reported
// success and then reverted. Refusing up front, naming the holder and how to
// free the id, replaces that silent override.
func checkArchivedClaudeIDConflict(instances []*session.Instance, inst *session.Instance, sessionID string) (string, string) {
	holder := session.ArchivedClaudeSessionIDHolder(instances, inst, sessionID)
	if holder == nil {
		return "", ""
	}
	return fmt.Sprintf("Claude session %s is already held by archived session %q (id: %s); "+
		"unarchive it (agent-deck session unarchive %s) or delete it (agent-deck remove %s), "+
		"or move the id off it first (agent-deck session set %s claude-session-id <other>)",
		sessionID, holder.Title, holder.ID, holder.ID, holder.ID, holder.ID), ErrCodeAlreadyExists
}
