package voicedesk

// Re-review round 2: R2-1 (a question back is never a yes) and R2-6 (an
// explicit tell to a shared short name is asked), from the reviewer's
// suite, plus the 27 shared-name forms and the lead's question-mark rows.

import "testing"

func r2Ask(t *testing.T, h *harness) Result {
	t.Helper()
	ask := h.say("tell phone build to run the tests")
	if ask.Confirm == nil {
		t.Fatalf("no question asked: %+v", ask)
	}
	return ask
}

func r2Answer(h *harness, token, s string) Result {
	return h.do(Request{Sentence: s, Pending: token, AnswerDelayMs: 500, HasAnswerDelay: true})
}

func TestR2QuestionBackIsNeverAYes(t *testing.T) {
	for _, w := range []string{"yes?", "yes?!", "yes!?", "yes?.", "\"yes?\"", "(yes?)", "yes？", "yes؟", "ہاں؟", "جی ہاں؟", "ہاں بھیجو؟", "haan?!", "हाँ?!", "हाँ？", "ہاں؟‏", "yes?​", "send it?!", "ok?!"} {
		t.Run(w, func(t *testing.T) {
			h := newHarness(t)
			got := r2Answer(h, r2Ask(t, h).Confirm.Token, w)
			if len(h.deck.sent) != 0 || got.Intent == IntentConfirmYes {
				t.Fatalf("%q was taken as a yes: intent=%s say=%q sent=%d", w, got.Intent, got.Say, len(h.deck.sent))
			}
		})
	}
}

func TestR2ExplicitTellToASharedShortNameIsAsked(t *testing.T) {
	for _, s := range []string{"tell conductor to check the build", "tell conductor to push to main", "conductor ko bolo ki build dekho", "ask tailscale to restart the daemon"} {
		h := newHarness(t)
		if got := h.say(s); !got.Handled {
			t.Errorf("%q is handed on (handled:false)", s)
		}
	}
}

// The lead's question-mark rows: re-asked once, then cancelled; never sent.
func TestQuestionMarkAnywhereIsNotAnAnswer(t *testing.T) {
	for _, w := range []string{"ہاں؟", "جی ہاں؟", "ہاں بھیجو؟", "Yes?!", "haan?!", "हाँ?!", "\"yes?\"", "(yes?)", "yes?.", "yes？",
		"yes?\u200f", "\u200fyes?", "yes\u200b?", "ye\u200bs?", "yes⁇", "yes‽", "¿yes?", "haan ji?", "ok send it?"} {
		t.Run(w, func(t *testing.T) {
			if k, _ := classifyAnswer(w); k != AnswerNone {
				t.Fatalf("%q classifies as %q", w, k)
			}
			h := newHarness(t)
			first := r2Answer(h, r2Ask(t, h).Confirm.Token, w)
			if len(h.deck.sent) != 0 || first.Confirm == nil || first.Say != "Please say yes or no." {
				t.Fatalf("first: %+v sent=%d", first, len(h.deck.sent))
			}
			second := r2Answer(h, first.Confirm.Token, w)
			if !second.Handled || second.Say != "Cancelled, nothing sent." || len(h.deck.sent) != 0 {
				t.Fatalf("second: %+v sent=%d", second, len(h.deck.sent))
			}
		})
	}
	// a plain answer with a zero-width or bidi mark (no question mark) is still the answer
	for _, w := range []string{"yes\u200b", "\u200fہاں", "haan\u200d"} {
		if k, _ := classifyAnswer(w); k != AnswerYes {
			t.Errorf("%q is not a yes", w)
		}
	}
}

// The 27 shared-name forms of the round-2 review: with billing dashboard,
// billing report, deck conductor, deck conductor 2, velvet sink and velvet
// sink 2 (all his), a name that fits two asks which one; a whole name reads
// back; an unknown one is not found. None is handed on.
func TestSharedNameFormsAreAsked(t *testing.T) {
	const which = "choice"
	rows := []struct{ sentence, kind, say string }{
		{"What is the status of billing?", which, "Did you mean billing dashboard or billing report?"},
		{"biling ka status kya hai?", which, "Did you mean billing dashboard or billing report?"},
		{"Read billing.", which, "Did you mean billing dashboard or billing report?"},
		{"Tell billing to send the invoice.", which, "Did you mean billing dashboard or billing report?"},
		{"biling ko bolo ki report banaao", which, "Did you mean billing dashboard or billing report?"},
		{"billing ko bolo ki report banao", which, "Did you mean billing dashboard or billing report?"},
		{"tell conductor to check the build", which, "Did you mean deck conductor or deck conductor 2?"},
		{"tell conductor to push to main", which, "Did you mean deck conductor or deck conductor 2?"},
		{"ask conductor to merge the branch", which, "Did you mean deck conductor or deck conductor 2?"},
		{"conductor ko bolo ki build dekho", which, "Did you mean deck conductor or deck conductor 2?"},
		{"कंडक्टर को बोलो कि बिल्ड देखो", which, "Did you mean deck conductor or deck conductor 2?"},
		{"status of conductor", which, "Did you mean deck conductor or deck conductor 2?"},
		{"how is conductor doing", which, "Did you mean deck conductor or deck conductor 2?"},
		{"read conductor", which, "Did you mean deck conductor or deck conductor 2?"},
		{"what did conductor say", which, "Did you mean deck conductor or deck conductor 2?"},
		{"conductor ka status kya hai", which, "Did you mean deck conductor or deck conductor 2?"},
		{"tell velvet to say hi", which, "Did you mean velvet sink or velvet sink 2?"},
		{"tell the velvet to say hi", which, "Did you mean velvet sink or velvet sink 2?"},
		{"tell deck to stop", which, "Did you mean deck conductor or deck conductor 2?"},
		{"tell the billing sessions to stop", which, "Did you mean billing dashboard or billing report?"},
		{"find conductor", which, "Two: deck conductor, deck conductor 2. First or second?"},
		{"find billing", which, "Two: billing dashboard, billing report. First or second?"},
		{"find the billing session", which, "Two: billing dashboard, billing report. First or second?"},
		{"tell billing dashboard to send the invoice", "yes", "Send to billing dashboard: send the invoice. Yes?"},
		{"tell billing report to stop", "yes", "Send to billing report: stop. Yes?"},
		{"tell deck conductor to check the build", "yes", "Send to deck conductor: check the build. Yes?"},
		{"tell billing two to stop", "", "I found no session of yours called billing two."},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h := newHarness(t)
			h.deck.sessions = []Session{
				{ID: "s1", Title: "billing-dashboard", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s2", Title: "billing-report", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s3", Title: "deck-conductor", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s4", Title: "deck-conductor-2", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s5", Title: "velvet-sink", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s6", Title: "velvet-sink-2", Group: "team/alex", Profile: "work", Status: "idle"},
				{ID: "s7", Title: "timing-probe", Group: "team/alex", Profile: "work", Status: "idle"},
			}
			got := h.do(Request{Sentence: r.sentence, On: "s7"})
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if !got.Handled || kind != r.kind || got.Say != r.say {
				t.Fatalf("handled=%v confirm=%q say=%q, want %q %q", got.Handled, kind, got.Say, r.kind, r.say)
			}
		})
	}
}
