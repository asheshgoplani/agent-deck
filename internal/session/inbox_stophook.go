package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

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

// StopHookDecision mirrors the Claude Code Stop-hook JSON contract. Decision
// "block" keeps the turn alive and feeds Reason back as the next turn's input.
type StopHookDecision struct {
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

func stopBlocksDir() string {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return filepath.Join(os.TempDir(), ".agent-deck", "runtime", "stop-blocks")
	}
	return filepath.Join(dir, "runtime", "stop-blocks")
}

func stopBlocksPathFor(instanceID string) string {
	return filepath.Join(stopBlocksDir(), sanitizeInboxName(instanceID)+".json")
}

type stopBlockState struct {
	Count int `json:"count"`
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

func saveStopBlockCountLocked(instanceID string, count int) {
	path := stopBlocksPathFor(instanceID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	data, _ := json.Marshal(stopBlockState{Count: count})
	tmp := path + ".tmp"
	if os.WriteFile(tmp, data, 0o644) == nil {
		_ = os.Rename(tmp, path)
	}
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
	if strings.TrimSpace(instanceID) == "" {
		return StopHookDecision{}, false, nil
	}

	stopBlockMu.Lock()
	defer stopBlockMu.Unlock()

	count := loadStopBlockCountLocked(instanceID)
	if !stopHookActive {
		// Fresh user turn: reset the consecutive-block budget.
		count = 0
	}

	// Budget exhausted: stop blocking so the conductor can reach idle. Leave any
	// pending records untouched for the heartbeat to drain.
	if count >= MaxStopHookBlocks {
		saveStopBlockCountLocked(instanceID, count)
		return StopHookDecision{}, false, nil
	}

	events, err := DrainInboxForParent(instanceID)
	if err != nil {
		return StopHookDecision{}, false, err
	}
	if len(events) == 0 {
		// Nothing to inject — let the conductor go idle and reset the budget.
		saveStopBlockCountLocked(instanceID, 0)
		return StopHookDecision{}, false, nil
	}

	saveStopBlockCountLocked(instanceID, count+1)
	return StopHookDecision{
		Decision: "block",
		Reason:   FormatCompletionsForInjection(events),
	}, true, nil
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
