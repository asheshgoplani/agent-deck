package session

import (
	"testing"
	"time"
)

func TestCodexHookRejectionPreservesCompletionInvalidation(t *testing.T) {
	inst, codexHome := newCodexGateInstance(t)
	root, child := uniqueSID(t), uniqueSID(t)
	seedCodexRolloutWithMeta(t, codexHome, root, "user", "", false)
	seedCodexRolloutWithMeta(t, codexHome, child, "subagent", root, true)
	inst.CodexSessionID = root
	parentGeneration := root + ":parent-turn"
	inst.codexStartedGeneration, inst.codexCompletedGeneration = parentGeneration, parentGeneration
	inst.codexStartedSessionID, inst.codexCompletedSessionID = root, root
	inst.codexInvalidatingGeneration = parentGeneration
	inst.hookStatus, inst.hookSessionID = "running", root
	if inst.codexCompletionConverged() {
		t.Fatal("parent completion must remain invalid while its durable invalidation is pending")
	}

	inst.UpdateHookStatus(&HookStatus{
		Status: "waiting", SessionID: child, Event: "agent-turn-complete", UpdatedAt: time.Now(),
		CodexStartedGeneration: child + ":child-turn", CodexCompletedGeneration: child + ":child-turn",
		CodexStartedSessionID: child, CodexCompletedSessionID: child,
	})
	if inst.CodexSessionID != root || inst.hookStatus != "running" || inst.hookSessionID != root {
		t.Fatalf("rejected child replaced parent hook: binding=%q status=%q cached=%q", inst.CodexSessionID, inst.hookStatus, inst.hookSessionID)
	}
	if inst.codexInvalidatingGeneration != parentGeneration || inst.codexCompletionConverged() {
		t.Fatal("rejected child revived parent completion evidence pending invalidation")
	}
}
