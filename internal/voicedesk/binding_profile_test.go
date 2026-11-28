package voicedesk

import "testing"

// A pending question is bound to each target's profile too: a stored
// question whose target, choice or bulk target moved to another profile is
// no longer valid (review R14 / Codex).
func TestPendingBindingCoversTargetProfiles(t *testing.T) {
	base := Pending{Token: "00000000000000000000000000000001", Profile: "work", Kind: "yes", Action: actionStop,
		Target:  &Ref{ID: "a1", Host: "", Profile: "work"},
		Choices: []Ref{{ID: "c1", Profile: "work"}},
		Targets: []Ref{{ID: "t1", Profile: "work"}, {ID: "t2", Profile: "work"}}}
	base.Seal()
	if !base.Valid("work") {
		t.Fatal("setup: not valid")
	}
	for name, mut := range map[string]func(p *Pending){
		"target profile": func(p *Pending) { t := *p.Target; t.Profile = "other"; p.Target = &t },
		"choice profile": func(p *Pending) { p.Choices = []Ref{{ID: "c1", Profile: "other"}} },
		"bulk profile":   func(p *Pending) { p.Targets = []Ref{{ID: "t1", Profile: "other"}, {ID: "t2", Profile: "work"}} },
	} {
		p := base
		mut(&p)
		if p.Valid("work") {
			t.Errorf("%s changed, the question is still valid", name)
		}
	}
}
