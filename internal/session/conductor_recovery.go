package session

import (
	"fmt"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// One reconciliation turn, not a replay of previous user messages or inbox
// records. The conductor retains responsibility for checking durable receipts
// before it resumes any side effect.
const conductorRecoveryMessage = "[CONDUCTOR RECOVERY] Run one bounded recovery turn: drain your durable inbox with agent-deck inbox drain self --json; read ./state.json and saved task/handoff records; reconcile them with live child sessions and external completion receipts. Continue only already-authorized unfinished work. Do not repeat completed actions, resend delivered messages, or relaunch work already running. Preserve completion/dedup records. Report blockers; if nothing actionable remains, wait."

const conductorRecoveryReadyTimeout = 20 * time.Second

// wakeConductorAfterSpawn is synchronous so short-lived CLI callers cannot
// exit before delivery. It runs under the spawn lock, after a successful real
// spawn only. The target send lock also excludes heartbeat and inbox writers.
type conductorRecoveryTarget struct{ *tmux.Session }

func (t conductorRecoveryTarget) CapturePaneFresh() (string, error) {
	raw, err := t.Session.CapturePaneFresh()
	return strings.TrimRight(raw, " \t\r\n"), err
}

func (i *Instance) wakeConductorAfterSpawn() error {
	if !i.IsConductor {
		return nil
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("conductor process started but recovery target is unavailable")
	}
	lock, err := AcquireSendLock(i.ID, 2*time.Second)
	if err != nil {
		return fmt.Errorf("conductor process started but recovery send lock failed: %w", err)
	}
	defer lock.Release()
	target := conductorRecoveryTarget{i.tmuxSession}
	if err := send.WaitForAgentReady(target, i.Tool, conductorRecoveryReadyTimeout, send.PromptGates{
		ClaudeComposer: IsClaudeCompatible(i.Tool), CodexPrompt: IsCodexCompatible(i.Tool),
	}); err != nil {
		return fmt.Errorf("conductor process started but recovery was not sent: %w", err)
	}
	// Recheck the pane immediately before input. Never clear a draft or send an
	// interrupt, and never retry an uncertain submission of this recovery turn.
	guard := send.GuardComposerDraft(target, send.ComposerGuardOptions{Strip: tmux.StripANSI})
	if guard.Refused {
		return fmt.Errorf("conductor process started but recovery was not sent: composer occupied or unreadable")
	}
	status, err := i.tmuxSession.GetStatus()
	if err != nil || (status != "waiting" && status != "idle" && status != "starting") {
		return fmt.Errorf("conductor process started but recovery was not sent: target is not ready")
	}
	if err := i.tmuxSession.SendKeysAndEnter(conductorRecoveryMessage); err != nil {
		return fmt.Errorf("conductor process started but recovery submission is uncertain (not retried): %w", err)
	}
	return nil
}
