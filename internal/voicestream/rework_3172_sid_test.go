package voicestream

import "testing"

// F4: the voice state is per machine, the bus per profile. A frame on
// profile A's bus never carries profile B's session as its session_id.
func TestRework3172SessionIDOnlyForThisProfile(t *testing.T) {
	ev := StateEvent{Target: &Target{ID: "s1", Title: "beta", Host: "", Profile: "other"}}
	if got := ev.SessionIDFor("work"); got != "" {
		t.Errorf("state: session_id %q for a target of another profile", got)
	}
	if got := ev.SessionIDFor("other"); got != "s1" {
		t.Errorf("state: session_id %q, want s1", got)
	}
	ev.Target.Profile = "" // unknown profile: kept, as before
	if got := ev.SessionIDFor("work"); got != "s1" {
		t.Errorf("state: session_id %q for a target with no profile", got)
	}
	r := ResultEvent{Target: &Target{ID: "s1", Profile: "other"}}
	if got := r.SessionIDFor("work"); got != "" {
		t.Errorf("result: session_id %q for a target of another profile", got)
	}
}

// The producer publishes with its bus profile.
func TestRework3172ProducerSessionIDForItsProfile(t *testing.T) {
	p := NewProducer(t.TempDir(), "work")
	if p.Profile != "work" {
		t.Fatalf("producer profile %q", p.Profile)
	}
}
