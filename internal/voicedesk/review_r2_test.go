package voicedesk

// Re-review r2 (PR 3176, 23055ad4) cases, kept as regression tests. Each
// asserts the safe or wanted behaviour.

import (
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/needsme"
)

func r2Acts(h *harness) string { return strings.Join(h.deck.acts, ",") }

type g3row struct {
	items []string
	s     string
	want  string // "talk" (not handled, nothing ticked), "ask" (question, nothing ticked), "refuse" (handled, no question, nothing ticked), "tick:<id>"
}

func g3probe(t *testing.T, rows []g3row) {
	t.Helper()
	for _, row := range rows {
		t.Run(row.s+" | "+strings.Join(row.items, ","), func(t *testing.T) {
			h, tf := todoHarness(t)
			tf.items = nil
			for i, it := range row.items {
				tf.items = append(tf.items, Todo{ID: string(rune('1' + i)), Text: it})
			}
			r := h.say(row.s)
			got := "talk"
			switch {
			case len(tf.done) > 0:
				got = "tick:" + strings.Join(tf.done, ",")
			case r.Confirm != nil:
				got = "ask"
			case r.Handled:
				got = "refuse"
			}
			t.Logf("%q items=%v -> %s intent=%s say=%q", row.s, row.items, got, r.Intent, r.Say)
			if got != row.want {
				t.Errorf("%q items=%v: got %s (say=%q), want %s", row.s, row.items, got, r.Say, row.want)
			}
		})
	}
}

func TestR2G3StemAndPrefix(t *testing.T) {
	g3probe(t, []g3row{
		{[]string{"pay rental insurance"}, "tick off pay rent", "refuse"},
		{[]string{"pay rental insurance"}, "tick off rent", "refuse"},
		{[]string{"pay rental insurance"}, "pay rent insurance is done", "talk"},
		{[]string{"pay rental insurance"}, "tick off pay rentals insurance", "tick:1"}, // a plain plural, as the CLI
		{[]string{"book the plane"}, "tick off plan", "refuse"},
		{[]string{"book the plane"}, "tick off plans", "talk"}, // not a word of it: talk, nothing ticked (safe)
		{[]string{"book the plane"}, "book the plan is done", "talk"},
		{[]string{"book the plane"}, "tick off book the plan", "refuse"},
		{[]string{"book the planes"}, "tick off book the plane", "tick:1"},
		// CLI: "es" only after s/x/z/ch/sh; "jon"/"jones", "tim"/"times" are no match
		{[]string{"call jones"}, "call jon is done", "talk"},
		{[]string{"call jones"}, "tick off call jon", "refuse"},
		{[]string{"email tim"}, "tick off email times", "talk"},
		// sound-alikes
		{[]string{"pay the bill"}, "tick off pay the build", "talk"}, // a sound-alike: nothing ticked (safe)
		{[]string{"fix the build"}, "tick off fix the bill", "talk"},
		// two items fit
		{[]string{"call the bank", "call the bank manager"}, "tick off bank", "ask"},
		{[]string{"call the bank", "call bank"}, "call the bank is done", "ask"},
		{[]string{"pay rent", "pay rental insurance"}, "tick off pay rent", "tick:1"},
	})
}

func TestR2G3EsStem(t *testing.T) {
	g3probe(t, []g3row{
		{[]string{"book the planes"}, "book the plan is done", "talk"},
		{[]string{"book the planes"}, "tick off book the plan", "refuse"},
		{[]string{"book the planes"}, "tick off plan", "refuse"},
		{[]string{"update the notes"}, "update the not is done", "talk"},
	})
}

func TestR2DeleteMoreForms(t *testing.T) {
	for _, s := range []string{
		// English, polite and inflected
		"can you delete beta notes", "could you please remove beta notes", "please delete beta notes", "delete beta notes please",
		"delete beta notes now", "go ahead and delete beta notes", "i want to delete beta notes", "i want you to delete beta notes",
		"let's delete beta notes", "you can delete beta notes", "beta notes can be deleted", "beta notes should be deleted",
		"get beta notes deleted", "deleting beta notes", "delete beta notes for me", "remove beta notes from the deck",
		"remove beta notes from agent deck", "purge beta notes", "scrap beta notes", "discard beta notes", "throw away beta notes",
		"kill and remove beta notes", "delete beta note", "delete the beta notes one", "delete worker one", "delete test 1",
		"delete tailscale fix on boxone", "delete tailscale notes", "delete release notes", "delete voice plan 1109",
		"stop beta notes then delete it", "archive beta notes and then remove it",
		// Roman Hindi
		"beta notes ko hatwa do", "beta notes ko mitwa do", "beta notes hata dijiye", "beta notes ko delete kar dijiye",
		"beta notes ko remove karo", "beta notes ko delete karna hai", "mujhe beta notes delete karna hai", "beta notes delete kar dena",
		"beta notes ko saaf karo", "beta notes ko phenk do", "beta notes hatao na", "beta notes ko hata do please", "beta notes ko hata dena",
		"beta notes wala session hata do", "beta notes waala hata do", "jo beta notes hai usko hata do", "beta notes ko uda do",
		// Devanagari
		"बीटा नोट्स रिमूव करो", "बीटा नोट्स को हटाइए", "बीटा नोट्स को डिलीट कर दीजिए", "बीटा नोट्स मिटा दीजिए", "बीटा नोट्स को हटा दीजिये",
		"बीटा नोट्स निकाल दो", "बीटा नोट्स को उड़ा दो", "बीटा नोट्स वाला सेशन हटा दो", "बीटा नोट्स को खत्म करो",
		// all
		"delete everything", "delete all sessions", "delete all my sessions", "remove every worker", "sab kuch hata do", "सब हटा दो",
		"sab sessions delete karo", "सारे सेशन डिलीट करो", "delete all the tailscale sessions",
		// conductor / another person
		"delete the conductor", "delete conductor deck alex", "conductor hata do", "delete deck conductor",
		"delete sam macdesign", "sam macdesign hata do", "delete all of sam's sessions", "delete sam's sessions", "sam ke sessions hata do",
	} {
		h := deleteHarness(t)
		r := h.say(s)
		if len(h.deck.acts) != 0 || len(h.deck.sent) != 0 || len(h.deck.launched) != 0 {
			t.Errorf("%q ACTED: %v %v %v", s, h.deck.acts, h.deck.sent, h.deck.launched)
		}
		if !r.Handled || refusedOf(r) == "" {
			t.Errorf("%q not refused: handled=%v intent=%s refused=%s say=%q (handed to the mic's session)", s, r.Handled, r.Intent, refusedOf(r), r.Say)
		} else {
			t.Logf("ok %q -> %s %q", s, refusedOf(r), r.Say)
		}
	}
}

func TestR2DeleteCodingTalk(t *testing.T) {
	for _, s := range []string{
		"delete the session cookie", "remove the session token", "delete the voice test file", "remove the test session fixture",
		"delete the release notes draft", "delete the old phone build script", "remove the beta flag", "delete the notes",
		"remove the notes", "delete the tests", "remove the workers", "delete the worker pool", "remove the audit log",
		"hata do ye test", "ye notes hata do", "release notes se ye line hata do", "delete the voice test", "delete all the tests",
		"remove all the logs", "delete all the files", "sab files hata do", "delete everything in the tmp folder",
		"remove the build", "delete the build folder", "drop the tables", "delete the branch", "remove the remote",
	} {
		h := deleteHarness(t)
		r := h.say(s)
		t.Logf("%-42q handled=%v refused=%s say=%q", s, r.Handled, refusedOf(r), r.Say)
		if len(h.deck.acts) != 0 {
			t.Errorf("%q acted %v", s, h.deck.acts)
		}
	}
}

func TestR2BulkRenamedConductor(t *testing.T) {
	h := phase2Harness(t)
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idConductor {
			h.deck.sessions[i].Title = "deck-alex" // renamed conductor, conductor_name stays deck-alex
			h.deck.sessions[i].Conductor = true    // what the deck says (list --json conductor_name)
			h.deck.sessions[i].Group = "team/alex/workers"
			h.deck.sessions[i].Status = "running"
		}
	}
	q := h.say("stop all workers")
	t.Logf("say=%q", q.Say)
	if q.Confirm != nil && strings.Contains(sayAndShow(q), idConductor) {
		h.yes(q)
		t.Errorf("a renamed conductor (title deck-alex) is in the bulk stop list: %q acts=%s", q.Say, r2Acts(h))
	}
}

func TestR2G1SearchOtherProfileSpoken(t *testing.T) {
	h := phase2Harness(t)
	h.deck.sessions = append(h.deck.sessions, Session{ID: "e1000001-1700000031", Title: "scratch-probe", Group: "team/alex", Profile: "scratch", Status: "idle"})
	h.deck.hits = []SearchHit{{Title: "scratch-probe", DeckID: "e1000001-1700000031", Snippet: "invoice in the scratch profile"}}
	r := h.do(Request{Sentence: "where did we talk about the invoice", Profile: "work"})
	t.Logf("profile work, hit from profile scratch -> say=%q show=%+v", r.Say, r.Show.Items)
	if strings.Contains(r.Say, "scratch") {
		t.Errorf("a hit from another profile (scratch) is spoken while voice runs in profile work: %q", r.Say)
	}
}

func TestR2G1SearchNoOwnerConfigAnyProfile(t *testing.T) {
	h := phase2Harness(t)
	h.desk.Config = Config{}
	h.deck.sessions = append(h.deck.sessions, Session{ID: "e1000002-1700000032", Title: "other-profile-chat", Group: "", Profile: "personal", Status: "idle"})
	h.deck.hits = []SearchHit{{Title: "other-profile-chat", DeckID: "e1000002-1700000032", Snippet: "invoice personal"}}
	r := h.do(Request{Sentence: "where did we talk about the invoice", Profile: "work"})
	t.Logf("no owner config, hit from profile personal -> say=%q", r.Say)
	if strings.Contains(r.Say, "other profile chat") {
		t.Errorf("hit from profile personal spoken while voice runs in profile work: %q", r.Say)
	}
}

func TestR2G4LaunchFlagNameRefused(t *testing.T) {
	for _, s := range []string{
		"start a new session called -p other",
		"ek nayi session shuru karo naam -p other",
		"नया सेशन शुरू करो नाम --cmd bash",
		"start a new session called ．．／．．／etc／passwd",
		"start a new session called –p other",
	} {
		h := phase2Harness(t)
		r := h.say(s)
		if r.Confirm != nil || !strings.Contains(r.Say, "plain words") {
			t.Errorf("%q: say %q confirm=%v", s, r.Say, r.Confirm != nil)
		}
	}
}

func g6Obs(t *testing.T, label string, r Result) {
	t.Helper()
	then := "<nil>"
	if r.Then != nil {
		then = r.Then.Action + " -> " + r.Then.Target.ID + " (" + r.Then.Target.Title + ", profile " + r.Then.Target.Profile + ")"
	}
	conf := "<nil>"
	if r.Confirm != nil {
		conf = r.Confirm.Kind
	}
	t.Logf("OBS %-28s handled=%v intent=%q say=%q confirm=%s then=%s refused=%q", label, r.Handled, r.Intent, r.Say, conf, then, refusedOf(r))
}

func TestR2G6_2b_OtherProfileNext(t *testing.T) {
	for name, cfg := range scopeConfigs() {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.desk.Config = cfg
			h.addSession(Session{ID: "p2000001-1700000001", Title: "otter-notes", Group: "team/alex", Profile: "otherprof", Status: "waiting",
				Reason: "asks a question", WaitingSeconds: 99999, LastActivity: t0})
			for _, s := range []string{"take me to the next one", "agla wala kholo"} {
				got := h.do(Request{Sentence: s, Profile: "work"})
				g6Obs(t, s, got)
				if got.Then != nil && got.Then.Target.Profile == "otherprof" {
					t.Errorf("SAFETY %q from profile work -> then.switch to profile otherprof: say=%q", s, got.Say)
				}
			}
		})
	}
}

func TestR2G6_3_ItWaitingForYou(t *testing.T) {
	reason := needsme.Reason("", "")
	t.Logf("OBS needsme.Reason(\"\", \"\") = %q", reason)
	for _, sub := range []string{"", "idle-at-empty-prompt", "model-unavailable", "account-blocked", "process-exited", "auth-401"} {
		h := newHarness(t)
		h.addSession(Session{ID: "w4000001-1700000001", Title: "garden-planner", Group: "team/alex", Profile: "work", Status: "waiting",
			Substate: sub, Reason: needsme.Reason(sub, ""), WaitingSeconds: 99999, LastActivity: t0})
		got := h.say("take me to the next one")
		g6Obs(t, "next (substate "+sub+")", got)
		if strings.Contains(got.Say, ", it waiting for you") || strings.Contains(got.Say, ", it model unavailable") ||
			strings.Contains(got.Say, ", it account blocked") || strings.Contains(got.Say, ", it process exited") {
			t.Errorf("SAY ungrammatical: %q", got.Say)
		}
	}
}
