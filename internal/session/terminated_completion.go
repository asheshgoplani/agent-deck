package session

import (
	"fmt"
	"time"

	"al.essio.dev/pkg/shellescape"
)

const terminatedCompletionMaxAge = 30 * time.Second

func completionHookTool(tool string) bool {
	return IsClaudeCompatible(tool) || IsCodexCompatible(tool)
}

// Publish before a start/resume can signal the old process. A remote hook
// directory is not local authority, so SSH sessions retain the old fallback.
func (i *Instance) seedCompletionLaunch() error {
	if !completionHookTool(i.Tool) || i.IsSSH() {
		return nil
	}
	// Veto in-flight probes before durable publication can block on a hook writer.
	i.spawnGen.Add(1)
	if _, err := i.seedHookGeneration("starting", false); err != nil {
		return fmt.Errorf("seed completion launch: %w", err)
	}
	return nil
}

func (i *Instance) bindCompletionLaunchCommand(command string) string {
	i.mu.RLock()
	generation := i.hookLaunchGeneration
	i.mu.RUnlock()
	if completionHookTool(i.Tool) && generation != "" && !i.IsSSH() {
		return "export AGENTDECK_HOOK_GENERATION=" + shellescape.Quote(generation) + "; " + command
	}
	return command
}

func (i *Instance) completionSessionIDLocked() string {
	if IsClaudeCompatible(i.Tool) {
		return i.ClaudeSessionID
	}
	if IsCodexCompatible(i.Tool) {
		return i.CodexSessionID
	}
	return i.GenericSessionID
}

// Ordinary Stop/waiting is never completion proof. Both launch and
// conversation identities are mandatory, including after observer reload.
func validTerminatedCompletion(hook *HookStatus, tool, generation, sessionID string, started, now time.Time) bool {
	if hook == nil || !hook.TimestampKnown || generation == "" || sessionID == "" || started.IsZero() ||
		hook.HookGeneration != generation || hook.SessionID != sessionID || hook.Sequence == 0 ||
		hook.DoneStatus != "ok" || hook.Status != "waiting" {
		return false
	}
	// A newly invoked Stop must not refresh a prior launch's transcript sentinel.
	if IsClaudeCompatible(tool) && (hook.DoneAt.IsZero() || hook.DoneAt.Before(started) || hook.DoneAt.After(now)) {
		return false
	}
	if IsCodexCompatible(tool) && (hook.codexCompletionConsumed || hook.CodexStartedGeneration == "" ||
		hook.CodexStartedGeneration != hook.CodexCompletedGeneration || hook.CodexStartedSessionID != sessionID || hook.CodexCompletedSessionID != sessionID) {
		return false
	}
	return !hook.UpdatedAt.Before(started) && !hook.UpdatedAt.After(now) && now.Sub(hook.UpdatedAt) <= terminatedCompletionMaxAge
}
