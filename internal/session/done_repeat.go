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
// entry) and, if count is set, counts it there. It is a repeat when it is
// either
//   - the delivered transcript turn seen again, at any age; or
//   - a background turn (see doneRepeatBackground) within doneRepeatWindow
//     of the delivery.
//
// counted is false when the turn was already counted, is the delivered turn
// itself, or count is false. Ledger errors fail open, so the completion is
// delivered.
func checkDoneRepeat(childID, profile string, sig DoneSignal, turnUUID string, background, count bool, at time.Time) (repeat, counted bool) {
	prev, ok := ReadLedgerEntry(childID)
	if !ok || !sameDelivered(prev, profile, sig) {
		return false, false
	}
	if turnUUID != "" && prev.TurnUUID == turnUUID {
		return true, false
	}
	if !background || at.Sub(prev.FinishedAt) >= doneRepeatWindow {
		return false, false
	}
	if !count || (turnUUID != "" && prev.LastRepeatUUID == turnUUID) {
		return true, false
	}
	// Re-read just before the write: a task worker in another process may
	// have recorded a new completion since; never overwrite it with this
	// stale entry.
	if cur, ok := ReadLedgerEntry(childID); !ok || !cur.FinishedAt.Equal(prev.FinishedAt) || !sameDelivered(cur, profile, sig) {
		return false, false
	}
	prev.Repeats++
	prev.LastRepeatAt = at
	prev.LastRepeatUUID = turnUUID
	if err := WriteLedgerEntry(prev); err != nil {
		return false, false
	}
	return true, true
}

// sameDelivered reports whether the ledger entry records sig for profile.
func sameDelivered(e CompletionLedgerEntry, profile string, sig DoneSignal) bool {
	if p := strings.TrimSpace(e.Profile); p != "" && profile != "" && p != profile {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(e.Status), sig.Status) && strings.TrimSpace(e.Summary) == strings.TrimSpace(sig.Summary)
}

// doneRepeatBackground reports whether a turn nobody started: a background
// task, a system or scheduled injection, or the child's own inbox prompt
// (the same set ClassifyTurnTier treats as background). A turn a person or a
// send started is news even when it ends with the same sentinel (the #2469
// rule for replies), and so is a turn whose start fell outside the tail
// window (unknown) or a slash command a person typed.
func doneRepeatBackground(facts TurnFacts) bool {
	if strings.TrimSpace(facts.FromID) != "" || facts.TypedCommand {
		return false
	}
	switch facts.Trigger {
	case TurnTriggerTask, TurnTriggerSystem, TurnTriggerInbox:
		return true
	}
	return false
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
