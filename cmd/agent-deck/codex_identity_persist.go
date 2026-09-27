package main

import (
	"fmt"
	"os"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// codexLaunchIdentityWait bounds how long launch waits for a fresh Codex
// process to hold its thread open. A launch turn has already started by then;
// a composer without one may take longer, and its first send hydrates it.
const codexLaunchIdentityWait = 3 * time.Second

// persistLiveCodexIdentity writes the thread the pane's live Codex process
// holds open (its rollout or thread writer lock) as the instance's Codex
// identity, with the targeted binding write that touches no other column.
// It waits up to wait for that evidence and returns the id and detection time,
// or "" when there is none. It does not mutate inst, so launch can call it
// while startup detection still runs; callers that keep inst adopt the result.
//
// Launch never persisted this binding for Codex (#2396, #2400): the row
// stayed unbound until a follow-up send, output fell back to pane text, and
// archive killed the only evidence of the identity.
func persistLiveCodexIdentity(storage *session.Storage, inst *session.Instance, wait time.Duration) (string, time.Time) {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) || storage == nil || storage.GetDB() == nil {
		return "", time.Time{}
	}
	deadline := time.Now().Add(wait)
	id := inst.LiveCodexThreadID()
	for id == "" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		id = inst.LiveCodexThreadID()
	}
	if id == "" {
		return "", time.Time{}
	}
	detectedAt := time.Now()
	if err := storage.GetDB().WriteCodexSessionBinding(inst.ID, id, detectedAt); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not persist Codex session identity: %v\n", err)
		return "", time.Time{}
	}
	if tmuxSess := inst.GetTmuxSession(); tmuxSess != nil && tmuxSess.Exists() {
		_ = tmuxSess.SetEnvironment("CODEX_SESSION_ID", id)
	}
	return id, detectedAt
}

// adoptLiveCodexIdentity binds an unbound Codex instance to its live thread,
// persisting it first. Bound instances and other tools are left alone.
func adoptLiveCodexIdentity(storage *session.Storage, inst *session.Instance) {
	if inst == nil || !session.IsCodexCompatible(inst.Tool) || inst.CodexSessionID != "" {
		return
	}
	if id, detectedAt := persistLiveCodexIdentity(storage, inst, 0); id != "" {
		inst.CodexSessionID, inst.CodexDetectedAt = id, detectedAt
	}
}
