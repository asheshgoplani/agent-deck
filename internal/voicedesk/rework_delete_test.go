package voicedesk

import "testing"

// deleteHarness: the phase 2 fixture plus the sessions of the reviewer's
// live run ("beta notes" of his, "sam review" of Sam).
func deleteHarness(t *testing.T) *harness {
	h := phase2Harness(t)
	h.addSession(Session{ID: "a1000099-1700000099", Title: "beta-notes", Group: "team/alex", Status: "idle"})
	h.addSession(Session{ID: "c1000099-1700000099", Title: "sam-review", Group: "team/sam", Status: "idle"})
	return h
}

// M3: every delete-class sentence of the reviewer's 81 that names one of
// his sessions, a group of his, "all workers", or a session on the deck is
// refused by the desk (handled, refused, nothing done), never handed on
// as talk to the session the mic is on.
func TestReworkDeleteFormsRefused(t *testing.T) {
	for _, s := range []string{
		// the reviewer's own test (TestReviewDesk2DeleteFormsRefused)
		"delete voice test", "remove voice test", "delete the voice test", "voice test delete karo", "voice test hata do", "voice test ko hata do",
		"voice test mita do", "voice test band karo aur hata do", "delete all workers", "sab workers hata do", "वॉइस टेस्ट हटा दो", "voice test session hatao",
		// evidence/a-e2e-04-delete-refuse.txt, every delete-class row
		"delete the beta notes session", "delete beta notes", "delete the beta notes", "remove beta notes", "remove session beta notes",
		"remove the beta notes session", "delete session beta notes", "please delete the beta notes session", "can you delete the beta notes session",
		"delete beta notes session permanently", "destroy beta notes", "wipe beta notes", "erase the beta notes session", "get rid of beta notes",
		"trash beta notes", "drop the beta notes session", "nuke beta notes", "delete all workers", "remove all workers", "delete all the workers",
		"kill all workers and delete them", "stop beta notes and delete it", "stop and delete beta notes", "archive and delete beta notes",
		"beta notes session delete karo", "beta notes session hata do", "beta notes session mita do", "beta notes delete karo", "beta notes hata do",
		"beta notes ko hata do", "beta notes mita do", "beta notes ko delete kar do", "beta notes band karo aur hata do",
		"beta notes ko band karo aur hata do", "beta notes band karo aur delete kar do", "beta notes band karo aur hata do session",
		"beta notes khatam karo", "beta notes ko khatam kar do", "beta notes nikal do", "beta notes ko nikaal do", "beta notes udaa do",
		"sab workers hata do", "saare workers delete karo", "sab workers mita do", "बीटा नोट्स सेशन डिलीट करो", "बीटा नोट्स सेशन हटा दो",
		"बीटा नोट्स डिलीट करो", "बीटा नोट्स हटा दो", "बीटा नोट्स बंद करो और हटा दो", "बीटा नोट्स को मिटा दो", "सब वर्कर्स हटा दो",
		"dilit the beta notes session", "dileet beta notes session", "beta notes session dilit karo", "beta notes seshan hata do",
		"beta notes session hataa do", "beta notes session hatao", "rimoov beta notes session",
		"delete the sam review session", "sam review session hata do", "delete the stray session",
		// a group of his
		"delete the workers group", "workers hata do",
	} {
		t.Run(s, func(t *testing.T) {
			h := deleteHarness(t)
			r := h.say(s)
			if len(h.deck.acts) != 0 || len(h.deck.sent) != 0 || len(h.deck.launched) != 0 {
				t.Fatalf("%q acted: acts=%v sent=%v launched=%v", s, h.deck.acts, h.deck.sent, h.deck.launched)
			}
			if !r.Handled || refusedOf(r) == "" || r.Confirm != nil || r.Intent != IntentRefuse {
				t.Fatalf("%q is not refused by the desk (handled=%v intent=%s refused=%s confirm=%v say=%q): it would be typed into the session the mic is on",
					s, r.Handled, r.Intent, refusedOf(r), r.Confirm != nil, r.Say)
			}
		})
	}
}

// M3: the spoken lines of the refusals.
func TestReworkDeleteRefusalLines(t *testing.T) {
	for _, c := range []struct{ s, say, target string }{
		{"delete beta notes", "I never delete a session by voice; do it on screen.", "a1000099-1700000099"},
		{"beta notes band karo aur hata do", "I never delete a session by voice; do it on screen.", "a1000099-1700000099"},
		{"delete all workers", "I never delete sessions by voice; do it on screen.", ""},
		{"delete the sam review session", "That is Sam's session.", ""},
		{"delete the workers group", "I never delete sessions by voice; do it on screen.", ""},
	} {
		h := deleteHarness(t)
		r := h.say(c.s)
		if r.Say != c.say || targetID(r) != c.target {
			t.Errorf("%q: say=%q target=%s, want %q target=%s", c.s, r.Say, targetID(r), c.say, c.target)
		}
	}
}

// M3 / S1: ordinary coding talk with a delete verb stays talk; so do the
// rows of the reviewer's table that are not delete-class.
func TestReworkDeleteTalkStaysTalk(t *testing.T) {
	for _, s := range []string{
		"delete the session cookie", "remove the unused import", "remove the unused imports", "delete this line", "hata do ye function",
		"ye function hata do", "delete it", "isko hata do", "band karo aur hata do", "delete the old branch", "delete the build session",
		"remove session handling from the server", "delete the server session data", "remove the debug logs", "drop the table",
		"delete the cache and rebuild", "can you delete the old function in beta notes", "remove the console log from the parser",
		"kill beta notes", "close beta notes", "terminate beta notes", "push beta notes", "git push", "pay the invoice",
	} {
		t.Run(s, func(t *testing.T) {
			h := deleteHarness(t)
			r := h.say(s)
			if r.Handled || len(h.deck.acts) != 0 || len(h.deck.sent) != 0 {
				t.Fatalf("%q taken by the desk: intent=%s refused=%s say=%q", s, r.Intent, refusedOf(r), r.Say)
			}
		})
	}
}

// M3: a tell that holds a delete word stays a tell (never sent without a yes).
func TestReworkDeleteInsideTellStaysTell(t *testing.T) {
	for _, s := range []string{"tell voice test to delete the temp files", "voice test ko bolo sab delete kar de"} {
		h := deleteHarness(t)
		if r := h.say(s); r.Intent != IntentTell {
			t.Errorf("%q: intent=%s say=%q", s, r.Intent, r.Say)
		}
	}
}

// S1: "delete the session cookie" is talk, not a which-session question.
func TestReworkDeleteSessionCookieIsTalk(t *testing.T) {
	h := deleteHarness(t)
	h.addSession(Session{ID: "a1000097-1700000097", Title: "c-bash", Group: "team/alex", Status: "idle"})
	h.addSession(Session{ID: "a1000096-1700000096", Title: "c-rm", Group: "team/alex", Status: "idle"})
	if r := h.say("delete the session cookie"); r.Handled || r.Confirm != nil {
		t.Fatalf("taken: intent=%s say=%q", r.Intent, r.Say)
	}
}
