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

// CONTRACT 4.1 (audio run on 993ebc16): in an explicit desk form, a name
// whose words fit two or more of his sessions asks which one; it is never
// handed on as talk. Only a generic coding word is handed on.
func TestNameFittingSeveralSessionsAsks(t *testing.T) {
	for _, s := range []string{
		"What is the status of billing?", "biling ka status kya hai?", "Read billing.", "biling ka jawab padho",
		"Tell billing to send the invoice.", "biling ko bolo ki report banaao", "बिलिंग को बोलो कि रिपोर्ट बनाओ",
		"ask billing to send the invoice", "billing se kaho ki invoice bhejo", "बिलिंग का स्टेटस क्या है",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			h.addSession(Session{ID: "e1000021-1700000061", Title: "billing-dashboard", Group: "team/alex", Profile: "work", Status: "idle"})
			h.addSession(Session{ID: "e1000022-1700000062", Title: "billing-report", Group: "team/alex", Profile: "work", Status: "idle"})
			got := h.say(s)
			if !got.Handled || got.Confirm == nil || got.Confirm.Kind != "choice" || len(got.Show.Items) != 2 {
				t.Fatalf("not asked: %+v", got)
			}
			if got.Say != "Did you mean billing dashboard or billing report?" {
				t.Errorf("say = %q", got.Say)
			}
		})
	}
}

// Conductor ruling: an explicit tell is never handed on to be typed as
// plain text. No match is "not found", also for coding words.
func TestTellIsNeverHandedOn(t *testing.T) {
	for _, s := range []string{
		"tell zephyr to stop", "tell billing two to stop", "tell build 0957 to stop", "zephyr ko bolo ki ruk jao",
		"tell the deck to shuffle before dealing", "tell velvet to say hi",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			got := h.say(s)
			if !got.Handled || got.Intent != IntentTell {
				t.Fatalf("a tell was handed on: %+v", got)
			}
		})
	}
	// a pronoun or a generic addressee is not a session: handed on
	for _, s := range []string{"tell me what you think about this", "ask him to wait", "tell them to retry",
		"tell everyone to pull", "tell the user to restart the app", "tell the team to review", "tell the client to wait",
		"usko bolo ki ruk jaye", "unko bolo ki ruk jao", "mujhe batao ki kya hua", "sabko bolo ki chhutti hai"} {
		h := newHarness(t)
		if got := h.say(s); got.Handled {
			t.Errorf("addressee %q: %+v", s, got)
		}
	}
}

// The one exception to the tell rule (final review F1): a tell whose
// unknown addressee is a generic coding word is talk to the current
// session, handed on; the 23 sentences the review saw swallowed.
func TestCodingAddresseeTellIsHandedOn(t *testing.T) {
	for _, s := range []string{
		"tell the compiler to ignore unused variables",
		"tell the build script to print the version",
		"tell the tests to run in parallel",
		"tell the notes app to sync on start",
		"tell the conductor class to retry three times",
		"tell the shared box component to use flexbox",
		"tell git to ignore the build folder",
		"tell the logger to write to stderr",
		"tell the parser that tabs are allowed",
		"tell mac app users to update",
		"ask the build server if the cache is warm",
		"ask the database whether the row exists first",
		"ask the notes module to flush",
		"test ko bolo ki fast chale",
		"compiler ko bolo warnings ignore kare",
		"function ko bolo ki null return kare",
		"server se pucho ki cache garam hai kya",
		"database se pucho ki row hai ya nahi",
		"टेस्ट को बोलो कि तेज चले",
		"tell the build script to skip the lint step",
		"ask the linter why it complains",
		"test ko bolo ki fast chale",
		"server se poocho ki kya hua",
	} {
		t.Run(s, func(t *testing.T) {
			h := newHarness(t)
			if got := h.do(Request{Sentence: s, On: idConductor, DryRun: true}); got.Handled {
				t.Fatalf("swallowed: intent=%s refused=%s say=%q", got.Intent, refusedOf(got), got.Say)
			}
		})
	}
}

var _ = context.Background
