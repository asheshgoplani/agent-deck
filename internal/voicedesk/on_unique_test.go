package voicedesk

import "testing"

// --on is a session id only. When two listed sessions share that id (a
// local one and one on a remote), the desk cannot tell which one the mic
// is on, so a tell to either one asks first instead of sending at once.
func TestTellOnIDOnTwoHostsAsks(t *testing.T) {
	h := newHarness(t)
	for i := range h.deck.sessions {
		if h.deck.sessions[i].Title == "tailscale-fix" { // boxone
			h.deck.sessions[i].ID = idPhone
		}
	}
	got := h.do(Request{Sentence: "tell tailscale fix to check the logs", On: idPhone})
	if got.Confirm == nil {
		t.Fatalf("sent at once to a session whose id the mic shares with another host: say=%q sent=%v", got.Say, h.deck.sent)
	}
	if len(h.deck.sent) != 0 {
		t.Errorf("sent: %v", h.deck.sent)
	}
	// the plain case still sends at once
	h = newHarness(t)
	got = h.do(Request{Sentence: "tell phone build to check the logs", On: idPhone})
	if got.Confirm != nil || len(h.deck.sent) != 1 {
		t.Errorf("tell on the mic's own session: confirm=%v sent=%v say=%q", got.Confirm != nil, h.deck.sent, got.Say)
	}
}
