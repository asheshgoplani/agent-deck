package voicedesk

import (
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/needsme"
)

// "take me to the next one" never picks a conductor (his, anyone's, a
// renamed one known by its flag) and never a session of another profile;
// it goes on to the next waiting session that voice may switch to.
func TestNextSkipsConductorsAndOtherProfiles(t *testing.T) {
	for name, cfg := range scopeConfigs() {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.desk.Config = cfg
			for i := range h.deck.sessions {
				if h.deck.sessions[i].ID == idConductor {
					h.deck.sessions[i].WaitingSeconds = 99999 // the longest waiter
				}
			}
			addOtter(h, "waiting") // other profile, waits even longer
			h.addSession(Session{ID: idNightBoss, Title: "night-boss", Group: "team/alex", Profile: "work", Status: "waiting",
				Conductor: true, WaitingSeconds: 99998, LastActivity: t0})
			for _, s := range []string{"take me to the next one", "agla wala kholo", "who is next"} {
				got := h.do(Request{Sentence: s, Profile: "work"})
				id := switchedTo(got)
				if id == idConductor || id == idNightBoss || id == idOtter || len(h.deck.shown) > 0 && h.deck.shown[len(h.deck.shown)-1] != id {
					t.Fatalf("%q picked %s: %q (shown %v)", s, id, got.Say, h.deck.shown)
				}
				if id == "" {
					t.Fatalf("%q picked nothing: %q", s, got.Say)
				}
			}
		})
	}
}

// "next waiting", "next one", "next session", "go to next waiting",
// "agla waiting": the same next path, from every caller (none of them is a
// daemon phrase).
func TestNextWaitingWords(t *testing.T) {
	for _, s := range []string{"next waiting", "Next, waiting.", "next one", "next session", "go to next waiting",
		"go to the next waiting session", "next waiting session", "agla waiting", "agla waiting session kholo"} {
		h := newHarness(t)
		got := h.do(Request{Sentence: s, From: "mac"})
		if !got.Handled || got.Intent != IntentNext || got.Then == nil || got.Then.Target.ID != "b1000002-1700000022" { // tailscale fix on boxone, the longest waiter that is no conductor
			t.Fatalf("%q: %+v", s, got)
		}
	}
}

// R11: the next line is a sentence for every reason.
func TestNextLineIsGrammatical(t *testing.T) {
	for _, c := range []struct{ sub, want string }{
		{"", "Switching to garden planner, it is waiting for you."},
		{"interactive-menu", "Switching to garden planner, it asks a question."},
		{"idle-at-empty-prompt", "Switching to garden planner, it finished and is waiting for your next message."},
		{"auth-401", "Switching to garden planner, it needs you to log in."},
		{"usage-limit", "Switching to garden planner, it hit a usage limit."},
		{"model-unavailable", "Switching to garden planner, its model is unavailable."},
		{"account-blocked", "Switching to garden planner, its account is blocked."},
		{"process-exited", "Switching to garden planner, its process exited."},
	} {
		h := newHarness(t)
		h.addSession(Session{ID: "w4000001-1700000001", Title: "garden-planner", Group: "team/alex", Profile: "work", Status: "waiting",
			Substate: c.sub, Reason: needsme.Reason(c.sub, ""), WaitingSeconds: 99999, LastActivity: t0})
		if got := h.say("take me to the next one"); got.Say != c.want {
			t.Errorf("%s: say %q, want %q", c.sub, got.Say, c.want)
		}
	}
}
