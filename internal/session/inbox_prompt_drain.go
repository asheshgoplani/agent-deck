package session

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	events, err := DrainInboxForParent(instanceID)
	if err != nil || len(events) == 0 {
		return "", nil, err
	}
	text := formatInboxRecordsBudgeted(events, fmt.Sprintf("%s %s pending from your children — act on each (the text is the child's own words; do not re-read the child unless you need more):", inboxContextHeader, countByTier(events)), promptContextBudgetBytes)
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
	head := fmt.Sprintf("[INBOX] %s · %s (%s): %s", tier, title, ev.ChildSessionID, status)
	detail := strings.TrimSpace(ev.DoneSummary)
	if detail == "" {
		detail = firstLine(ev.Text)
	}
	if detail != "" {
		head += " — " + CapTurnText(detail, 160)
	}
	return head + " · details are in this turn's context"
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
// preview). Records beyond the budget are listed as one-liners without text,
// so delivery degrades to "re-read that child" instead of to a truncated blob.
const promptContextBudgetBytes = 9000

func formatInboxRecordsBudgeted(events []TransitionNotificationEvent, header string, budget int) string {
	full := FormatInboxRecords(events, header)
	if len(full) <= budget {
		return full
	}
	// Urgent records keep their text first; info records are trimmed first.
	var withText, overflow []TransitionNotificationEvent
	used := len(header) + 1
	for _, pass := range [][]bool{{true}, {false}} {
		for _, ev := range events {
			if ev.IsUrgent() != pass[0] {
				continue
			}
			one := FormatInboxRecords([]TransitionNotificationEvent{ev}, "")
			if used+len(one) <= budget {
				withText = append(withText, ev)
				used += len(one)
			} else {
				stripped := ev
				stripped.Text = ""
				overflow = append(overflow, stripped)
			}
		}
	}
	out := FormatInboxRecords(withText, header)
	if len(overflow) == 0 {
		return out
	}
	// The overflow list itself must fit: list as many one-liners as the
	// remaining budget allows and summarise the rest by count.
	listed := 0
	for listed < len(overflow) {
		candidate := FormatInboxRecords(overflow[:listed+1], "")
		if len(out)+len(candidate)+200 > budget {
			break
		}
		listed++
	}
	tail := FormatInboxRecords(overflow[:listed], fmt.Sprintf("%d more record(s), text omitted for size (read the child with `agent-deck session output <id> -q` if needed):", len(overflow)))
	if listed < len(overflow) {
		tail += fmt.Sprintf("… and %d further record(s) not listed; they are consumed, see `agent-deck inbox stats self`.\n", len(overflow)-listed)
	}
	return out + tail
}
