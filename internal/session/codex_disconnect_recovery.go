package session

import (
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

const (
	codexDisconnectMarker    = "stream disconnected before completion"
	codexResponsesEndpoint   = "backend-api/codex/responses"
	codexRecoveryWindow      = 6 * time.Hour
	codexRecoveryMaxAttempts = 2
)

// CodexDisconnectRecoveryAction describes what a single scan decided. It is
// intentionally small so callers can observe recovery without retaining pane
// contents, which can contain user prompts and agent output.
type CodexDisconnectRecoveryAction string

const (
	CodexDisconnectRecoveryPending   CodexDisconnectRecoveryAction = "pending_confirmation"
	CodexDisconnectRecoveryRestarted CodexDisconnectRecoveryAction = "restarted"
	CodexDisconnectRecoveryCapped    CodexDisconnectRecoveryAction = "attempt_cap_reached"
	CodexDisconnectRecoveryFailed    CodexDisconnectRecoveryAction = "restart_failed"
)

// CodexDisconnectRecoveryOutcome is emitted for a confirmation, resume, or
// cap decision. It never includes captured pane contents.
type CodexDisconnectRecoveryOutcome struct {
	InstanceID string
	Action     CodexDisconnectRecoveryAction
	Err        error
}

type codexDisconnectObservation struct {
	fingerprint [sha256.Size]byte
	observedAt  time.Time
}

// CodexDisconnectRecovery is a process-local coordinator used only by the
// interactive TUI. It deliberately invokes Instance.Restart, which keeps the
// persisted Codex rollout and resumes the same conversation.
type CodexDisconnectRecovery struct {
	mu       sync.Mutex
	now      func() time.Time
	capture  func(*Instance) (string, error)
	restart  func(*Instance) error
	pending  map[string]codexDisconnectObservation
	attempts map[string][]time.Time
}

func NewCodexDisconnectRecovery() *CodexDisconnectRecovery {
	return &CodexDisconnectRecovery{
		now: time.Now,
		capture: func(inst *Instance) (string, error) {
			if pane := inst.GetTmuxSession(); pane != nil {
				return pane.CapturePaneFresh()
			}
			return "", fmt.Errorf("tmux session unavailable")
		},
		restart:  func(inst *Instance) error { return inst.Restart() },
		pending:  make(map[string]codexDisconnectObservation),
		attempts: make(map[string][]time.Time),
	}
}

// IsCodexStreamDisconnectedPane accepts only Codex's rendered error banner.
// Requiring its bullet prefix and backend endpoint excludes normal output,
// prompts which quote the words, and unrelated network failures.
func IsCodexStreamDisconnectedPane(content string) bool {
	lines := strings.Split(tmux.StripANSI(content), "\n")
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	start := len(lines) - 16
	if start < 0 {
		start = 0
	}
	for i := start; i < len(lines); i++ {
		line := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(line, "■") || !strings.Contains(strings.ToLower(line), codexDisconnectMarker) {
			continue
		}
		for j := maxInt(start, i-1); j <= minInt(len(lines)-1, i+3); j++ {
			if strings.Contains(strings.ToLower(lines[j]), codexResponsesEndpoint) {
				return true
			}
		}
	}
	return false
}

// Scan performs one non-blocking recovery pass. A failure must be visible in
// two consecutive unchanged pane captures before the existing safe resume path
// is used. Callers should invoke it at a coarse cadence (the TUI uses 60s).
func (r *CodexDisconnectRecovery) Scan(instances []*Instance) []CodexDisconnectRecoveryOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.now()
	outcomes := make([]CodexDisconnectRecoveryOutcome, 0)
	for _, inst := range instances {
		key, ok := resumableCodexRecoveryKey(inst)
		if !ok {
			continue
		}

		pane, err := r.capture(inst)
		if err != nil || !IsCodexStreamDisconnectedPane(pane) {
			delete(r.pending, key)
			continue
		}

		fingerprint := sha256.Sum256([]byte(tmux.StripANSI(pane)))
		previous, confirmed := r.pending[key]
		if !confirmed || previous.fingerprint != fingerprint {
			r.pending[key] = codexDisconnectObservation{fingerprint: fingerprint, observedAt: now}
			outcomes = append(outcomes, CodexDisconnectRecoveryOutcome{InstanceID: inst.ID, Action: CodexDisconnectRecoveryPending})
			continue
		}
		delete(r.pending, key)

		r.attempts[key] = recentCodexRecoveryAttempts(r.attempts[key], now)
		if len(r.attempts[key]) >= codexRecoveryMaxAttempts {
			sessionLog.Warn("codex_stream_disconnect_recovery_capped", slog.String("instance_id", inst.ID))
			outcomes = append(outcomes, CodexDisconnectRecoveryOutcome{InstanceID: inst.ID, Action: CodexDisconnectRecoveryCapped})
			continue
		}

		r.attempts[key] = append(r.attempts[key], now)
		if err := r.restart(inst); err != nil {
			sessionLog.Warn("codex_stream_disconnect_recovery_failed", slog.String("instance_id", inst.ID), slog.String("error", err.Error()))
			outcomes = append(outcomes, CodexDisconnectRecoveryOutcome{InstanceID: inst.ID, Action: CodexDisconnectRecoveryFailed, Err: err})
			continue
		}
		sessionLog.Info("codex_stream_disconnect_recovered", slog.String("instance_id", inst.ID))
		outcomes = append(outcomes, CodexDisconnectRecoveryOutcome{InstanceID: inst.ID, Action: CodexDisconnectRecoveryRestarted})
	}
	return outcomes
}

func resumableCodexRecoveryKey(inst *Instance) (string, bool) {
	if inst == nil || !IsCodexCompatible(inst.Tool) {
		return "", false
	}
	status := inst.GetStatusThreadSafe()
	if status == StatusStopped || status == StatusRunning || status == StatusStarting {
		return "", false
	}
	sessionID, err := normalizeToolSessionID(FieldCodexSessionID, inst.CodexSessionID)
	if err != nil || sessionID == "" || sessionID != strings.TrimSpace(inst.CodexSessionID) {
		return "", false
	}
	if !codexRolloutExistsInHome(sessionID, inst.getCodexHomeDir()) {
		return "", false
	}
	return inst.ID + ":" + sessionID, true
}

func recentCodexRecoveryAttempts(attempts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-codexRecoveryWindow)
	kept := attempts[:0]
	for _, attempt := range attempts {
		if !attempt.Before(cutoff) {
			kept = append(kept, attempt)
		}
	}
	return kept
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
