package session

import (
	"fmt"
	"strings"
	"time"
)

// Issue #2481: a finished worker's leftover scheduled checks (/loop wakeups,
// background task notifications) re-print its identical completion sentinel
// as new transcript turns, and each one used to become an urgent finished
// record that woke the parent. The completion ledger is the durable "last
// delivered done" per child, so repeats are recognised and counted there.

// doneRepeatWindow is how long after a delivered completion an identical one
// (same status and summary) from a background turn is a repeat. Past it the
// identical completion is delivered once more, so a worker stuck re-asserting
// the same result is still surfaced, at most once per window.
const doneRepeatWindow = time.Hour

// checkDoneRepeat reports whether a completion about to be delivered repeats
// the child's last delivered one (same status and summary on the ledger
// entry) and, if so, counts it there. It is a repeat when it is either
//   - the delivered transcript turn seen again, at any age; or
//   - a background turn (no person or parent send started it, no held send
//     answered by it) within doneRepeatWindow of the delivery. A turn a
//     person or a send started is news (the #2469 rule), never a repeat.
//
// counted is false when the turn was already counted or is the delivered
// turn itself. Ledger errors fail open, so the completion is delivered.
func checkDoneRepeat(childID, profile string, sig DoneSignal, turnUUID, trigger, fromID string, at time.Time) (repeat, counted bool) {
	prev, ok := ReadLedgerEntry(childID)
	if !ok {
		return false, false
	}
	if p := strings.TrimSpace(prev.Profile); p != "" && profile != "" && p != profile {
		return false, false
	}
	if !strings.EqualFold(strings.TrimSpace(prev.Status), sig.Status) || strings.TrimSpace(prev.Summary) != strings.TrimSpace(sig.Summary) {
		return false, false
	}
	if turnUUID != "" && prev.TurnUUID == turnUUID {
		return true, false
	}
	background := strings.TrimSpace(fromID) == "" && trigger != TurnTriggerHuman && trigger != TurnTriggerSend
	if !background || at.Sub(prev.FinishedAt) >= doneRepeatWindow {
		return false, false
	}
	if turnUUID != "" && prev.LastRepeatUUID == turnUUID {
		return true, false
	}
	prev.Repeats++
	prev.LastRepeatAt = at
	prev.LastRepeatUUID = turnUUID
	if err := WriteLedgerEntry(prev); err != nil {
		return false, false
	}
	return true, true
}

// DisplaySummary is the ledger entry's summary as the CLI and the TUI show
// it: the summary (or "reported <status>" when the worker gave none), plus
// the number of identical repeats that were counted instead of delivered.
func (e CompletionLedgerEntry) DisplaySummary() string {
	summary := e.Summary
	if summary == "" {
		// A ledger entry without a summary still tells us the turn ended and
		// how. Say that, rather than printing a bare status word that reads
		// like a description of the work.
		summary = "reported " + e.Status
	}
	if e.Repeats > 0 {
		summary += fmt.Sprintf(" (repeated %dx, not delivered)", e.Repeats)
	}
	return summary
}
