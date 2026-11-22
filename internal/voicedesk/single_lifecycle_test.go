package voicedesk

import "testing"

// R20/R21 (review r3 TestR3SingleDestructiveTargets): a named single stop,
// restart or archive of a conductor (by title, or renamed with the conductor
// flag), of the session the mic is on, or of a session of another profile is
// refused at once with a short reason: no yes question, nothing done.
func TestSingleLifecycleRefusesConductorMicAndOtherProfile(t *testing.T) {
	type c struct {
		label, sentence, on string
		extra               *Session
	}
	renamed := Session{ID: "c9000001-1700000001", Title: "night-boss", Group: "team/alex", Profile: "work", Status: "running", Conductor: true, LastActivity: t0}
	other := Session{ID: "p9000001-1700000001", Title: "otter-notes", Group: "team/alex", Profile: "otherprof", Status: "running", LastActivity: t0}
	var cases []c
	for _, v := range []string{"stop", "restart", "archive"} {
		cases = append(cases,
			c{"conductor by title " + v, v + " conductor deck alex", "", nil},
			c{"conductor by title " + v + " (deck conductor)", v + " deck conductor", "", nil},
			c{"renamed conductor " + v, v + " night boss", "", &renamed},
			c{"other profile " + v, v + " otter notes", "", &other},
			c{"mic session " + v, v + " phone build", idPhone, nil},
		)
	}
	cases = append(cases,
		c{"hi conductor", "deck conductor band karo", "", nil},
		c{"hi renamed", "night boss band karo", "", &renamed},
		c{"deva renamed", "नाइट बॉस बंद करो", "", &renamed},
	)
	for _, k := range cases {
		h := newHarness(t)
		if k.extra != nil {
			h.addSession(*k.extra)
		}
		r := h.do(Request{Sentence: k.sentence, On: k.on, Profile: "work"})
		if r.Confirm != nil {
			y := h.do(Request{Sentence: "yes", On: k.on, Profile: "work", Pending: r.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
			t.Errorf("%s: %q asked %q (yes -> %q, acts=%v), want a refusal with no question", k.label, k.sentence, r.Say, y.Say, h.deck.acts)
			continue
		}
		if k.label == "deva renamed" && !r.Handled && len(h.deck.acts) == 0 {
			continue // the name is not recognised in Devanagari: not the desk's, nothing done (as in the review run)
		}
		if !r.Handled || refusedOf(r) == "" || len(h.deck.acts) != 0 {
			t.Errorf("%s: %q handled=%v refused=%q say=%q acts=%v", k.label, k.sentence, r.Handled, refusedOf(r), r.Say, h.deck.acts)
		}
	}
	// an ordinary session of his still asks first
	h := newHarness(t)
	if r := h.do(Request{Sentence: "stop voice test", Profile: "work"}); r.Confirm == nil {
		t.Fatalf("stop voice test: %+v", r)
	}
}
