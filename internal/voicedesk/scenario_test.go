package voicedesk

import "testing"

// Brainstorm scenario (owner, 23:30): switch to a session, ask it
// something, open the page it made, follow up. Each step's result is what
// the caller acts on: then.switch moves the mic, so the next steps run with
// --on that session.
func TestScenarioSwitchAskOpenFollowUp(t *testing.T) {
	h, pf := pageHarness(t)
	// 1. voice never switches to a conductor (owner rule): "find the deck
	// conductor session" asks nothing and switches nothing; he moves the
	// mic to it himself, so the next steps run with --on that session
	if ask := h.say("find the deck conductor session"); ask.Confirm != nil || ask.Then != nil {
		t.Fatalf("voice offered to switch to a conductor: %+v", ask)
	}
	on := idConductor
	// 2. ask: he is on it now, a plain question is sent at once and said back
	got := h.do(Request{Sentence: "ask deck conductor what changed since yesterday", On: on})
	if got.Say != "Sent to conductor deck alex." || len(h.deck.sent) != 1 || h.deck.sent[0].msg != "what changed since yesterday" {
		t.Fatalf("ask: %+v sent=%+v", got, h.deck.sent)
	}
	// 3. open the page it made, without naming it
	got = h.do(Request{Sentence: "jo page banaya woh kholo", On: on})
	if got.Say != "Opening Tonight's plan from conductor deck alex." || len(pf.opened) != 1 {
		t.Fatalf("open: %+v", got)
	}
	// 4. follow up: read its reply, then a risky follow-up still asks first
	got = h.do(Request{Sentence: "read deck conductor", On: on})
	if got.Intent != IntentRead || got.Say == "" {
		t.Fatalf("read: %+v", got)
	}
	got = h.do(Request{Sentence: "tell deck conductor to delete the stale sessions", On: on})
	if got.Confirm == nil || len(h.deck.sent) != 1 {
		t.Fatalf("risky follow-up sent without a yes: %+v", got)
	}
	// and "archive" of a session is never done by the plain sentence
	if got = h.say("archive deck conductor"); got.Confirm == nil || len(h.deck.acts) != 0 {
		t.Fatalf("archive without a yes: %+v", got)
	}
}
