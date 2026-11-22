package voicedesk

import (
	"strings"
	"testing"
	"time"
)

// Phase 2 lane-0 tables: launch, stop, restart, archive (each after a
// yes), search what was said, workers, the next one that needs me, and
// "voice never deletes".

func phase2Harness(t *testing.T) *harness {
	h := newHarness(t)
	h.deck.hits = []SearchHit{
		{Title: "billing-notes", DeckID: idTsNotes, // a deck session of his (a hit is shown only then)
			Snippet: "we agreed the invoice goes out on Friday"},
		{Title: "release-notes", DeckID: idRelease, Snippet: "the invoice template needs a logo"},
	}
	h.deck.children = map[string][]Session{
		idConductor: {
			{ID: idVoiceTest, Title: "voice-test", Status: "running"},
			{ID: idPhone, Title: "🎙 phone-build", Status: "waiting"},
		},
	}
	return h
}

func TestPhase2IntentTable(t *testing.T) {
	rows := []struct {
		sentence, intent, lang, target, refused, confirm, say string
		handled                                               bool
	}{
		{"start a new session called test one in workers", IntentLaunch, LangEN, "", "", "yes", "Start a new session test one in team/alex/workers? Yes?", true},
		{"launch a new session named api probe", IntentLaunch, LangEN, "", "", "yes", "Start a new session api probe in team/alex? Yes?", true},
		{"start a new session called test one on boxone", IntentLaunch, LangEN, "", "", "yes", "Start a new session test one in team/alex on boxone? Yes?", true},
		{"ek nayi session shuru karo naam test one", IntentLaunch, LangHI, "", "", "yes", "Start a new session test one in team/alex? Yes?", true},
		{"workers mein ek nayi session shuru karo naam test one", IntentLaunch, LangHI, "", "", "yes", "Start a new session test one in team/alex/workers? Yes?", true},
		{"test one naam ki nayi session banao", IntentLaunch, LangHI, "", "", "yes", "Start a new session test one in team/alex? Yes?", true},
		{"start a new session called phone build", IntentLaunch, LangEN, "", RefusedUnsafe, "", "There is already a session called phone build.", true},
		{"start a new session called test one in nowhere", IntentLaunch, LangEN, "", RefusedNotFound, "", "I found no group of yours called nowhere.", true},

		{"stop voice test", IntentStop, LangEN, idVoiceTest, "", "yes", "Stop voice test? Yes?", true},
		{"voice test band karo", IntentStop, LangHI, idVoiceTest, "", "yes", "Stop voice test? Yes?", true},
		{"voice test ko band kar do", IntentStop, LangHI, idVoiceTest, "", "yes", "Stop voice test? Yes?", true},
		{"stop release notes", IntentStop, LangEN, idRelease, "", "", "release notes is already stopped.", true},
		{"stop conductor deck sam", IntentStop, LangEN, "", RefusedOtherPerson, "", "That is Sam's session.", true},
		{"restart proactive audit", IntentRestart, LangEN, idAudit, "", "yes", "Restart proactive audit? Yes?", true},
		{"proactive audit restart karo", IntentRestart, LangHI, idAudit, "", "yes", "Restart proactive audit? Yes?", true},
		{"proactive audit ko phir se shuru karo", IntentRestart, LangHI, idAudit, "", "yes", "Restart proactive audit? Yes?", true},
		{"archive tailscale notes", IntentArchive, LangEN, idTsNotes, "", "yes", "Archive tailscale notes? Yes?", true},
		{"tailscale notes archive karo", IntentArchive, LangHI, idTsNotes, "", "yes", "Archive tailscale notes? Yes?", true},
		{"stop all workers", IntentStop, LangEN, "", "", "yes", "Stop the first five of seven: phone build, tailscale fix on boxone, macapp redesign1, voice test, proactive audit. Yes?", true},
		{"sab workers band karo", IntentStop, LangHI, "", "", "yes", "Stop the first five of seven: phone build, tailscale fix on boxone, macapp redesign1, voice test, proactive audit. Yes?", true},
		{"restart all the tailscale sessions", IntentRestart, LangEN, "", "", "yes", "Restart two: tailscale fix on boxone, tailscale notes. Yes?", true},

		{"where did we talk about the invoice", IntentSearch, LangEN, "", "", "", "Two conversations: billing notes, release notes.", true},
		{"search what was said about the logo", IntentSearch, LangEN, "", "", "", "One conversation: release notes.", true},
		{"invoice ki baat kahan hui thi", IntentSearch, LangHI, "", "", "", "Two conversations: billing notes, release notes.", true},
		{"where did we talk about kubernetes", IntentSearch, LangEN, "", "", "", "Nothing found about kubernetes.", true},

		{"what are deck conductor's workers doing", IntentWorkers, LangEN, idConductor, "", "", "Two workers: voice test working, phone build waiting for you.", true},
		{"what are the workers of deck conductor doing", IntentWorkers, LangEN, idConductor, "", "", "Two workers: voice test working, phone build waiting for you.", true},
		{"deck conductor ke workers kya kar rahe hain", IntentWorkers, LangHI, idConductor, "", "", "Two workers: voice test working, phone build waiting for you.", true},
		{"what are the workers of conductor boxone doing", IntentWorkers, LangEN, idBoxCond, "", "", "Workers of conductor boxone on boxone are not reachable by voice yet.", true},
		{"what are voice test's workers doing", IntentWorkers, LangEN, idVoiceTest, "", "", "voice test has no workers.", true},

		{"take me to the next one", IntentNext, LangEN, idTsFix, "", "", "Switching to tailscale fix on boxone, it asks a question.", true},
		{"agla wala kholo", IntentNext, LangHI, idTsFix, "", "", "Switching to tailscale fix on boxone, it asks a question.", true},

		{"delete the phone build session", IntentRefuse, LangEN, idPhone, RefusedUnsafe, "", "I never delete a session by voice; do it on screen.", true},
		{"phone build session delete karo", IntentRefuse, LangHI, idPhone, RefusedUnsafe, "", "I never delete a session by voice; do it on screen.", true},
	}
	for _, r := range rows {
		t.Run(r.sentence, func(t *testing.T) {
			h := phase2Harness(t)
			got := h.say(r.sentence)
			kind := ""
			if got.Confirm != nil {
				kind = got.Confirm.Kind
			}
			if got.Handled != r.handled || got.Intent != r.intent || got.Lang != r.lang || targetID(got) != r.target || refusedOf(got) != r.refused || kind != r.confirm || got.Say != r.say {
				t.Fatalf("got handled=%v intent=%s lang=%s target=%s refused=%s confirm=%s\nsay  %q\nwant %q", got.Handled, got.Intent, got.Lang, targetID(got), refusedOf(got), kind, got.Say, r.say)
			}
			if len(h.deck.acts) != 0 || len(h.deck.launched) != 0 || len(h.deck.sent) != 0 {
				t.Fatalf("acted without a yes: acts=%v launched=%v sent=%v", h.deck.acts, h.deck.launched, h.deck.sent)
			}
		})
	}
}

// Sentences near the phase-2 verbs that belong to the daemon or to the
// session: handled:false.
func TestPhase2NotMine(t *testing.T) {
	for _, s := range []string{
		"stop", "stop listening", "stop talking", "restart", "stop the tests", "restart the dev server",
		"stop it", "archive this", "delete the old branch", "remove the unused imports", "kill the process",
		"start a new branch", "make a new file called notes", "what are the workers doing", "music band karo",
		"ye band karo", "next", "search the logs for errors", "where did we put the config", "start the server",
		"sab band karo", "stop all of it",
	} {
		t.Run(s, func(t *testing.T) {
			h := phase2Harness(t)
			got := h.say(s)
			if got.Handled {
				t.Fatalf("stole %q: %+v", s, got)
			}
		})
	}
}

func TestPhase2ActOnlyAfterYes(t *testing.T) {
	t.Run("stop then yes", func(t *testing.T) {
		h := phase2Harness(t)
		got := h.yes(h.say("stop voice test"))
		if got.Say != "Stopped voice test." || len(h.deck.acts) != 1 || h.deck.acts[0] != "stop "+idVoiceTest {
			t.Fatalf("got %+v acts=%v", got, h.deck.acts)
		}
	})
	t.Run("restart and archive", func(t *testing.T) {
		h := phase2Harness(t)
		h.yes(h.say("proactive audit restart karo"))
		h.yes(h.say("archive tailscale notes"))
		if strings.Join(h.deck.acts, ",") != "restart "+idAudit+",archive "+idTsNotes {
			t.Fatalf("acts=%v", h.deck.acts)
		}
	})
	t.Run("stop all workers: five per yes", func(t *testing.T) {
		h := phase2Harness(t)
		got := h.yes(h.say("stop all workers"))
		if len(h.deck.acts) != 5 || got.Say != "Stopped five. Two more are left; say it again for them." {
			t.Fatalf("got %q acts=%v", got.Say, h.deck.acts)
		}
		again := h.say("stop all workers")
		if again.Say != "Stop two: audit 0955, voice plan 1109. Yes?" {
			t.Fatalf("second round: %q", again.Say)
		}
	})
	t.Run("launch then yes", func(t *testing.T) {
		h := phase2Harness(t)
		got := h.yes(h.say("start a new session called test one in workers"))
		if got.Say != "Started test one." || len(h.deck.launched) != 1 {
			t.Fatalf("got %+v launched=%v", got, h.deck.launched)
		}
		l := h.deck.launched[0]
		if l.Title != "test-one" || l.Group != "team/alex/workers" || l.Profile != "work" || l.Host != "" {
			t.Fatalf("launch spec %+v", l)
		}
	})
	t.Run("no, late yes, buried yes: nothing happens", func(t *testing.T) {
		h := phase2Harness(t)
		ask := h.say("stop voice test")
		h.do(Request{Sentence: "nahin", Pending: ask.Confirm.Token})
		ask = h.say("stop all workers")
		h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token, AnswerDelayMs: 10500, HasAnswerDelay: true})
		ask = h.say("start a new session called test one")
		h.now = h.now.Add(11 * time.Second)
		if late := h.do(Request{Sentence: "yes", Pending: ask.Confirm.Token}); late.Say != "Too late, nothing done." {
			t.Errorf("late launch yes said %q", late.Say)
		}
		ask = h.say("restart proactive audit")
		h.do(Request{Sentence: "yes and also push the branch", Pending: ask.Confirm.Token})
		if len(h.deck.acts) != 0 || len(h.deck.launched) != 0 {
			t.Fatalf("acted: acts=%v launched=%v", h.deck.acts, h.deck.launched)
		}
	})
	t.Run("session gone before the yes", func(t *testing.T) {
		h := phase2Harness(t)
		ask := h.say("stop voice test")
		h.setStatus(idVoiceTest, "running", true)
		got := h.yes(ask)
		if len(h.deck.acts) != 0 || !strings.Contains(got.Say, "nothing done") {
			t.Fatalf("got %q acts=%v", got.Say, h.deck.acts)
		}
	})
	t.Run("ambiguous stop asks which, then asks yes", func(t *testing.T) {
		h := phase2Harness(t)
		ask := h.say("stop tailscale")
		if ask.Confirm == nil || ask.Confirm.Kind != "choice" {
			t.Fatalf("ask %+v", ask)
		}
		pick := h.do(Request{Sentence: "first", Pending: ask.Confirm.Token, AnswerDelayMs: 500, HasAnswerDelay: true})
		if pick.Confirm == nil || pick.Confirm.Kind != "yes" || len(h.deck.acts) != 0 {
			t.Fatalf("pick %+v acts=%v", pick, h.deck.acts)
		}
	})
	t.Run("search off", func(t *testing.T) {
		h := phase2Harness(t)
		h.deck.searchErr = ErrSearchOff
		got := h.say("where did we talk about the invoice")
		if refusedOf(got) != RefusedUnsafe || !strings.Contains(got.Say, "recall") {
			t.Fatalf("got %+v", got)
		}
	})
	t.Run("a search snippet with a key is not shown", func(t *testing.T) {
		h := phase2Harness(t)
		h.deck.hits = []SearchHit{{Title: "keys", Snippet: "the invoice key is " + fakeKey()}}
		got := h.say("where did we talk about the invoice")
		for _, it := range got.Show.Items {
			if strings.Contains(it.Line, "Zx9Qw7Lm") {
				t.Fatalf("secret shown: %+v", it)
			}
		}
	})
	t.Run("next skips the session he is on", func(t *testing.T) {
		h := phase2Harness(t)
		got := h.do(Request{Sentence: "take me to the next one", On: idTsFix})
		// the waiting conductor is never picked (owner rule): phone build
		if got.Then == nil || got.Then.Target.ID != idPhone {
			t.Fatalf("got %+v", got)
		}
	})
}
