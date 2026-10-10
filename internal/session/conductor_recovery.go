package session

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/send"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

// One reconciliation turn, not a replay of previous user messages or inbox
// records. The conductor retains responsibility for checking durable receipts
// before it resumes any side effect.
const conductorRecoveryMessage = "[CONDUCTOR RECOVERY] Run one bounded recovery turn: drain your durable inbox with agent-deck inbox drain self --json; read ./state.json and saved task/handoff records; reconcile them with live child sessions and external completion receipts. Continue only already-authorized unfinished work. Do not repeat completed actions, resend delivered messages, or relaunch work already running. Preserve completion/dedup records. Report blockers; if nothing actionable remains, wait."

const conductorRecoveryReadyTimeout = 60 * time.Second

// Trim terminal padding, not meaningful rows: small fresh prompts can be
// followed by more blank rows than the shared composer scan window.
type conductorRecoveryTarget struct{ *tmux.Session }

func (t conductorRecoveryTarget) CapturePaneFresh() (string, error) {
	raw, err := t.Session.CapturePaneFresh()
	return strings.TrimRight(raw, " \t\r\n"), err
}

// ConductorRecoveryWarning reports a recovery failure separately from a
// successful process spawn, so callers still persist the new lifecycle state.
func (i *Instance) ConductorRecoveryWarning() string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	return i.conductorRecoveryWarning
}

func (i *Instance) recoverConductorAfterSpawn() {
	err := i.wakeConductorAfterSpawn()
	warning := ""
	if err != nil {
		warning = err.Error()
	}
	i.mu.Lock()
	i.conductorRecoveryWarning = warning
	i.mu.Unlock()
	if err != nil {
		sessionLog.Warn("conductor_recovery_incomplete", slog.String("instance_id", i.ID), slog.String("reason", warning))
	}
}

// Synchronous under the spawn lock: no CLI-exit race or second generation
// replacing this target during delivery. No inbox records are consumed here.
func (i *Instance) wakeConductorAfterSpawn() error {
	if !i.IsConductor {
		return nil
	}
	if !conductorRecoveryTurnEnabled() {
		sessionLog.Info("conductor_recovery_disabled", slog.String("instance_id", i.ID))
		return nil
	}
	if !IsClaudeCompatible(i.Tool) && !IsCodexCompatible(i.Tool) {
		return fmt.Errorf("conductor process started but recovery was not sent: %s has no verified empty-composer guard", i.Tool)
	}
	if i.tmuxSession == nil {
		return fmt.Errorf("conductor process started but recovery target is unavailable")
	}
	lock, err := AcquireSendLock(i.ID, 5*time.Second)
	if err != nil {
		return fmt.Errorf("conductor process started but recovery send lock failed: %w", err)
	}
	defer lock.Release()
	target := conductorRecoveryTarget{i.tmuxSession}
	if err := waitForConductorRecoveryPrompt(target, i.Tool, conductorRecoveryReadyTimeout); err != nil {
		return fmt.Errorf("conductor process started but recovery was not sent: %w", err)
	}
	// Recheck the pane immediately before input. Never clear a draft or send an
	// interrupt, and never retry an uncertain submission of this recovery turn.
	guard := send.GuardComposerDraft(target, send.ComposerGuardOptions{Strip: tmux.StripANSI})
	if guard.Refused {
		return fmt.Errorf("conductor process started but recovery was not sent: composer occupied or unreadable")
	}
	raw, err := target.CapturePaneFresh()
	if err != nil || !conductorRecoveryPromptSafe(i.Tool, raw) {
		return fmt.Errorf("conductor process started but recovery was not sent: no safe empty composer")
	}
	// Verify the complete bounded body BEFORE Enter. If a remount swallows
	// bytes, leave the draft untouched and report uncertainty, never retry it.
	message := conductorRecoveryMessage + " Recovery token: " + GenerateID() + "."
	err = i.tmuxSession.SendKeysAndEnterChecked(message, target.CapturePaneFresh,
		func(raw string, captureErr error) (bool, error) {
			if captureErr != nil {
				return false, captureErr
			}
			compact := func(s string) string { return strings.Join(strings.Fields(tmux.StripANSI(s)), "") }
			return strings.Contains(compact(raw), compact(message)), nil
		})
	if err != nil {
		return fmt.Errorf("conductor process started but recovery submission is uncertain (not retried): %w", err)
	}
	// The unique body was observed intact before Enter. An empty composer
	// now proves it left the composer. A blank/missing pane or
	// a lingering draft is unknown, never success and never an Enter retry.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		raw, err := target.CapturePaneFresh()
		if err != nil {
			continue
		}
		draft, visible := send.ComposerDraft(raw, tmux.StripANSI)
		if visible && draft == "" {
			return nil
		}
	}
	return fmt.Errorf("conductor process started but recovery submission is uncertain (not retried): composer did not clear")
}

// conductorRecoveryTurnEnabled reads [conductor].recovery_turn. An unreadable
// config keeps the default (on), matching how the rest of the conductor
// settings fall back.
func conductorRecoveryTurnEnabled() bool {
	cfg, err := LoadUserConfig()
	if err != nil || cfg == nil {
		return true
	}
	return cfg.Conductor.RecoveryTurnEnabled()
}

func conductorRecoveryPromptSafe(tool, raw string) bool {
	draft, visible := send.ComposerDraft(raw, tmux.StripANSI)
	if !visible || draft != "" {
		return false
	}
	if IsClaudeCompatible(tool) {
		tool = "claude"
	}
	if IsCodexCompatible(tool) {
		tool = "codex"
	}
	detector := tmux.NewPromptDetector(tool)
	content := tmux.StripANSI(raw)
	if !detector.HasPrompt(content) {
		return false
	}
	switch detector.ClassifySubstate(content) {
	case tmux.SubstateNone, tmux.SubstateIdleAtEmptyPrompt:
		return true
	default:
		return false
	}
}

// A wall-clock deadline (rather than a poll-count budget) also bounds slow
// tmux captures. Require three consecutive fresh empty frames after startup.
func waitForConductorRecoveryPrompt(target send.ComposerGuardTarget, tool string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	ready := 0
	for time.Now().Before(deadline) {
		raw, err := target.CapturePaneFresh()
		if err == nil {
			if draft, visible := send.ComposerDraft(raw, tmux.StripANSI); visible && draft != "" {
				return fmt.Errorf("composer occupied")
			}
			detectorTool := tool
			if IsClaudeCompatible(tool) {
				detectorTool = "claude"
			}
			if IsCodexCompatible(tool) {
				detectorTool = "codex"
			}
			if tmux.NewPromptDetector(detectorTool).ClassifySubstate(tmux.StripANSI(raw)) == tmux.SubstateInteractiveMenu {
				return fmt.Errorf("interactive menu requires operator input")
			}
			if conductorRecoveryPromptSafe(tool, raw) {
				ready++
			} else {
				ready = 0
			}
			if ready >= 3 {
				return nil
			}
		} else {
			ready = 0
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("no safe empty composer within %s", timeout)
}
