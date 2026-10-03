package session

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// Issue #2469, consumer side: the prompt-time drain delivers records with
// text into the turn that is starting; the Stop hook blocks only for urgent
// records; the wake line names its record; info waiting past the digest
// window wakes an idle parent once.

func commitTestRecord(t *testing.T, parentID string, ev TransitionNotificationEvent) {
	t.Helper()
	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	if ev.FromStatus == "" {
		ev.FromStatus = "running"
	}
	if ev.ToStatus == "" {
		ev.ToStatus = "waiting"
	}
	if ev.Profile == "" {
		ev.Profile = "default"
	}
	if err := CommitToInbox(parentID, ev); err != nil {
		t.Fatalf("CommitToInbox: %v", err)
	}
}

func TestIssue2469_PromptDrainInjectsTextAndConsumes(t *testing.T) {
	reviewTestHome(t, "default")
	parent := "parent-prompt-drain"
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c1", ChildTitle: "board", Tier: TurnTierInfo, Trigger: TurnTriggerTask, Text: "Lane C merged.", LastOutputHash: "turn:a1"})
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c2", ChildTitle: "lead", Tier: TurnTierUrgent, Trigger: TurnTriggerHuman, Text: "NEED: merge or hold?", Question: true, LastOutputHash: "turn:b1"})

	text, events, err := DrainForPrompt(parent)
	if err != nil || len(events) != 2 {
		t.Fatalf("DrainForPrompt: %v events=%d", err, len(events))
	}
	for _, want := range []string{"[agent-deck inbox] 1 urgent, 1 info pending", "- [info] board (c1): waiting\n    Lane C merged.", "- [urgent] lead (c2): waiting\n    NEED: merge or hold?"} {
		if !strings.Contains(text, want) {
			t.Fatalf("injected text missing %q:\n%s", want, text)
		}
	}
	if again, _, _ := DrainForPrompt(parent); again != "" {
		t.Fatalf("second prompt drain must find nothing: %q", again)
	}
	st, _ := ReadInboxStats(parent)
	if st.Drains != 1 || st.RecordsDelivered != 2 || st.BytesInjected != int64(len(text)) {
		t.Fatalf("stats: %+v", st)
	}
	if DrainForPrompt_leafIsCheap(t) {
		t.Log("leaf fast path ok")
	}
}

// DrainForPrompt_leafIsCheap asserts an empty inbox returns without error or text.
func DrainForPrompt_leafIsCheap(t *testing.T) bool {
	t.Helper()
	text, events, err := DrainForPrompt("leaf-with-no-children")
	if err != nil || text != "" || events != nil {
		t.Fatalf("leaf: %q %v %v", text, events, err)
	}
	return true
}

func TestIssue2469_StopHookBlocksOnlyForUrgent(t *testing.T) {
	reviewTestHome(t, "default")
	parent := "parent-stop-tiers"
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c1", ChildTitle: "board", Tier: TurnTierInfo, Text: "progress", LastOutputHash: "turn:a1"})

	if dec, blocked, err := DrainForStopHook(parent, false); err != nil || blocked {
		t.Fatalf("info-only inbox must not block the Stop hook: blocked=%v dec=%+v err=%v", blocked, dec, err)
	}
	if !InboxHasPending(parent) {
		t.Fatal("the info record must still be pending for the prompt-time drain")
	}

	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c2", ChildTitle: "lead", Tier: TurnTierUrgent, Text: "Login expired", ToStatus: "error", LastOutputHash: "turn:b1"})
	dec, blocked, err := DrainForStopHook(parent, false)
	if err != nil || !blocked {
		t.Fatalf("urgent record must block: blocked=%v err=%v", blocked, err)
	}
	if !strings.Contains(dec.Reason, "[urgent] lead (c2): error") || !strings.Contains(dec.Reason, "[info] board (c1): waiting") {
		t.Fatalf("the block must carry the urgent record and the queued info:\n%s", dec.Reason)
	}
	if InboxHasPending(parent) {
		t.Fatal("block drained everything")
	}

	// A legacy record (no tier) from an older producer still blocks.
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c3", ChildTitle: "old", LastOutputHash: "jsonl:100"})
	if _, blocked, _ := DrainForStopHook(parent, false); !blocked {
		t.Fatal("untiered record must block as before")
	}
}

func TestIssue2469_NudgeHeadlineNamesTheRecord(t *testing.T) {
	done := NudgeHeadline(TransitionNotificationEvent{ChildTitle: "board-zero", ChildSessionID: "fdecb64d", Kind: transitionKindFinished, DoneStatus: "ok", DoneSummary: "12 merged, 1 blocked", Tier: TurnTierUrgent})
	if done != "[INBOX] urgent · board-zero (fdecb64d): done (ok) — 12 merged, 1 blocked · details are in this turn's context" {
		t.Fatalf("done headline: %q", done)
	}
	q := NudgeHeadline(TransitionNotificationEvent{ChildTitle: "lead", ChildSessionID: "947c", ToStatus: "waiting", Tier: TurnTierUrgent, Text: "Two options.\nNEED: pick one?"})
	if !strings.HasPrefix(q, "[INBOX] urgent · lead (947c): waiting — Two options.") {
		t.Fatalf("question headline: %q", q)
	}
	long := NudgeHeadline(TransitionNotificationEvent{ChildTitle: "x", ChildSessionID: "y", ToStatus: "waiting", Text: strings.Repeat("a", 1000)})
	if len(long) > 300 {
		t.Fatalf("headline must stay short: %d bytes", len(long))
	}
	legacy := NudgeHeadline(TransitionNotificationEvent{ChildTitle: "old", ChildSessionID: "z", ToStatus: "waiting"})
	if !strings.HasPrefix(legacy, "[INBOX] urgent · old (z): waiting") {
		t.Fatalf("legacy headline: %q", legacy)
	}
}

func TestIssue2469_DigestDueOnlyForAgedInfo(t *testing.T) {
	reviewTestHome(t, "default")
	parent := "parent-digest"
	now := time.Now()
	window := 15 * time.Minute

	if due, _, _ := DigestDue(parent, window, now); due {
		t.Fatal("empty inbox: no digest")
	}
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c1", Tier: TurnTierInfo, Text: "p1", LastOutputHash: "turn:1", Timestamp: now.Add(-5 * time.Minute)})
	if due, _, _ := DigestDue(parent, window, now); due {
		t.Fatal("info younger than the window: no digest yet")
	}
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c2", Tier: TurnTierInfo, Text: "p2", LastOutputHash: "turn:2", Timestamp: now.Add(-20 * time.Minute)})
	due, records, children := DigestDue(parent, window, now)
	if !due || records != 2 || children != 2 {
		t.Fatalf("aged info must be due: due=%v records=%d children=%d", due, records, children)
	}
	markDigestWake(parent, now)
	if due, _, _ := DigestDue(parent, window, now.Add(time.Minute)); due {
		t.Fatal("a digest inside the window must not repeat")
	}
	if due, _, _ := DigestDue(parent, window, now.Add(window+time.Minute)); !due {
		t.Fatal("after the window the digest is due again")
	}
	// An urgent record pending means the urgent wake covers it: no digest.
	commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: "c3", Tier: TurnTierUrgent, Text: "err", ToStatus: "error", LastOutputHash: "turn:3", Timestamp: now.Add(-30 * time.Minute)})
	if due, _, _ := DigestDue(parent, window, now.Add(window+time.Minute)); due {
		t.Fatal("urgent pending: the urgent path wakes, not the digest")
	}
	if DigestNudgeMessage(2, 2) != "[INBOX] digest · 2 progress note(s) from 2 child(ren) · details are in this turn's context" {
		t.Fatal("digest message")
	}
}

func TestIssue2469_FleetBlockUnchangedSkips(t *testing.T) {
	reviewTestHome(t, "default")
	if FleetBlockUnchanged("p", "[agent-deck fleet] 2 children: 1 running, 1 waiting, 0 done") {
		t.Fatal("first snapshot is always new")
	}
	if !FleetBlockUnchanged("p", "[agent-deck fleet] 2 children: 1 running, 1 waiting, 0 done") {
		t.Fatal("identical snapshot must be reported unchanged")
	}
	if FleetBlockUnchanged("p", "[agent-deck fleet] 2 children: 0 running, 2 waiting, 0 done") {
		t.Fatal("changed counts are new")
	}
}

func TestIssue2469_PromptInjectionStaysUnderBudgetAndConsumesOnlyWhatItShows(t *testing.T) {
	reviewTestHome(t, "default")
	parent := "parent-budget"
	for i := 0; i < 120; i++ {
		tier := TurnTierInfo
		if i%5 == 0 {
			tier = TurnTierUrgent
		}
		commitTestRecord(t, parent, TransitionNotificationEvent{ChildSessionID: fmt.Sprintf("c%03d", i), ChildTitle: "child", Tier: tier, Text: strings.Repeat("x", 500), LastOutputHash: fmt.Sprintf("turn:%d", i)})
	}
	var shown []TransitionNotificationEvent
	rounds := 0
	for rounds < 20 {
		text, events, err := DrainForPrompt(parent)
		if err != nil {
			t.Fatalf("drain: %v", err)
		}
		if text == "" {
			break
		}
		rounds++
		if len(text) > promptContextBudgetBytes+200 {
			t.Fatalf("round %d injection %d bytes exceeds the budget", rounds, len(text))
		}
		for _, ev := range events {
			if !strings.Contains(text, fmt.Sprintf("(%s): waiting\n    xxxx", ev.ChildSessionID)) {
				t.Fatalf("round %d consumed %s without showing its text", rounds, ev.ChildSessionID)
			}
		}
		if rounds == 1 {
			// 24 urgent x ~560 B exceed the budget, so the whole first round
			// must be urgent records.
			for i, ev := range events {
				if !ev.IsUrgent() {
					t.Fatalf("urgent records must be delivered first; position %d was %+v", i, ev)
				}
			}
			if !strings.Contains(text, "more record(s) are still queued") {
				t.Fatal("first round must say more is queued")
			}
		}
		shown = append(shown, events...)
	}
	if len(shown) != 120 {
		t.Fatalf("every record must eventually be delivered exactly once: %d over %d rounds", len(shown), rounds)
	}
	if rounds < 2 {
		t.Fatal("120 x 500 B cannot fit one 9 KB injection")
	}
	seen := map[string]bool{}
	for _, ev := range shown {
		if seen[ev.ChildSessionID] {
			t.Fatalf("record %s delivered twice", ev.ChildSessionID)
		}
		seen[ev.ChildSessionID] = true
	}
	if InboxHasPending(parent) {
		t.Fatal("inbox must be empty at the end")
	}
}

func TestIssue2469_NudgeHeadlineIsOnePrintableLine(t *testing.T) {
	ev := TransitionNotificationEvent{ChildTitle: "evil\ntitle", ChildSessionID: "r1", ToStatus: "waiting", Tier: TurnTierUrgent,
		Text: "first line\x1b[2J\r\nrm -rf /\nsecond"}
	got := NudgeHeadline(ev)
	if strings.ContainsAny(got, "\n\r\x1b\t") {
		t.Fatalf("headline carries control characters: %q", got)
	}
	if !strings.Contains(got, "first line") || strings.Contains(got, "rm -rf") {
		t.Fatalf("headline must carry only the first line of the text: %q", got)
	}
}
