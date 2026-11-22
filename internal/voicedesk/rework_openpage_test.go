package voicedesk

import "testing"

// S1: the open forms are soft and need his session's own words.
func TestReworkOpenPageSoft(t *testing.T) {
	for _, s := range []string{"open the coverage report", "open the test report", "open the build report", "open the readme page",
		"show me what the build made", "open the page from proactive audits", "open the page from proactiv audit"} {
		t.Run(s, func(t *testing.T) {
			h, pf := pageHarness(t)
			h.addSession(Session{ID: "a1000098-1700000098", Title: "tests", Group: "team/alex", Status: "idle"})
			pf.pages["a1000098-1700000098"] = Page{Title: "Test page"}
			r := h.say(s)
			if r.Handled || len(pf.opened) != 0 {
				t.Fatalf("%q taken: intent=%s refused=%s say=%q", s, r.Intent, refusedOf(r), r.Say)
			}
		})
	}
	// his session said by its own words still opens
	h, pf := pageHarness(t)
	if r := h.say("open the page from proactive audit"); r.Say != "Opening Audit report from proactive audit." || len(pf.opened) != 1 {
		t.Fatalf("named open: %+v", r)
	}
}
