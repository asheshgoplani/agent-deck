package voicedesk

import (
	"strings"
	"testing"
)

// Rework of review desk2 (PR 3176) findings M1, M5, S2, N3, N4, N7.

func sayAndShow(r Result) string {
	all := r.Say
	for _, it := range r.Show.Items {
		all += "|" + it.ID + "|" + it.Title + "|" + it.Line
	}
	return all
}

// M1 (reviewer R1): every session a bulk yes acts on is named in the spoken question.
func TestReworkDesk2BulkYesNamesEverySession(t *testing.T) {
	h := phase2Harness(t)
	q := h.say("stop all workers")
	if q.Confirm == nil {
		t.Fatalf("no question: %+v", q)
	}
	h.yes(q)
	if len(h.deck.acts) == 0 {
		t.Fatalf("nothing acted")
	}
	for _, a := range h.deck.acts {
		id := strings.TrimPrefix(a, "stop ")
		for _, s := range h.deck.sessions {
			if s.ID == id && !strings.Contains(q.Say, spokenTitle(s.Title, s.Host)) {
				t.Errorf("stopped %q after a yes to a question that never named it: %q", s.Title, q.Say)
			}
		}
	}
	if len(h.deck.acts) != len(q.Show.Items) {
		t.Errorf("acted on %d, showed %d", len(h.deck.acts), len(q.Show.Items))
	}
}

// M1: a conductor is never in a bulk list unless the words name conductors.
func TestReworkDesk2BulkLeavesConductorOut(t *testing.T) {
	h := phase2Harness(t)
	for i := range h.deck.sessions {
		if h.deck.sessions[i].ID == idConductor {
			h.deck.sessions[i].Group = "team/alex/workers"
			h.deck.sessions[i].Status = "running"
		}
	}
	for round := 0; round < 3; round++ {
		q := h.say("stop all workers")
		if q.Confirm != nil && (strings.Contains(sayAndShow(q), idConductor) || strings.Contains(q.Say, "conductor")) {
			t.Fatalf("round %d: the conductor is in a bulk list: %q", round, sayAndShow(q))
		}
		if q.Confirm == nil {
			break
		}
		h.yes(q)
	}
	for _, a := range h.deck.acts {
		if strings.HasSuffix(a, idConductor) {
			t.Fatalf("stopped the conductor: %v", h.deck.acts)
		}
	}
	// named on purpose or not, a group stop never takes a conductor:
	// "stop all conductors" is refused with no question
	before := len(h.deck.acts)
	q := h.say("stop all conductors")
	if q.Confirm != nil || refusedOf(q) != RefusedUnsafe {
		t.Errorf("'stop all conductors' is not refused: %q", sayAndShow(q))
	}
	if len(h.deck.acts) != before {
		t.Errorf("acted: %v", h.deck.acts[before:])
	}
}

// M1: the session the mic is on is never in a bulk list.
func TestReworkDesk2BulkLeavesMicSessionOut(t *testing.T) {
	h := phase2Harness(t)
	for round := 0; round < 3; round++ {
		q := h.do(Request{Sentence: "stop all workers", On: idPhone})
		if q.Confirm != nil && strings.Contains(sayAndShow(q), idPhone) {
			t.Fatalf("round %d: the mic's session is in a bulk list: %q", round, sayAndShow(q))
		}
		if q.Confirm == nil {
			break
		}
		h.do(Request{Sentence: "yes", Pending: q.Confirm.Token, On: idPhone, AnswerDelayMs: 800, HasAnswerDelay: true})
	}
	for _, a := range h.deck.acts {
		if strings.HasSuffix(a, idPhone) {
			t.Fatalf("stopped the mic's session: %v", h.deck.acts)
		}
	}
}

// M5 (reviewer R4b): another person's conversation is not read out or shown.
func TestReworkDesk2SearchOtherPeopleOnly(t *testing.T) {
	h := phase2Harness(t)
	h.deck.hits = []SearchHit{
		{Title: "sam-macdesign", DeckID: "c1000002-1700000012", Snippet: "sam: the invoice for my salary is confidential"},
		{Title: "release-notes", DeckID: idRelease, Snippet: "the invoice goes out on Friday"},
	}
	r := h.say("where did we talk about the invoice")
	all := strings.ToLower(sayAndShow(r))
	if strings.Contains(all, "salary") || strings.Contains(all, "sam") {
		t.Errorf("Sam's conversation is read out / shown: %q", all)
	}
	if r.Say != "One conversation: release notes." {
		t.Errorf("say %q", r.Say)
	}
}

// M5 (reviewer R4): secrets and other people's hits together; a hit with no deck session is dropped too.
func TestReworkDesk2SearchSecretsAndOtherPeople(t *testing.T) {
	h := phase2Harness(t)
	const key = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	h.deck.hits = []SearchHit{
		{Title: "deploy " + key, DeckID: idRelease, Snippet: "the invoice token is " + key + " rotate it"},
		{Title: "sam-macdesign", DeckID: "c1000002-1700000012", Snippet: "sam: the invoice for my salary is confidential"},
		{Title: "billing-notes", DeckID: "x1", Snippet: "password: hunter2-Zx9!qLm48vT for the invoice portal"},
		{Title: "loose-chat", DeckID: "", Snippet: "the invoice of someone without a deck session"},
	}
	r := h.say("where did we talk about the invoice")
	all := sayAndShow(r)
	if strings.Contains(all, key) || strings.Contains(all, "hunter2") {
		t.Errorf("a secret from a search hit is spoken or shown: %q", all)
	}
	low := strings.ToLower(all)
	if strings.Contains(low, "salary") || strings.Contains(low, "sam") || strings.Contains(low, "loose") || strings.Contains(low, "billing") {
		t.Errorf("a conversation that is not his is read out / shown: %q", all)
	}
}

// M5: when every hit is someone else's, it is "nothing found".
func TestReworkDesk2SearchOnlyOthersIsNothingFound(t *testing.T) {
	h := phase2Harness(t)
	h.deck.hits = []SearchHit{{Title: "sam-macdesign", DeckID: "c1000002-1700000012", Snippet: "the invoice for sam"}}
	r := h.say("where did we talk about the invoice")
	if r.Say != "Nothing found about invoice." || len(r.Show.Items) != 0 {
		t.Errorf("got %q show=%+v", r.Say, r.Show.Items)
	}
}

// S2 (reviewer R8): "stop all workers" with none running is answered, not handed on.
func TestReworkDesk2StopAllWhenNoneRunning(t *testing.T) {
	h := phase2Harness(t)
	for i := range h.deck.sessions {
		h.deck.sessions[i].Status = "stopped"
	}
	r := h.say("stop all workers")
	if !r.Handled || r.Say != "No workers are running." || len(h.deck.acts) != 0 {
		t.Errorf("handled=%v say=%q acts=%v", r.Handled, r.Say, h.deck.acts)
	}
}

// S2: archive / restart all with every match archived already is answered too.
func TestReworkDesk2ArchiveRestartAllWhenNoneLeft(t *testing.T) {
	for s, want := range map[string]string{
		"archive all workers": "No workers left to archive.",
		"restart all workers": "No workers left to restart; they are archived.",
	} {
		h := phase2Harness(t)
		for i := range h.deck.sessions {
			if h.deck.sessions[i].Group == "team/alex/workers" {
				h.deck.sessions[i].Archived = true
			}
		}
		r := h.say(s)
		if !r.Handled || r.Say != want || len(h.deck.acts) != 0 {
			t.Errorf("%q: handled=%v say=%q acts=%v", s, r.Handled, r.Say, h.deck.acts)
		}
	}
}

// N3: a host after the group is parsed.
func TestReworkDesk2LaunchGroupThenHost(t *testing.T) {
	h := phase2Harness(t)
	r := h.say("start a new session called test one in workers on boxone")
	if r.Say != "Start a new session test one in team/alex/workers on boxone? Yes?" {
		t.Fatalf("say %q", r.Say)
	}
	h.yes(r)
	if len(h.deck.launched) != 1 || h.deck.launched[0].Host != "boxone" || h.deck.launched[0].Group != "team/alex/workers" {
		t.Fatalf("launched %+v", h.deck.launched)
	}
}

// N4: a name with a path or a shell sign gets a plain-words line, not "secret"; a secret is still refused.
func TestReworkDesk2LaunchNameNotPlain(t *testing.T) {
	for _, s := range []string{"start a new session called ../../etc/passwd", "start a new session called foo; rm"} {
		h := phase2Harness(t)
		r := h.say(s)
		if r.Confirm != nil || strings.Contains(r.Say, "secret") || !strings.Contains(r.Say, "plain words") {
			t.Errorf("%q: say %q confirm=%v", s, r.Say, r.Confirm != nil)
		}
	}
	h := phase2Harness(t)
	r := h.say("start a new session called ghp_16C7e42F292c6912E7710c838347Ae178B4a")
	if r.Confirm != nil || !strings.Contains(r.Say, "secret") {
		t.Errorf("secret name: say %q", r.Say)
	}
}

// N7: a worker that is another person's session is not listed.
func TestReworkDesk2WorkersOfOthersHidden(t *testing.T) {
	h := phase2Harness(t)
	h.deck.children[idConductor] = append(h.deck.children[idConductor],
		Session{ID: "c1000002-1700000012", Title: "sam-macdesign", Status: "waiting"},
		Session{ID: "z0000009-1700000099", Title: "unknown-child", Status: "running"})
	r := h.say("workers of conductor deck alex")
	all := strings.ToLower(sayAndShow(r))
	if strings.Contains(all, "sam") || strings.Contains(all, "unknown") {
		t.Errorf("another person's worker is listed: %q", all)
	}
	if !strings.Contains(r.Say, "voice test") {
		t.Errorf("his worker is missing: %q", r.Say)
	}
}
