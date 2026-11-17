package voicedesk

import (
	"encoding/json"
	"strings"
	"testing"
)

// The voice daemon marks the session that has the mic: "🎙 garden planner".
// The marker is never part of a name: not spoken, not shown, not in target.
func TestMicMarkerIsStripped(t *testing.T) {
	for _, marker := range []string{"🎙 ", "🎙️ ", "🎙"} {
		t.Run(marker, func(t *testing.T) {
			h := newHarness(t)
			for i := range h.deck.sessions {
				if h.deck.sessions[i].ID == idPhone {
					h.deck.sessions[i].Title = marker + "phone-build"
				}
			}
			for _, s := range []string{"status of phone build", "who is waiting for me", "list my sessions", "tell phone build to run the tests"} {
				got := h.say(s)
				b, _ := json.Marshal(got)
				if strings.Contains(string(b), "🎙") {
					t.Fatalf("%q: marker leaked: %s", s, b)
				}
			}
			got := h.say("tell phone build to run the tests")
			if targetID(got) != idPhone || got.Target.Title != "phone-build" || got.Say != "Send to phone build: run the tests. Yes?" {
				t.Fatalf("got %+v", got)
			}
		})
	}
}

// No --on (the daemon's own id can be null): every tell asks first.
func TestNoOnMeansEveryTellAsks(t *testing.T) {
	h := newHarness(t)
	for _, s := range []string{"tell deck conductor to check the phone build", "voice test ko bolo ki tests chalao"} {
		got := h.do(Request{Sentence: s, On: ""})
		if got.Confirm == nil || got.Confirm.Kind != "yes" || len(h.deck.sent) != 0 {
			t.Fatalf("%q: sent without a yes: %+v", s, got)
		}
	}
}
