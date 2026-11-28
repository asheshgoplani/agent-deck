package voicedesk

import "testing"

// Rework of review finding S3: spoken numbers in session names.

const (
	idWorker1  = "w1000001-1700000101"
	idWorker2  = "w1000002-1700000102"
	idWorker11 = "w1000011-1700000111"
	idTest1    = "w2000001-1700000201"
	idTest11   = "w2000011-1700000211"
	idTest10   = "w2000010-1700000210"
	idBuild955 = "w3000001-1700000301"
	idPlanDo   = "w4000001-1700000401"
	idPlan2    = "w4000002-1700000402"
)

func numbersHarness(t *testing.T) *harness {
	h := newHarness(t)
	for _, s := range []Session{
		{ID: idWorker1, Title: "worker-1", Group: "team/alex/workers", Profile: "work", Status: "running"},
		{ID: idWorker2, Title: "worker-2", Group: "team/alex/workers", Profile: "work", Status: "running"},
		{ID: idWorker11, Title: "worker-11", Group: "team/alex/workers", Profile: "work", Status: "running"},
		{ID: idTest1, Title: "test-1", Group: "team/alex", Profile: "work", Status: "idle"},
		{ID: idTest11, Title: "test-11", Group: "team/alex", Profile: "work", Status: "idle"},
		{ID: idTest10, Title: "test 10", Group: "team/alex", Profile: "work", Status: "idle"},
		{ID: idBuild955, Title: "build-0955", Group: "team/alex", Profile: "work", Status: "idle"},
	} {
		h.addSession(s)
	}
	return h
}

func TestReworkSpokenNumbersMatchDigits(t *testing.T) {
	for _, c := range []struct{ sentence, intent, target string }{
		{"stop worker one", IntentStop, idWorker1},
		{"status of worker one", IntentStatus, idWorker1},
		{"stop worker two", IntentStop, idWorker2},
		{"stop worker eleven", IntentStop, idWorker11},
		{"restart worker eleven", IntentRestart, idWorker11},
		{"stop test one", IntentStop, idTest1},
		{"stop test ten", IntentStop, idTest10},
		{"worker do band karo", IntentStop, idWorker2},
		{"worker ek ko band karo", IntentStop, idWorker1},
	} {
		h := numbersHarness(t)
		r := h.say(c.sentence)
		if r.Intent != c.intent || targetID(r) != c.target {
			t.Errorf("%q: intent=%s target=%s say=%q, want %s %s", c.sentence, r.Intent, targetID(r), r.Say, c.intent, c.target)
		}
	}
}

// A session that has the Hindi number word literally in its name takes it:
// "plan do" is plan-do, never plan-2, and "do" is a number for no session.
func TestReworkHindiNumberLiteralWins(t *testing.T) {
	for _, c := range []struct{ sentence, target string }{
		{"stop plan do", idPlanDo},
		{"plan do band karo", idPlanDo},
		{"stop plan two", idPlan2},
	} {
		h := numbersHarness(t)
		h.addSession(Session{ID: idPlanDo, Title: "plan-do", Group: "team/alex", Profile: "work", Status: "idle"})
		h.addSession(Session{ID: idPlan2, Title: "plan-2", Group: "team/alex", Profile: "work", Status: "idle"})
		if r := h.say(c.sentence); targetID(r) != c.target {
			t.Errorf("%q: target=%s say=%q, want %s", c.sentence, targetID(r), r.Say, c.target)
		}
	}
}

func TestReworkSpokenNumbersKeepSafety(t *testing.T) {
	h := numbersHarness(t)
	// the digits rule: a wrong or shortened number never reaches build-0955
	for _, s := range []string{"stop build 0995", "stop build 955", "stop build nine five five"} {
		if r := h.say(s); targetID(r) == idBuild955 {
			t.Errorf("%q reached build-0955: %q", s, r.Say)
		}
	}
	// "test one" is test-1, never test-11 or test 10, also when test-1 is gone
	h2 := numbersHarness(t)
	h2.setStatus(idTest1, "idle", true)
	if r := h2.say("stop test one"); targetID(r) == idTest11 || targetID(r) == idTest10 {
		t.Errorf("stop test one reached %s: %q", targetID(r), r.Say)
	}
	// a Hindi verb "do" is never a number: "worker ko band kar do" does not pick worker-2
	h3 := numbersHarness(t)
	if r := h3.say("worker ko band kar do"); targetID(r) == idWorker2 {
		t.Errorf("'band kar do' read as worker 2: %q", r.Say)
	}
	// a look-alike of a number word is no number
	h4 := numbersHarness(t)
	if r := h4.say("stop worker won"); targetID(r) == idWorker1 && r.Confirm != nil && r.Confirm.Kind == "yes" {
		t.Errorf("'won' read as one: %q", r.Say)
	}
	if len(h.deck.acts)+len(h2.deck.acts)+len(h3.deck.acts)+len(h4.deck.acts) != 0 {
		t.Fatalf("a question acted")
	}
}
