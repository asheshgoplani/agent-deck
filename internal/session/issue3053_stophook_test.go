package session

import (
	"strings"
	"testing"
	"time"
)

// TestIssue3053_StopHookMessageHeading checks that the Stop-hook reason labels
// queued replies, progress updates and finished records as normal inbox
// delivery and keeps each record's own text.
func TestIssue3053_StopHookMessageHeading(t *testing.T) {
	reply := TransitionNotificationEvent{
		ChildSessionID: "sender", ChildTitle: "reviewer", TargetKind: InboxTargetKindReply,
		Tier: TurnTierUrgent, ToStatus: "waiting", Text: "Please review.\nThe patch is ready.",
	}
	progress := TransitionNotificationEvent{
		ChildSessionID: "worker", Tier: TurnTierInfo, ToStatus: "waiting", Text: "Still checking results.",
	}
	finished := TransitionNotificationEvent{
		ChildSessionID: "child", ChildTitle: "builder", Tier: TurnTierInfo,
		ToStatus: "waiting", Kind: transitionKindFinished, DoneStatus: "success", DoneSummary: "Build passed.",
	}
	replyText := "- [urgent] reply from=sender reviewer: waiting\n    Please review.\n    The patch is ready.\n"
	progressText := "- [info] worker (worker): waiting\n    Still checking results.\n"
	finishedText := "- [info] builder (child): success — Build passed.\n"
	for _, tc := range []struct {
		name   string
		events []TransitionNotificationEvent
		want   []string
	}{
		{"tagged reply", []TransitionNotificationEvent{reply}, []string{replyText}},
		{"waiting progress", []TransitionNotificationEvent{progress}, []string{progressText}},
		{"finished", []TransitionNotificationEvent{finished}, []string{finishedText}},
		{"mixed", []TransitionNotificationEvent{reply, progress, finished}, []string{replyText, progressText, finishedText}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatCompletionsForInjection(tc.events)
			if !strings.HasPrefix(got, "New session messages (normal inbox delivery; continue this turn to handle each):\n") {
				t.Errorf("missing neutral normal-delivery heading:\n%s", got)
			}
			if strings.Contains(got, "Child session(s) completed while you were busy") {
				t.Errorf("heading must not label every message a child completion:\n%s", got)
			}
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("missing record text %q:\n%s", want, got)
				}
			}
		})
	}
}

// TestIssue3053_StopHookReplyDeliveredOnce checks that an urgent reply blocks
// Stop once under the normal-delivery heading and is not redelivered. The
// exactly-once drain already holds on main; this guards it alongside the new
// heading.
func TestIssue3053_StopHookReplyDeliveredOnce(t *testing.T) {
	inboxTestHome(t)
	parent := "parent-stop-reply"
	if err := CommitToInbox(parent, TransitionNotificationEvent{
		ChildSessionID: "sender", ChildTitle: "reviewer", TargetKind: InboxTargetKindReply,
		Profile: "default", FromStatus: "running", ToStatus: "waiting",
		Tier: TurnTierUrgent, Text: "Please review the patch.",
		LastOutputHash: "reply-1", Timestamp: time.Now(),
	}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	dec, blocked, err := DrainForStopHook(parent, false)
	if err != nil || !blocked || dec.Decision != "block" {
		t.Fatalf("expected block for urgent reply: dec=%+v blocked=%v err=%v", dec, blocked, err)
	}
	for _, want := range []string{"New session messages (normal inbox delivery;", "reply from=sender reviewer: waiting", "Please review the patch."} {
		if !strings.Contains(dec.Reason, want) {
			t.Errorf("reason missing %q:\n%s", want, dec.Reason)
		}
	}
	if again, blocked, err := DrainForStopHook(parent, true); err != nil || blocked || again.Reason != "" || again.Decision != "" {
		t.Fatalf("second drain must not redeliver: dec=%+v blocked=%v err=%v", again, blocked, err)
	}
}
