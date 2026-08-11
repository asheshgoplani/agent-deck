package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/asheshgoplani/agent-deck/internal/logging"
)

// commsLog is the shared logger for the issue #1225 durable-comms paths
// (inbox/outbox/stop-hook). Audit B4: error paths that were previously silent
// (stop-block persist, dead-letter missed-log) surface here so an operator can
// see a dropped completion or a broken loop-guard.
var commsLog = logging.ForComponent(logging.CompSession)

// Issue #1225 Step 3 — the busy-parent fix. A conductor's Stop hook drains the
// durable outbox and returns {decision:"block",reason} so the completions are
// injected as the conductor's next turn input, at the moment it is provably
// free. This is how a BUSY parent still receives every completion at its very
// next turn boundary, with zero forced interrupts and zero loss.
//
// Loop guard: blocking on Stop keeps the conductor alive for another turn. If a
// child finishes a new turn every cycle, naive "block whenever pending" would
// trap the conductor forever (Agent Teams #47930 token burn). We cap CONSECUTIVE
// stop-hook-induced blocks at MaxStopHookBlocks; once tripped we stop blocking
// and leave any new records for the heartbeat to drain, so the conductor can
// reach idle. A genuine user turn (stop_hook_active=false) resets the budget.

// MaxStopHookBlocks is the cap on consecutive stop-hook-induced blocks.
const MaxStopHookBlocks = 3

var stopBlockMu sync.Mutex
var stopBlockPersist = writeFileDurable
var stopHookAcknowledgeInbox = AcknowledgeInboxForParent
var stopHookAcknowledgeRuntime = AcknowledgeRuntimeQueue

func SetStopHookRuntimeAcknowledgerForTest(fn func(string, string) error) func() {
	previous := stopHookAcknowledgeRuntime
	stopHookAcknowledgeRuntime = fn
	return func() { stopHookAcknowledgeRuntime = previous }
}

// StopHookDecision mirrors the Claude Code Stop-hook JSON contract. Decision
// "block" keeps the turn alive and feeds Reason back as the next turn's input.
type StopHookDecision struct {
	Decision                 string `json:"decision,omitempty"`
	Reason                   string `json:"reason,omitempty"`
	InboxReason              string `json:"-"`
	InboxAckToken            string `json:"-"`
	RuntimeQueueAckToken     string `json:"-"`
	StopBlockCount           int    `json:"-"`
	StopBlockPrevious        int    `json:"-"`
	StopBlockToken           string `json:"-"`
	StopBlockResponseWritten bool   `json:"-"`
}

func stopBlocksDir() string {
	dir, err := runtimeDataPath("stop-blocks")
	if err != nil {
		return tempAgentDeckPath("runtime", "stop-blocks")
	}
	return dir
}

func stopBlocksPathFor(instanceID string) string {
	return filepath.Join(stopBlocksDir(), sanitizeInboxName(instanceID)+".json")
}

type stopBlockState struct {
	Count        int    `json:"count"`
	PendingCount int    `json:"pending_count,omitempty"`
	PendingToken string `json:"pending_token,omitempty"`
	InboxToken   string `json:"inbox_token,omitempty"`
	RuntimeToken string `json:"runtime_token,omitempty"`
	AckPhase     string `json:"ack_phase,omitempty"`
}

func loadStopBlockCountLocked(instanceID string) int {
	raw, err := os.ReadFile(stopBlocksPathFor(instanceID))
	if err != nil {
		return 0
	}
	var s stopBlockState
	if json.Unmarshal(raw, &s) != nil {
		return 0
	}
	return s.Count
}

// saveStopBlockCountLocked persists the consecutive-block counter durably.
//
// Audit B4: this MUST surface its error. A swallowed failure here is the most
// dangerous bug in the design — if the counter never persists,
// loadStopBlockCountLocked keeps returning 0, so every Stop blocks (0 < cap)
// forever, the exact token-burn loop the guard prevents. Callers fail safe on a
// non-nil error (do not block).
func saveStopBlockCountLocked(instanceID string, count int) error {
	return saveStopBlockStateLocked(instanceID, stopBlockState{Count: count})
}

func loadStopBlockStateLocked(instanceID string) stopBlockState {
	raw, err := os.ReadFile(stopBlocksPathFor(instanceID))
	if err != nil {
		return stopBlockState{}
	}
	var state stopBlockState
	_ = json.Unmarshal(raw, &state)
	return state
}

func saveStopBlockStateLocked(instanceID string, state stopBlockState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return stopBlockPersist(stopBlocksPathFor(instanceID), data, 0o644)
}

// DrainForStopHook implements the conductor Stop-hook contract for one instance.
// stopHookActive is Claude Code's flag: true means this Stop is a continuation
// induced by a previous block (so it counts against the budget); false is a
// genuine user turn boundary (resets the budget).
//
// Returns the decision to emit, whether it blocked, and any error. When the
// budget is exhausted it returns no-block WITHOUT draining, so pending records
// are preserved for the heartbeat path (never lost to the guard).
func DrainForStopHook(instanceID string, stopHookActive bool) (StopHookDecision, bool, error) {
	dec, blocked, err := StageForStopHook(instanceID, stopHookActive)
	if err != nil || !blocked {
		return dec, blocked, err
	}
	if err := acknowledgeStopHookWithoutRuntime(instanceID, dec); err != nil {
		if rbErr := RollbackStopHookDelivery(instanceID, dec.StopBlockToken); rbErr != nil {
			return StopHookDecision{}, false, fmt.Errorf("acknowledge inbox: %w; rollback reservation: %v", err, rbErr)
		}
		return StopHookDecision{}, false, err
	}
	dec.InboxAckToken = ""
	return dec, blocked, nil
}

func acknowledgeStopHookWithoutRuntime(instanceID string, dec StopHookDecision) error {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	state := loadStopBlockStateLocked(instanceID)
	if state.PendingToken != dec.StopBlockToken || state.PendingCount != dec.StopBlockCount {
		return errors.New("Stop-hook budget reservation token mismatch")
	}
	if dec.InboxAckToken != "" {
		if err := AcknowledgeInboxForParent(instanceID, dec.InboxAckToken); err != nil {
			return err
		}
	}
	return saveStopBlockStateLocked(instanceID, stopBlockState{Count: dec.StopBlockCount})
}

// StageForStopHook reserves the delivery without consuming its inbox records.
// The hook writer acknowledges it only after the complete response is written.
func StageForStopHook(instanceID string, stopHookActive bool) (StopHookDecision, bool, error) {
	if strings.TrimSpace(instanceID) == "" {
		return StopHookDecision{}, false, nil
	}

	// Audit B12 fast path + scope: a session with nothing pending — every leaf /
	// non-parent session — returns immediately with no block and ZERO ledger
	// writes. Only a completion target (a conductor/parent that children commit
	// to) ever has a pending inbox, so the global Stop-hook sync flip is inert
	// for non-conductor sessions. Cheap stat, no consume.
	if !InboxHasPending(instanceID) && !RuntimeQueueHasPending(instanceID) && !stopHookReservationPending(instanceID) {
		return StopHookDecision{}, false, nil
	}

	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()

	state := loadStopBlockStateLocked(instanceID)
	count := state.Count
	if !stopHookActive && state.PendingToken == "" {
		// Fresh user turn: reset the consecutive-block budget.
		count = 0
	}

	// Budget exhausted: stop blocking so the conductor can reach idle. Leave any
	// pending records untouched for the heartbeat to drain. The counter is
	// already at its persisted value, so no write is needed.
	if count >= MaxStopHookBlocks {
		return StopHookDecision{}, false, nil
	}
	if state.PendingToken != "" && state.AckPhase != "" && state.AckPhase != "prepared" {
		inboxReason := ""
		if state.InboxToken != "" {
			inboxReason = "reserved inbox delivery"
		}
		return StopHookDecision{
			Decision:                 "block",
			InboxReason:              inboxReason,
			InboxAckToken:            state.InboxToken,
			RuntimeQueueAckToken:     state.RuntimeToken,
			StopBlockCount:           state.PendingCount,
			StopBlockPrevious:        state.Count,
			StopBlockToken:           state.PendingToken,
			StopBlockResponseWritten: true,
		}, true, nil
	}
	reservationToken := state.PendingToken
	reservedCount := state.PendingCount
	if reservationToken == "" {
		reservationToken = runtimeQueueNewID()
		reservedCount = count + 1
		state = stopBlockState{Count: count, PendingCount: count + 1, PendingToken: reservationToken}
		if err := saveStopBlockStateLocked(instanceID, state); err != nil {
			return StopHookDecision{}, false, err
		}
	}

	batch, err := StageRuntimeQueue(instanceID)
	if err != nil {
		if rbErr := rollbackStopHookDeliveryLocked(instanceID, reservationToken); rbErr != nil {
			return StopHookDecision{}, false, fmt.Errorf("stage runtime queue: %w; rollback reservation: %v", err, rbErr)
		}
		return StopHookDecision{}, false, err
	}
	inboxBatch, err := StageInboxForParent(instanceID)
	if err != nil {
		if rbErr := rollbackStopHookDeliveryLocked(instanceID, reservationToken); rbErr != nil {
			return StopHookDecision{}, false, fmt.Errorf("stage inbox: %w; rollback reservation: %v", err, rbErr)
		}
		return StopHookDecision{}, false, err
	}
	events := inboxBatch.Events
	if len(events) == 0 && len(batch.Messages) == 0 {
		// Race: another drain (heartbeat) emptied the inbox between the peek and
		// here. No block; reset the budget to 0 — a non-blocking idle Stop breaks
		// the consecutive-block chain.
		if inboxBatch.Token != "" {
			if ackErr := AcknowledgeInboxForParent(instanceID, inboxBatch.Token); ackErr != nil {
				if rbErr := rollbackStopHookDeliveryLocked(instanceID, reservationToken); rbErr != nil {
					return StopHookDecision{}, false, fmt.Errorf("acknowledge empty inbox: %w; rollback reservation: %v", ackErr, rbErr)
				}
				return StopHookDecision{}, false, ackErr
			}
		}
		if rbErr := saveStopBlockStateLocked(instanceID, stopBlockState{}); rbErr != nil {
			return StopHookDecision{}, false, fmt.Errorf("reset empty Stop-hook reservation: %w", rbErr)
		}
		return StopHookDecision{}, false, nil
	}
	state.InboxToken = inboxBatch.Token
	state.RuntimeToken = batch.Token
	if state.AckPhase == "" {
		state.AckPhase = "prepared"
	}
	if err := saveStopBlockStateLocked(instanceID, state); err != nil {
		if rbErr := rollbackStopHookDeliveryLocked(instanceID, reservationToken); rbErr != nil {
			return StopHookDecision{}, false, fmt.Errorf("persist Stop-hook delivery tokens: %w; rollback reservation: %v", err, rbErr)
		}
		return StopHookDecision{}, false, fmt.Errorf("persist Stop-hook delivery tokens: %w", err)
	}

	var inboxReason string
	if len(events) > 0 {
		inboxReason = FormatCompletionsForInjection(events)
	}
	runtimeReason := FormatRuntimeMessagesForInjection(batch.Messages)
	reason := inboxReason
	if inboxReason != "" && runtimeReason != "" {
		reason = strings.TrimRight(inboxReason, "\n") + "\n\n" + runtimeReason
	} else if runtimeReason != "" {
		reason = runtimeReason
	}
	return StopHookDecision{
		Decision:                 "block",
		Reason:                   reason,
		InboxReason:              inboxReason,
		InboxAckToken:            inboxBatch.Token,
		RuntimeQueueAckToken:     batch.Token,
		StopBlockCount:           reservedCount,
		StopBlockPrevious:        count,
		StopBlockToken:           reservationToken,
		StopBlockResponseWritten: state.AckPhase == "response-written",
	}, true, nil
}

func stopHookReservationPending(instanceID string) bool {
	return loadStopBlockStateLocked(instanceID).PendingToken != ""
}

func MarkStopHookResponseWritten(instanceID, reservationToken string) error {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	state := loadStopBlockStateLocked(instanceID)
	if state.PendingToken != reservationToken {
		return errors.New("Stop-hook response token mismatch")
	}
	state.AckPhase = "response-written"
	return saveStopBlockStateLocked(instanceID, state)
}

// AcknowledgeStopHookDelivery commits inbox consumption and advances the loop
// budget only after the complete Stop-hook response was written.
func AcknowledgeStopHookDelivery(instanceID, inboxToken, runtimeToken, reservationToken string, count int) error {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	state := loadStopBlockStateLocked(instanceID)
	if reservationToken == "" || state.PendingToken != reservationToken || state.PendingCount != count || state.InboxToken != inboxToken || state.RuntimeToken != runtimeToken {
		return errors.New("Stop-hook budget reservation token mismatch")
	}
	if (state.AckPhase == "prepared" || state.AckPhase == "response-written") && inboxToken != "" {
		if err := stopHookAcknowledgeInbox(instanceID, inboxToken); err != nil {
			return err
		}
		state.AckPhase = "inbox-acknowledged"
		if err := saveStopBlockStateLocked(instanceID, state); err != nil {
			return err
		}
	}
	if state.AckPhase != "runtime-acknowledged" && runtimeToken != "" {
		if err := stopHookAcknowledgeRuntime(instanceID, runtimeToken); err != nil {
			return err
		}
		state.AckPhase = "runtime-acknowledged"
		if err := saveStopBlockStateLocked(instanceID, state); err != nil {
			return err
		}
	}
	return saveStopBlockStateLocked(instanceID, stopBlockState{Count: count})
}

func RollbackStopHookDelivery(instanceID, reservationToken string) error {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	return rollbackStopHookDeliveryLocked(instanceID, reservationToken)
}

func rollbackStopHookDeliveryLocked(instanceID, reservationToken string) error {
	state := loadStopBlockStateLocked(instanceID)
	if reservationToken == "" || state.PendingToken != reservationToken {
		return errors.New("Stop-hook budget rollback token mismatch")
	}
	return saveStopBlockStateLocked(instanceID, stopBlockState{Count: state.Count})
}

// FormatCompletionsForInjection renders drained completions as the human-
// readable reason injected into the conductor's next turn.
func FormatCompletionsForInjection(events []TransitionNotificationEvent) string {
	var b strings.Builder
	b.WriteString("Child session(s) completed while you were busy — handle each:\n")
	for _, ev := range events {
		status := ev.ToStatus
		if ev.Kind == transitionKindFinished && ev.DoneStatus != "" {
			status = ev.DoneStatus
		}
		title := ev.ChildTitle
		if title == "" {
			title = ev.ChildSessionID
		}
		line := fmt.Sprintf("- %s (%s): %s", title, ev.ChildSessionID, status)
		if ev.Kind == transitionKindFinished && ev.DoneSummary != "" {
			line += " — " + ev.DoneSummary
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return b.String()
}

// ResetStopBlockBudget clears an instance's consecutive-block counter. Used by
// rm_sweep on removal and available to tests.
func ResetStopBlockBudget(instanceID string) {
	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()
	_ = os.Remove(stopBlocksPathFor(instanceID))
}
