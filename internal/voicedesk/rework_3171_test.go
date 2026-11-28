package voicedesk

import "testing"

// A session of another local profile is never a target, for any intent,
// even when the deck lists it (the rule must not rest on the deck alone).
func TestRework3171OtherProfileNeverATarget(t *testing.T) {
	for _, s := range []string{"tell otter notes to continue", "status of otter notes", "read otter notes"} {
		h := newHarness(t)
		h.deck.sessions = append(h.deck.sessions, Session{ID: "e1000001-1700000031", Title: "otter-notes",
			Group: "team/alex", Profile: "other", Status: "idle"})
		h.deck.replies["e1000001-1700000031"] = "the other profile's reply"
		got := h.say(s)
		if got.Confirm != nil || len(h.deck.sent) != 0 || targetID(got) == "e1000001-1700000031" {
			t.Errorf("%q reached a session of another profile: say=%q target=%s", s, got.Say, targetID(got))
		}
		if !got.Handled || refusedOf(got) != RefusedNotFound || got.Say != "Otter notes is in another profile; nothing done." {
			t.Errorf("%q: handled=%v refused=%q say=%q, want the other-profile refusal", s, got.Handled, refusedOf(got), got.Say)
		}
	}
}

// A dry-run yes to a find never switches: no then.action for the caller.
func TestRework3171DryRunYesNeverSwitches(t *testing.T) {
	h := newHarness(t)
	q := h.say("find voice test")
	if q.Confirm == nil {
		t.Fatalf("setup: %q", q.Say)
	}
	r := h.do(Request{Sentence: "yes", Pending: q.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true, DryRun: true})
	if r.Then != nil {
		t.Errorf("dry-run yes returned then=%+v", r.Then)
	}
	if len(h.deck.shown) != 0 {
		t.Errorf("dry-run yes showed: %v", h.deck.shown)
	}
}
