package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Prompt-time inbox delivery (issue #2469, design principle 2). The wake
// nudge used to be an empty "[INBOX] ..." line: the parent then spent a tool
// call on `inbox drain` and another on re-reading the child. Now the parent's
// UserPromptSubmit hook drains the inbox as the turn STARTS and injects the
// records, text included, as additionalContext, so the turn that the nudge
// (or a heartbeat, or a human) started already contains everything pending
// and the model acts with zero tool calls. The Stop-hook drain stays as the
// busy-parent path and now blocks only for urgent records.

// inboxContextHeader opens the prompt-time injection.
const inboxContextHeader = "[agent-deck inbox]"

// DrainForPrompt consumes the parent's pending records for the turn that is
// starting and renders them for injection. Returns "" when nothing is
// pending (the common case for every leaf session: two stats, no writes).
func DrainForPrompt(instanceID string) (string, []TransitionNotificationEvent, error) {
	if strings.TrimSpace(instanceID) == "" || !InboxHasPending(instanceID) {
		return "", nil, nil
	}
	var left int
	events, err := DrainInboxForParentWhere(instanceID, func(pending []TransitionNotificationEvent) []TransitionNotificationEvent {
		take := selectRecordsForBudget(pending, promptContextBudgetBytes)
		left = len(pending) - len(take)
		return take
	})
	if err != nil || len(events) == 0 {
		return "", nil, err
	}
	text := FormatInboxRecords(events, fmt.Sprintf("%s %s pending from your children — act on each (the text is the child's own words; do not re-read the child unless you need more):", inboxContextHeader, countByTier(events)))
	if left > 0 {
		text += fmt.Sprintf("%d more record(s) are still queued and arrive on your next turn (or now with `agent-deck inbox drain self --json`).\n", left)
	}
	_ = BumpInboxStats(instanceID, func(s *InboxStats) {
		s.Drains++
		s.RecordsDelivered += int64(len(events))
		s.BytesInjected += int64(len(text))
		if ms := urgentLatencyMS(events, time.Now()); ms > 0 {
			s.LastUrgentLatencyMS = ms
		}
	})
	return text, events, nil
}

// selectRecordsForBudget picks the records whose full rendering fits the
// byte budget: urgent records first (oldest first), then info, each with its
// text. Records that do not fit are NOT consumed; they stay queued for the
// next turn, so nothing is ever consumed without being shown.
func selectRecordsForBudget(pending []TransitionNotificationEvent, budget int) []TransitionNotificationEvent {
	used := 200 // header + the "more queued" line
	var take []TransitionNotificationEvent
	for _, wantUrgent := range []bool{true, false} {
		for _, ev := range pending {
			if ev.IsUrgent() != wantUrgent {
				continue
			}
			one := len(FormatInboxRecords([]TransitionNotificationEvent{ev}, ""))
			if used+one > budget {
				continue
			}
			used += one
			take = append(take, ev)
		}
	}
	return take
}

// countByTier renders "2 urgent, 3 info" style counts for a header.
func countByTier(events []TransitionNotificationEvent) string {
	var urgent, info int
	for _, ev := range events {
		if ev.IsUrgent() {
			urgent++
		} else {
			info++
		}
	}
	parts := []string{}
	if urgent > 0 {
		parts = append(parts, fmt.Sprintf("%d urgent", urgent))
	}
	if info > 0 {
		parts = append(parts, fmt.Sprintf("%d info", info))
	}
	if len(parts) == 0 {
		return "0 records"
	}
	return strings.Join(parts, ", ")
}

// InboxHasUrgentPending reports whether the parent's inbox holds a record a
// consumer must wake for: an urgent (or untiered legacy) record, or a staged
// in-flight drain that must be finished. Non-consuming, one file read.
func InboxHasUrgentPending(parentID string) bool {
	if strings.TrimSpace(parentID) == "" {
		return false
	}
	if fileHasContent(inboxInflightPathFor(parentID)) {
		return true
	}
	events, err := ReadInboxEventsForDisplay(parentID)
	if err != nil {
		return true // unreadable: fail toward delivering
	}
	for _, ev := range events {
		if ev.IsUrgent() {
			return true
		}
	}
	return false
}

// NudgeHeadline renders the one-line wake message for an urgent record. The
// content arrives through the prompt-time drain of the turn this line
// starts; the line itself only has to tell the parent WHY it woke. Bounded
// to about 240 bytes.
func NudgeHeadline(ev TransitionNotificationEvent) string {
	title := strings.TrimSpace(ev.ChildTitle)
	if title == "" {
		title = ev.ChildSessionID
	}
	status := ev.ToStatus
	if ev.Kind == transitionKindFinished && ev.DoneStatus != "" {
		status = "done (" + ev.DoneStatus + ")"
	}
	tier := ev.Tier
	if tier == "" {
		tier = TurnTierUrgent
	}
	if ev.TargetKind == InboxTargetKindReply {
		tier = InboxTargetKindReply
	}
	head := fmt.Sprintf("[INBOX] %s · %s (%s): %s", tier, title, ev.ChildSessionID, status)
	detail := strings.TrimSpace(ev.DoneSummary)
	if detail == "" {
		detail = firstLine(ev.Text)
	}
	if detail != "" {
		head += " — " + CapTurnText(detail, 160)
	}
	// The headline is TYPED into the parent's pane. Child text (and, for a
	// pulled record, text a remote host chose) must never carry a newline or
	// a control sequence that could submit or alter more than this one line.
	return printableOneLine(head) + " · details are in this turn's context"
}

// printableOneLine replaces every control character (including CR/LF, tab,
// escape and DEL) with a space and collapses runs of spaces.
func printableOneLine(s string) string {
	var b strings.Builder
	lastSpace := false
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) || !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			r = ' '
		}
		if r == ' ' {
			if lastSpace {
				continue
			}
			lastSpace = true
		} else {
			lastSpace = false
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// DigestNudgeMessage is the wake for info that waited past the digest window.
func DigestNudgeMessage(records, children int) string {
	return fmt.Sprintf("[INBOX] digest · %d progress note(s) from %d child(ren) · details are in this turn's context", records, children)
}

func firstLine(text string) string {
	for _, raw := range strings.Split(text, "\n") {
		if line := strings.TrimSpace(raw); line != "" {
			return line
		}
	}
	return ""
}

// --- info digest timer -------------------------------------------------------

// inboxDigestDir holds per-parent "last digest wake" timestamps.
func inboxDigestDir() string {
	return runtimeDirOrTemp("inbox-digest")
}

func inboxDigestPath(parentID string) string {
	return filepath.Join(inboxDigestDir(), sanitizeInboxName(parentID)+".json")
}

// lastDigestWake returns when the parent was last woken for a digest (zero
// when never).
func lastDigestWake(parentID string) time.Time {
	info, err := os.Stat(inboxDigestPath(parentID))
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

func markDigestWake(parentID string, at time.Time) {
	path := inboxDigestPath(parentID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	if err := writeFileDurable(path, []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err == nil {
		_ = os.Chtimes(path, at, at)
	}
}

// DigestDue reports whether an idle parent should be woken for info that has
// waited: no urgent record pending (those wake on their own), at least one
// info record older than the window, and no digest wake inside the window.
// Returns the pending record and child counts for the message.
func DigestDue(parentID string, window time.Duration, now time.Time) (due bool, records, children int) {
	if window <= 0 || !InboxHasPending(parentID) {
		return false, 0, 0
	}
	events, err := ReadInboxEventsForDisplay(parentID)
	if err != nil || len(events) == 0 {
		return false, 0, 0
	}
	kids := map[string]bool{}
	var oldest time.Time
	for _, ev := range events {
		if ev.IsUrgent() {
			return false, 0, 0
		}
		kids[ev.ChildSessionID] = true
		if oldest.IsZero() || ev.Timestamp.Before(oldest) {
			oldest = ev.Timestamp
		}
	}
	if now.Sub(oldest) < window {
		return false, 0, 0
	}
	if last := lastDigestWake(parentID); !last.IsZero() && now.Sub(last) < window {
		return false, 0, 0
	}
	return true, len(events), len(kids)
}

// promptContextBudgetBytes keeps the injected block under Claude Code's
// additionalContext limit (10,000 characters; past it the model sees only a
// preview). Records beyond the budget stay queued for the next turn.
const promptContextBudgetBytes = 9000
