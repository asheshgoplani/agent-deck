package voicedesk

import "testing"

const idOtherProf = "p9000009-1700000099"

// Open page never opens a page of another profile's session, by name or
// through --on (review M1).
func TestRework3176OpenPageKeepsToThisProfile(t *testing.T) {
	h, pf := pageHarness(t)
	h.addSession(Session{ID: idOtherProf, Title: "otter-pages", Group: "team/alex/workers", Profile: "otherprof", Status: "idle", LastActivity: t0})
	pf.pages[idOtherProf] = Page{Title: "Otter page", Path: "/x/o.html", Where: "mac"}
	h.do(Request{Sentence: "open the page from otter pages", Profile: "work"})
	h.do(Request{Sentence: "open the page you made", Profile: "work", On: idOtherProf})
	for _, id := range pf.opened {
		if id == idOtherProf {
			t.Errorf("opened a page of another profile: %v", pf.opened)
		}
	}
}

// A launch never takes a group, or a profile, from another profile's
// sessions (review M2).
func TestRework3176LaunchKeepsToThisProfile(t *testing.T) {
	h := phase2Harness(t)
	h.addSession(Session{ID: idOtherProf, Title: "otter-one", Group: "team/alex/otterg", Profile: "otherprof", Status: "idle", LastActivity: t0})
	q := h.do(Request{Sentence: "start a new session called zz top in otterg", Profile: "work"})
	if q.Confirm != nil {
		h.yes(q)
	}
	for _, l := range h.deck.launched {
		if l.Profile == "otherprof" {
			t.Errorf("voice launch went to another profile: %+v", l)
		}
	}
}

// A yes refused because the row is a conductor or the mic's session now
// says why in refused (review N1).
func TestRework3176RefusedAtYesSetsRefused(t *testing.T) {
	h := phase2Harness(t)
	q := h.say("stop voice test")
	if q.Confirm == nil {
		t.Fatalf("setup: %q", q.Say)
	}
	r := h.do(Request{Sentence: "yes", Pending: q.Confirm.Token, On: idVoiceTest, AnswerDelayMs: 800, HasAnswerDelay: true})
	if refusedOf(r) != RefusedUnsafe {
		t.Errorf("refused=%q say=%q, want %q", refusedOf(r), r.Say, RefusedUnsafe)
	}
}
