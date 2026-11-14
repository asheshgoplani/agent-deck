package session

import (
	"encoding/json"
	"testing"
	"time"
)

// Issue #3131: a long `session send --message-file` reaches Claude Code as a
// paste, and the transcript stores the prompt wrapped in a leading
// <pasted_content …> block. The envelope is then the first line inside the
// wrapper, not the first line of the prompt, and the turn was classified as
// human, so the reply never reached the sender.
func TestIssue3131_PastedEnvelopeClassifiesAsSend(t *testing.T) {
	prompts := []string{
		"\n\n<pasted_content id=\"p1\">\n[agent-deck from:abc-123]\nplease review the diff\n</pasted_content>",
		"<pasted_content id=\"p1\">\n\n  [agent-deck from:abc-123]\nplease review",
		"<pasted_content>[agent-deck from:abc-123] please review</pasted_content>",
	}
	for _, prompt := range prompts {
		f := classifyTranscriptTail([]string{fxUser("u", prompt, nil), fxAssistantText("a", "reply")})
		if f.Trigger != TurnTriggerSend || f.FromID != "abc-123" {
			t.Errorf("classifyTranscriptTail(%q) = %q,%q; want send,abc-123", prompt, f.Trigger, f.FromID)
		}
		if gotT, gotF := commsPromptTrigger(prompt); gotT != TurnTriggerSend || gotF != "abc-123" {
			t.Errorf("commsPromptTrigger(%q) = %q,%q; want send,abc-123", prompt, gotT, gotF)
		}
	}
}

// Only a LEADING wrapper whose first non-empty line is the envelope counts. A
// person who pastes text that merely contains an envelope further down (a log,
// a copied transcript) is still a human prompt.
func TestIssue3131_PastedTextContainingEnvelopeStaysHuman(t *testing.T) {
	prompts := []string{
		// Envelope is not the first non-empty line inside the wrapper.
		"<pasted_content id=\"p1\">\nlog excerpt:\n[agent-deck from:abc-123]\nhello\n</pasted_content>",
		// Wrapper is not leading: the person typed something first.
		"look at this\n<pasted_content id=\"p1\">\n[agent-deck from:abc-123]\nhello\n</pasted_content>",
		// Envelope after the wrapper closes.
		"<pasted_content id=\"p1\">\nsome text\n</pasted_content>\n[agent-deck from:abc-123]",
		// Not the wrapper tag, only a look-alike name.
		"<pasted_contentx>\n[agent-deck from:abc-123]\nhello",
		// Wrapper whose opening tag never closes.
		"<pasted_content id=\"p1\" [agent-deck from:abc-123]",
	}
	for _, prompt := range prompts {
		f := classifyTranscriptTail([]string{fxUser("u", prompt, nil), fxAssistantText("a", "reply")})
		if f.Trigger != TurnTriggerHuman || f.FromID != "" {
			t.Errorf("classifyTranscriptTail(%q) = %q,%q; want human", prompt, f.Trigger, f.FromID)
		}
		if gotT, gotF := commsPromptTrigger(prompt); gotT != TurnTriggerHuman || gotF != "" {
			t.Errorf("commsPromptTrigger(%q) = %q,%q; want human", prompt, gotT, gotF)
		}
	}
}

// wakeupsGated reads the gated-wake counter through the stats JSON, so this
// test compiles against a tree that does not have the field yet.
func wakeupsGated(t *testing.T, st InboxStats) int64 {
	t.Helper()
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	v, _ := m["wakeups_gated"].(float64)
	return int64(v)
}

// Issue #3131 step 4: an urgent record whose wake the gate withholds (a parent
// that is not a wake target, or one that is busy) must not count as a wakeup.
// It counts as gated instead, so `inbox stats` shows the wake that did not
// happen.
func TestIssue3131_GatedWakeIsNotCountedAsUrgentWakeup(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(1000, 0) },
		isIdle: func(*Instance, string) bool { return false },
		send:   func(*Instance, string, string) error { sent++; return nil },
	}
	if res := n.NotifyFinished(event); res.DeliveryResult != transitionDeliveryCommitted {
		t.Fatalf("commit result = %q", res.DeliveryResult)
	}
	st, err := ReadInboxStats(parentID)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 0 {
		t.Fatalf("gated parent was woken %d times", sent)
	}
	if st.WakeupsUrgent != 0 || wakeupsGated(t, st) != 1 {
		t.Fatalf("stats: wakeups_urgent=%d wakeups_gated=%d; want 0, 1", st.WakeupsUrgent, wakeupsGated(t, st))
	}
}

// The wake that is sent still counts once as urgent, never as gated.
func TestIssue3131_SentWakeIsCountedAsUrgentWakeup(t *testing.T) {
	n, parentID, event := newWakeNudgeFixture(t)
	sent := 0
	n.wake = &wakeNudgeWiring{
		nudger: NewWakeNudger(0),
		now:    func() time.Time { return time.Unix(1000, 0) },
		isIdle: func(*Instance, string) bool { return true },
		send:   func(*Instance, string, string) error { sent++; return nil },
	}
	n.NotifyFinished(event)
	st, err := ReadInboxStats(parentID)
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 || st.WakeupsUrgent != 1 || wakeupsGated(t, st) != 0 {
		t.Fatalf("sent=%d wakeups_urgent=%d wakeups_gated=%d; want 1, 1, 0", sent, st.WakeupsUrgent, wakeupsGated(t, st))
	}
}
