package session

import (
	"errors"
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
	if i.skipRecoveryTurn {
		sessionLog.Info("conductor_recovery_skipped", slog.String("instance_id", i.ID), slog.String("reason", "idle build refresh"))
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
	// interrupt.
	guard := send.GuardComposerDraft(target, send.ComposerGuardOptions{Strip: tmux.StripANSI})
	if guard.Refused {
		return fmt.Errorf("conductor process started but recovery was not sent: composer occupied or unreadable")
	}
	raw, err := target.CapturePaneFresh()
	if err != nil || !conductorRecoveryPromptSafe(i.Tool, raw) {
		return fmt.Errorf("conductor process started but recovery was not sent: no safe empty composer")
	}
	// Verify the complete bounded body BEFORE Enter. If a remount swallows
	// bytes, leave the draft untouched and report uncertainty, never retype it.
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
	return confirmConductorRecoverySubmitted(target, message)
}

// Bounds for confirming the recovery turn left the composer. Variables so
// tests can shorten them.
var (
	conductorRecoveryClearWindow = 3 * time.Second
	conductorRecoveryClearPoll   = 100 * time.Millisecond
	// One entry per extra Enter: at most 2 more after the first.
	conductorRecoveryEnterBackoff = []time.Duration{500 * time.Millisecond, time.Second}
)

// conductorRecoverySubmitTarget is the pane surface the submit confirmation
// needs. conductorRecoveryTarget satisfies it.
type conductorRecoverySubmitTarget interface {
	CapturePaneFresh() (string, error)
	SendEnterChecked(before func() error) error
}

type conductorRecoveryComposerState int

const (
	recoveryComposerUnreadable conductorRecoveryComposerState = iota
	recoveryComposerEmpty
	recoveryComposerHoldsBody
	recoveryComposerOther
)

// confirmConductorRecoverySubmitted waits for the verified recovery body to
// leave the composer (issue #3062). An empty composer proves it was submitted
// or queued. If the composer still holds EXACTLY the verified body after the
// clear window, the Enter was not acted on: the body carries a unique token,
// so it cannot be an operator draft, and pressing Enter again cannot submit
// it twice. Enter is resent at most len(conductorRecoveryEnterBackoff)
// times. An unreadable composer, or one holding anything else, stops at once
// and is reported as uncertain.
func confirmConductorRecoverySubmitted(target conductorRecoverySubmitTarget, message string) error {
	for attempt := 0; ; attempt++ {
		deadline := time.Now().Add(conductorRecoveryClearWindow)
		for time.Now().Before(deadline) {
			time.Sleep(conductorRecoveryClearPoll)
			raw, err := target.CapturePaneFresh()
			if classifyConductorRecoveryComposer(raw, err, message) == recoveryComposerEmpty {
				return nil
			}
		}
		raw, err := target.CapturePaneFresh()
		switch classifyConductorRecoveryComposer(raw, err, message) {
		case recoveryComposerEmpty:
			return nil
		case recoveryComposerUnreadable:
			return fmt.Errorf("conductor process started but recovery submission is uncertain (not retried): composer unreadable")
		case recoveryComposerOther:
			return fmt.Errorf("conductor process started but recovery submission is uncertain (not retried): composer holds other input")
		}
		if attempt >= len(conductorRecoveryEnterBackoff) {
			return fmt.Errorf("conductor process started but recovery submission is uncertain: composer did not clear after %d Enter attempts", attempt+1)
		}
		time.Sleep(conductorRecoveryEnterBackoff[attempt])
		cleared := false
		err = target.SendEnterChecked(func() error {
			raw, err := target.CapturePaneFresh()
			switch classifyConductorRecoveryComposer(raw, err, message) {
			case recoveryComposerHoldsBody:
				return nil
			case recoveryComposerEmpty:
				cleared = true
				return errConductorRecoveryCleared
			default:
				return fmt.Errorf("composer changed before Enter retry")
			}
		})
		if cleared {
			return nil
		}
		if err != nil {
			return fmt.Errorf("conductor process started but recovery submission is uncertain (retry stopped): %w", err)
		}
		sessionLog.Info("conductor_recovery_enter_retried", slog.Int("attempt", attempt+2))
	}
}

var errConductorRecoveryCleared = errors.New("composer cleared")

// classifyConductorRecoveryComposer reads one fresh frame. Only a composer
// whose whole content is the verified recovery body is eligible for another
// Enter.
func classifyConductorRecoveryComposer(raw string, captureErr error, message string) conductorRecoveryComposerState {
	if captureErr != nil {
		return recoveryComposerUnreadable
	}
	draft, visible := send.ComposerDraft(raw, tmux.StripANSI)
	if !visible {
		return recoveryComposerUnreadable
	}
	if draft == "" {
		return recoveryComposerEmpty
	}
	if conductorRecoveryComposerIsBody(tmux.StripANSI(raw), draft, message) {
		return recoveryComposerHoldsBody
	}
	return recoveryComposerOther
}

// conductorRecoveryComposerIsBody compares whitespace-insensitively, since
// the pane wraps the long body. A framed composer yields the whole body as
// the draft. Without a frame the draft is only the prompt line, so the text
// from that line to the bottom of the pane must be the body and nothing else.
func conductorRecoveryComposerIsBody(content, draft, message string) bool {
	compact := func(s string) string { return strings.Join(strings.Fields(s), "") }
	want := compact(message)
	if compact(draft) == want {
		return true
	}
	lines := strings.Split(content, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimLeft(lines[i], " \t")
		for _, marker := range []string{"❯", "›"} {
			if strings.HasPrefix(trimmed, marker) {
				tail := trimmed[len(marker):] + "\n" + strings.Join(lines[i+1:], "\n")
				return compact(tail) == want
			}
		}
	}
	return false
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
