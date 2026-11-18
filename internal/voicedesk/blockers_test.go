package voicedesk

import (
	"context"
	"strings"
	"testing"
)

// Fixes for the second independent review (B1-B5, S1-S8). B1 (unreadable
// config) is a command test in cmd/agent-deck.

// B3: control characters and newlines never reach the target as keys.
func TestMessageControlCharacters(t *testing.T) {
	for _, raw := range []string{"check this\n/clear", "stop\x03 now\x04", "a\x1b[31mred\ttab\r\nnext"} {
		h := newHarness(t)
		got := h.do(Request{Sentence: "tell deck conductor to " + raw, On: idConductor})
		for _, m := range h.deck.sent {
			if strings.ContainsAny(m.msg, "\n\r\t\x03\x04\x1b") {
				t.Fatalf("control character sent: %q", m.msg)
			}
		}
		if strings.ContainsAny(got.Say, "\n\r\t\x03\x04\x1b") {
			t.Fatalf("control character in say: %q", got.Say)
		}
	}
	h := newHarness(t)
	h.do(Request{Sentence: "tell deck conductor to check this\n/clear", On: idConductor})
	if len(h.deck.sent) != 1 || h.deck.sent[0].msg != "check this /clear" {
		t.Fatalf("sent %+v", h.deck.sent)
	}
}

// B2: a session at a menu gets nothing typed, on the direct path, the
// read-back path and when it opens a menu between question and yes.
func TestNoSendAtMenu(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	h.setSubstate(idPhone, "interactive-menu")
	got := h.yes(ask)
	if len(h.deck.sent) != 0 || got.Say != "Phone build is asking a question; answer it on screen first." {
		t.Fatalf("got %q sent=%v", got.Say, h.deck.sent)
	}
	got = h.do(Request{Sentence: "tell phone build to run the tests", On: idPhone})
	if len(h.deck.sent) != 0 || refusedOf(got) != RefusedUnsafe {
		t.Fatalf("direct: %+v", got)
	}
}

func (h *harness) setSubstate(id, sub string) {
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == id {
			h.deck.sessions[i].Substate = sub
		}
	}
}

// B5: an unknown send outcome is never said as a failure or a success.
type flakySend struct {
	*fakeDeck
	err error
}

func (f *flakySend) Send(ctx context.Context, s Session, msg string) error {
	_ = f.fakeDeck.Send(ctx, s, msg)
	return f.err
}

func TestSendOutcomeIsSaidHonestly(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrUnconfirmed, "I am not sure it was sent; check conductor deck alex."},
		{ErrMenuOpen, "Conductor deck alex is asking a question; answer it on screen first."},
		{context.DeadlineExceeded, "Could not send to conductor deck alex."},
	} {
		h := newHarness(t)
		h.desk.Deck = &flakySend{fakeDeck: h.deck, err: c.err}
		ask := h.say("tell deck conductor to check the build")
		got := h.yes(ask)
		if got.Say != c.want {
			t.Errorf("%v: said %q", c.err, got.Say)
		}
	}
}

// B5: the yes path looks up only the target, never relists every host.
func TestYesLooksUpOnlyTheTarget(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell deck conductor to check the build")
	before := h.deck.lists
	h.yes(ask)
	if h.deck.lists != before {
		t.Fatalf("the yes path listed every host again (%d listings)", h.deck.lists-before)
	}
}

// S3: common answers and Urdu script; a short non-answer is asked again
// once, then handed on.
func TestShortNonAnswerIsAskedAgainOnce(t *testing.T) {
	for _, yes := range []string{"okay", "yeah", "sure", "haan bhej do", "theek hai", "ji", "bhejo", "ہاں", "جی ہاں"} {
		if k, _ := classifyAnswer(yes); k != AnswerYes {
			t.Errorf("%q is not a yes", yes)
		}
	}
	if k, _ := classifyAnswer("نہیں"); k != AnswerNo {
		t.Errorf("Urdu no is not a no")
	}
	h := newHarness(t)
	ask := h.say("tell deck conductor to check the build")
	again := h.do(Request{Sentence: "wait what", Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
	if again.Confirm == nil || again.Say != "Please say yes or no." || !again.Handled {
		t.Fatalf("not asked again: %+v", again)
	}
	third := h.do(Request{Sentence: "hmm what", Pending: again.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
	if !third.Handled || third.Say != "Cancelled, nothing sent." || len(h.deck.sent) != 0 {
		t.Fatalf("second miss: %+v", third)
	}
	// a re-asked question still sends on a yes
	ask = h.say("tell deck conductor to check the build")
	again = h.do(Request{Sentence: "yes?", Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
	got := h.yes(again)
	if got.Say != "Sent to conductor deck alex." {
		t.Fatalf("after re-ask: %+v", got)
	}
}

// S6: talk in a soft desk form is decided before any listing.
func TestSoftTalkNeedsNoListing(t *testing.T) {
	for _, s := range []string{"read the file and fix the bug", "what is this function doing with the cache", "read the logs and tell me what failed"} {
		h := newHarness(t)
		h.deck.noList = true
		if got := h.say(s); got.Handled {
			t.Fatalf("%q: %+v", s, got)
		}
	}
}

// S7: an unreadable local profile is said, by name.
func TestUnreadableProfileIsSaid(t *testing.T) {
	h := newHarness(t)
	h.deck.errs = []HostError{{Profile: "work", Error: "unreadable"}}
	if got := h.say("list my sessions"); !strings.HasSuffix(got.Say, "Profile work did not answer.") {
		t.Fatalf("got %q", got.Say)
	}
}

func (h *harness) yes(ask Result) Result {
	h.t.Helper()
	if ask.Confirm == nil {
		h.t.Fatalf("no question asked: %+v", ask)
	}
	return h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
}
