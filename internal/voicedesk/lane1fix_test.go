package voicedesk

import "testing"

// Lane-1 listener-style rows (second run).
func TestListenerAnswerSpellings(t *testing.T) {
	for _, c := range []struct{ s, want string }{
		{"rahne do", AnswerNo}, {"rehne do", AnswerNo}, {"nahee", AnswerNo}, {"नहीं", AnswerNo},
		{"haa", AnswerYes}, {"han", AnswerYes}, {"haan bhej do", AnswerYes}, {"bhejdo", AnswerYes},
		{"pahla", AnswerChoice}, {"pehla", AnswerChoice}, {"dusra", AnswerChoice}, {"doosra", AnswerChoice},
		{"ho", AnswerNone}, {"haan lekin ruko", AnswerNone}, {"rahne do aur build chalao", AnswerNone},
	} {
		if got, _ := classifyAnswer(c.s); got != c.want {
			t.Errorf("%q = %q, want %q", c.s, got, c.want)
		}
	}
}

func TestListenerRahneDoCancelsATell(t *testing.T) {
	h := newHarness(t)
	ask := h.say("Tell deck conductor to check the phone build")
	got := h.do(Request{Sentence: "rahne do", Pending: ask.Confirm.Token, AnswerDelayMs: 900, HasAnswerDelay: true})
	if got.Intent != IntentConfirmNo || got.Say != "Cancelled." || len(h.deck.sent) != 0 {
		t.Fatalf("got %+v", got)
	}
}

func TestListenerCommandWordsAndShortUnknownNames(t *testing.T) {
	rows := []struct{ sentence, intent, target, refused string }{
		{"macapp redesign ka jawab parho", IntentRead, idMacapp, ""},
		{"macapp redesign ka jawab padhho", IntentRead, idMacapp, ""},
		{"kaar paarking ko bolo ki gaadi nikaalo", IntentTell, "", RefusedNotFound},
		{"Read the last reply of garden party.", IntentRead, "", RefusedNotFound},
		{"garden party ka jawab padho", IntentRead, "", RefusedNotFound},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(r.sentence)
			if !got.Handled || got.Intent != r.intent || targetID(got) != r.target || refusedOf(got) != r.refused {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// Lane-1 run 3: an explicit tell with a short unknown name is "not found"
// even when its words sound like a coding word and the message holds talk
// markers ("karo"); the hand-on rule looks only at the name.
func TestShortUnknownNameInExplicitTellIsNotFound(t *testing.T) {
	for _, s := range []string{
		"piza aardar ko bolo ki jaldi karo.",
		"pizza order ko bolo ki jaldi karo",
		"tell pizza order to hurry up and check the oven",
		"tell car parking to open the gate",
		"पिज़्ज़ा ऑर्डर को बोलो कि जल्दी करो",
		"status of pizza order",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(s)
			if !got.Handled || refusedOf(got) != RefusedNotFound {
				t.Fatalf("got handled=%v intent=%s refused=%s say=%q", got.Handled, got.Intent, refusedOf(got), got.Say)
			}
		})
	}
}

// Feature sweep F10: the one yes list (documented in docs/voice-desk.md).
func TestSweepYesList(t *testing.T) {
	for _, y := range []string{"yeah", "yep", "sure", "ok", "yes please", "go ahead", "do it", "theek hai", "kar do"} {
		if k, _ := classifyAnswer(y); k != AnswerYes {
			t.Errorf("%q is not a yes", y)
		}
	}
}

// Feature sweep F9: --dry-run is said back on every path, also not-mine.
func TestDryRunFlagOnNotMinePaths(t *testing.T) {
	for _, s := range []string{"read kubernetes", "find voice test 0956", "hello there"} {
		h := newHarness(t)
		if got := h.do(Request{Sentence: s, DryRun: true}); got.Handled || !got.DryRun {
			t.Errorf("%q: %+v", s, got)
		}
	}
}
