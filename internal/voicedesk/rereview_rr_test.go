package voicedesk

// Adversarial cases RR1-RR7 from the re-review of the voice desk.

import (
	"strings"
	"testing"
)

func rrAnswer(t *testing.T, h *harness, word string) (Result, int) {
	t.Helper()
	ask := h.say("tell phone build to run the tests")
	if ask.Confirm == nil {
		t.Fatalf("no question asked: %+v", ask)
	}
	before := len(h.deck.sent)
	got := h.do(Request{Sentence: word, Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	return got, len(h.deck.sent) - before
}

// RR1. Only the listed answer forms act. A word that merely sounds like a
// yes ("sorry" ~ "sure", "ha", "share") must never send.
func TestRereviewSoundAlikeWordsAreNotAYes(t *testing.T) {
	for _, w := range []string{"sorry", "Sorry.", "sore", "share", "shore", "ha", "hah", "jee", "bhaji", "car do", "yes plus", "sand it", "sound it"} {
		t.Run(w, func(t *testing.T) {
			h := newHarness(t)
			got, sent := rrAnswer(t, h, w)
			if sent != 0 || got.Intent == IntentConfirmYes {
				t.Fatalf("%q was taken as a yes: intent=%s say=%q sent=%d", w, got.Intent, got.Say, sent)
			}
		})
	}
}

// RR2. A second short non-answer must not come back handled:false (the
// caller would type it into the session the mic is on).
func TestRereviewSecondMissIsNotHandedOn(t *testing.T) {
	h := newHarness(t)
	first, _ := rrAnswer(t, h, "hmm")
	if first.Confirm == nil {
		t.Fatalf("first miss was not asked again: %+v", first)
	}
	second := h.do(Request{Sentence: "hmm", Pending: first.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if !second.Handled {
		t.Fatalf("second miss is handled:false, so the caller types %q into the current session", "hmm")
	}
}

// RR3. A re-asked question still needs a real yes, once, and the old token is dead.
func TestRereviewReaskKeepsSingleUse(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	re := h.do(Request{Sentence: "wait", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if re.Confirm == nil {
		t.Fatalf("not re-asked: %+v", re)
	}
	h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if len(h.deck.sent) != 0 {
		t.Fatalf("the old token still sends")
	}
	h.do(Request{Sentence: "yes", Pending: re.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	h.do(Request{Sentence: "yes", Pending: re.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if len(h.deck.sent) != 1 {
		t.Fatalf("want exactly one send, got %d", len(h.deck.sent))
	}
}

// RR4. A late answer to a re-asked question does not act.
func TestRereviewReaskLateYes(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	re := h.do(Request{Sentence: "wait", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if re.Confirm == nil {
		t.Fatalf("not re-asked")
	}
	got := h.do(Request{Sentence: "yes", Pending: re.Confirm.Token, AnswerDelayMs: 10001, HasAnswerDelay: true})
	if len(h.deck.sent) != 0 || refusedOf(got) != RefusedExpired {
		t.Fatalf("late yes on a re-asked question: %+v sent=%d", got, len(h.deck.sent))
	}
}

// RR5. Vendor key shapes with underscores are secrets, not identifiers.
func TestRereviewUnderscoreKeysAreSecrets(t *testing.T) {
	// built at run time so no secret scanner mistakes the fixtures for keys
	live := "_" + "live" + "_"
	for _, s := range []string{"sk" + live + "jMPFLVjFUPXQzkM4Bv3aYavh", "rk" + live + "9fK2mPq7Lx4Rt8Zw3Bn6aB3x", "wh" + "sec_" + "jMPFLVjFUPXQzkM4Bv3aYavh9fK2"} {
		if !HasSecret("use " + s + " for the webhook") {
			t.Errorf("%s not detected", s)
		}
	}
}

// RR6. An explicit tell whose unknown name carries a number is a session
// reference, not coding talk: not-found, never handed on.
func TestRereviewUnknownNumberedNameIsNotFound(t *testing.T) {
	for _, s := range []string{"tell audit 0995 to stop", "status of audit 955"} {
		h := newHarness(t)
		got := h.say(s)
		if !got.Handled || refusedOf(got) != RefusedNotFound {
			t.Errorf("%q: handled=%v refused=%q (want not-found)", s, got.Handled, refusedOf(got))
		}
	}
}

// RR7. Control characters beyond C0 never reach the message.
func TestRereviewMessageHasNoControlOrBidi(t *testing.T) {
	h := newHarness(t)
	got := h.say("tell phone build to run \u009b31m the ‮ tests   now \u007f please")
	if got.Confirm == nil {
		t.Fatalf("no question: %+v", got)
	}
	line := got.Show.Items[0].Line
	for _, bad := range []string{"\u009b", "‮", " ", "\u007f"} {
		if strings.Contains(line, bad) || strings.Contains(got.Say, bad) {
			t.Errorf("%q survives in the message %q", bad, line)
		}
	}
}
