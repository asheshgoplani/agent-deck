package voicedesk

import (
	"context"
	"testing"
)

// Rework round 1 of the re-review (NB1, NS1-NS4, S1, N-bidi).

// NB1: an answer is a row of the documented table, exactly. Words that only
// sound like an answer never act.
func TestNearMissAnswersNeverAct(t *testing.T) {
	near := []string{
		// English near-misses of yes / send / sure / ok
		"sorry", "Sorry.", "sore", "share", "shore", "sure thing maybe", "sir", "sue", "shure", "yess", "yeas", "yea",
		"yes plus", "yes please not", "yet", "jess", "less", "guess", "chess", "ha", "hah", "aha", "huh", "hey",
		"sand it", "sound it", "send", "sent it", "bend it", "lend it", "okey", "oak", "okra", "o", "k",
		"go head", "go a head", "do eat", "due it", "yep yep yep", "nope nope", "now", "know", "noo", "nah",
		// Roman Hindi near-misses
		"jee", "bhaji", "bhaaji", "car do", "kar doo", "haanji haanji", "han han han", "ham", "hum", "haath", "haar",
		"bhejiye", "bhej", "bheja", "theek", "theek hai na", "thik", "na", "nahi to", "rahne", "mat",
		// Devanagari / Urdu near-misses
		"हम", "हाथ", "हार", "भेजा", "भेज", "ठीक", "जी जी जी", "नही नही", "ہم", "ہاتھ",
	}
	if len(near) < 60 {
		t.Fatalf("table too small: %d", len(near))
	}
	for _, w := range near {
		if k, _ := classifyAnswer(w); k == AnswerYes || k == AnswerNo || k == AnswerChoice {
			t.Errorf("%q classified as %q", w, k)
		}
	}
	for _, w := range near {
		h := newHarness(t)
		ask := h.say("tell phone build to run the tests")
		h.do(Request{Sentence: w, Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
		if len(h.deck.sent) != 0 {
			t.Errorf("%q sent the message", w)
		}
	}
}

// NB1: every documented row is still an answer.
func TestDocumentedAnswersAct(t *testing.T) {
	for _, w := range []string{"yes", "yes send it", "okay", "sure", "go ahead", "do it", "haan", "haa", "han", "haan ji",
		"ji haan", "haan bhejo", "bhej do", "bhejdo", "theek hai", "thik hai", "kar do", "हाँ", "हां भेजो", "ہاں", "جی ہاں",
		"Yes.", "Jarvis, yes", "YES SEND"} {
		if k, _ := classifyAnswer(w); k != AnswerYes {
			t.Errorf("%q is not a yes", w)
		}
	}
	for _, w := range []string{"no", "cancel", "nahin", "nahi", "nahee", "rehne do", "rahne do", "mat bhejo", "नहीं", "رہنے دو"} {
		if k, _ := classifyAnswer(w); k != AnswerNo {
			t.Errorf("%q is not a no", w)
		}
	}
	for w, want := range map[string]int{"first": 0, "pehla": 0, "pahla": 0, "doosra": 1, "dusra": 1, "teesra": 2, "tisra": 2, "दूसरा": 1} {
		if k, i := classifyAnswer(w); k != AnswerChoice || i != want {
			t.Errorf("%q = %s %d", w, k, i)
		}
	}
}

// NS1: the second short miss is handled (never typed into the session the
// mic is on) and cancels.
func TestSecondMissCancels(t *testing.T) {
	h := newHarness(t)
	ask := h.say("tell phone build to run the tests")
	again := h.do(Request{Sentence: "hmm", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	got := h.do(Request{Sentence: "hmm", Pending: again.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
	if !got.Handled || got.Say != "Cancelled, nothing sent." || got.Confirm != nil || len(h.deck.sent) != 0 {
		t.Fatalf("got %+v", got)
	}
}

// NS3: a send that failed before typing says so; only an unknown outcome
// says "not sure"; a target without a prompt is not "asking a question".
func TestSendErrorsAreSaidTruly(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{ErrNotSent, "Could not send to conductor deck alex."},
		{ErrNoPrompt, "Conductor deck alex is not at its prompt, so nothing was sent; try again when it is."},
		{ErrUnconfirmed, "I am not sure it was sent; check conductor deck alex."},
	} {
		h := newHarness(t)
		h.desk.Deck = &flakySend{fakeDeck: h.deck, err: c.err}
		got := h.yes(h.say("tell deck conductor to check the build"))
		if got.Say != c.want {
			t.Errorf("%v: said %q", c.err, got.Say)
		}
	}
}

// NS4: an explicit tell or status whose unknown name carries a number is a
// session reference: not found, even when it holds a coding word.
func TestUnknownNumberedCodingNameIsNotFound(t *testing.T) {
	for _, s := range []string{"tell build 0995 to go", "tell build 0957 to stop", "status of build 955", "build 0995 ko bolo ki ruk jao"} {
		h := newHarness(t)
		got := h.say(s)
		if !got.Handled || refusedOf(got) != RefusedNotFound {
			t.Errorf("%q: handled=%v refused=%q", s, got.Handled, refusedOf(got))
		}
	}
}

// S1: one word that fits part of several session names is talk, not a
// question ("tell the deck to shuffle before dealing").
func TestOneWordPartOfSeveralNamesIsTalk(t *testing.T) {
	for _, s := range []string{"tell the deck to shuffle before dealing", "deck ko bolo shuffle kare", "conductor se pucho kitne ticket bache",
		"ask the conductor to hand out tickets in the story"} {
		h := newHarness(t)
		h.addSession(Session{ID: "e1000009-1700000039", Title: "deck-conductor-2", Group: "team/alex", Profile: "work", Status: "idle"})
		if got := h.say(s); got.Handled {
			t.Errorf("%q: %+v", s, got)
		}
	}
	// a whole short name still asks or resolves
	h := newHarness(t)
	if got := h.say("status of conductor deck alex"); targetID(got) != idConductor {
		t.Errorf("whole name: %+v", got)
	}
}

var _ = context.Background
