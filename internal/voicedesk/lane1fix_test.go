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
