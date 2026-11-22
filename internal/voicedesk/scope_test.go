package voicedesk

// Owner rule: voice never targets a session of another local profile, and
// never a conductor (his or anyone's, renamed or not). Find, its yes, its
// choice and every switch keep to his own profile's non-conductor sessions.
// The cases are the re-review's g6 cases (r2), asserting the safe answer.

import "testing"

const (
	idOtter     = "p2000001-1700000001" // otter notes, profile otherprof
	idNightBoss = "c3000001-1700000001" // a renamed conductor (no conductor- title)
)

func scopeConfigs() map[string]Config {
	return map[string]Config{"owner_groups": fixtureConfig(), "no_owner_config": {}}
}

func addOtter(h *harness, status string) {
	h.addSession(Session{ID: idOtter, Title: "otter-notes", Group: "team/alex", Profile: "otherprof", Status: status,
		Reason: "asks a question", WaitingSeconds: 99999, LastActivity: t0})
}

func switchedTo(r Result) string {
	if r.Then == nil {
		return ""
	}
	return r.Then.Target.ID
}

// (2a) "find otter notes" + yes from profile work never switches into
// profile otherprof, and the Mac app is never asked to show it.
func TestVoiceNeverFindsAnotherProfile(t *testing.T) {
	for name, cfg := range scopeConfigs() {
		t.Run(name, func(t *testing.T) {
			for _, s := range []string{"find otter notes", "find the otter notes session", "otter notes wali session dhoondo"} {
				h := newHarness(t)
				h.desk.Config = cfg
				addOtter(h, "idle")
				ask := h.do(Request{Sentence: s, Profile: "work"})
				if ask.Confirm != nil {
					yes := h.do(Request{Sentence: "yes", Profile: "work", Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
					if switchedTo(yes) == idOtter {
						t.Fatalf("%q + yes from profile work switched to profile otherprof: say=%q", s, yes.Say)
					}
				}
				if switchedTo(ask) == idOtter || len(h.deck.shown) != 0 {
					t.Fatalf("%q: switched or shown: %+v shown=%v", s, ask, h.deck.shown)
				}
			}
		})
	}
}

// A yes to a question asked before the session moved to another profile is
// checked again: the fresh lookup says otherprof, so nothing is switched.
func TestYesRechecksTheProfile(t *testing.T) {
	h := newHarness(t)
	ask := h.say("find the phone build session")
	if ask.Confirm == nil {
		t.Fatalf("no question: %+v", ask)
	}
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idPhone {
			h.deck.sessions[i].Profile = "otherprof"
		}
	}
	yes := h.yes(ask)
	if yes.Then != nil || len(h.deck.shown) != 0 || refusedOf(yes) == "" {
		t.Fatalf("switched after the session left the profile: %+v", yes)
	}
}

// (2c) conductors are never found or switched to by name: the fixture's
// conductor-deck-alex (his group), other people's conductors (no owner
// config), a renamed conductor known only by its flag, and one known only
// by its group.
func TestVoiceNeverSwitchesToAConductor(t *testing.T) {
	conductors := map[string]bool{idConductor: true, idNightBoss: true, "c1000001-1700000011": true,
		"c1000003-1700000013": true, "g5000001-1700000001": true, "b1000001-1700000021": true}
	for name, cfg := range scopeConfigs() {
		t.Run(name, func(t *testing.T) {
			for _, s := range []string{"find the deck conductor session", "find conductor deck alex", "find night boss",
				"find the night boss session", "find ops desk", "find conductor on boxone"} {
				h := newHarness(t)
				h.desk.Config = cfg
				h.addSession(Session{ID: idNightBoss, Title: "night-boss", Group: "team/alex", Profile: "work", Status: "idle",
					Conductor: true, LastActivity: t0})
				h.addSession(Session{ID: "g5000001-1700000001", Title: "ops-desk", Group: "conductor", Profile: "work", Status: "idle",
					LastActivity: t0})
				ask := h.say(s)
				for _, it := range ask.Show.Items {
					if conductors[it.ID] {
						t.Errorf("%q offers conductor %s", s, it.Title)
					}
				}
				if ask.Confirm == nil {
					continue
				}
				var got Result
				if ask.Confirm.Kind == "choice" {
					got = h.do(Request{Sentence: "first", Pending: ask.Confirm.Token, AnswerDelayMs: 800, HasAnswerDelay: true})
				} else {
					got = h.yes(ask)
				}
				if conductors[switchedTo(got)] {
					t.Errorf("%q -> then.switch to conductor %s", s, got.Then.Target.Title)
				}
			}
		})
	}
}

// The phone and glasses say "switch to X", "go to X", "X pe jao": the same
// safe path as find (a yes before the switch). The Mac voice daemon handles
// those words itself, so from the Mac they stay not the desk's.
func TestSwitchWordsTakeTheFindPath(t *testing.T) {
	for _, from := range []string{"phone", "glasses", "text"} {
		for _, s := range []string{"switch to phone build", "go to phone build", "Switch to the phone build session",
			"phone build pe jao", "phone build par chalo"} {
			h := newHarness(t)
			ask := h.do(Request{Sentence: s, From: from})
			if !ask.Handled || ask.Intent != IntentFind || ask.Confirm == nil || ask.Confirm.Kind != "yes" ||
				ask.Say != "Found phone build. Switch to it?" || ask.Then != nil {
				t.Fatalf("%s %q: %+v", from, s, ask)
			}
			yes := h.do(Request{Sentence: "yes", From: from, Pending: ask.Confirm.Token, AnswerDelayMs: 700, HasAnswerDelay: true})
			if switchedTo(yes) != idPhone || yes.Say != "Switching to phone build." {
				t.Fatalf("%s %q + yes: %+v", from, s, yes)
			}
		}
		// a conductor or another profile by these words: never switched
		for _, s := range []string{"switch to conductor deck alex", "go to otter notes"} {
			h := newHarness(t)
			addOtter(h, "idle")
			ask := h.do(Request{Sentence: s, From: from})
			if ask.Confirm != nil || ask.Then != nil {
				t.Fatalf("%s %q: %+v", from, s, ask)
			}
		}
		// talk that starts the same way is not a session
		for _, s := range []string{"go to sleep", "switch to the main branch", "go to the file and fix it"} {
			h := newHarness(t)
			if got := h.do(Request{Sentence: s, From: from}); got.Handled {
				t.Fatalf("%s %q stolen: %+v", from, s, got)
			}
		}
	}
	h := newHarness(t)
	h.deck.noList = true
	for _, s := range []string{"switch to phone build", "go to phone build", "phone build pe jao"} {
		if got := h.do(Request{Sentence: s, From: "mac"}); got.Handled {
			t.Fatalf("mac %q is the daemon's: %+v", s, got)
		}
	}
}
