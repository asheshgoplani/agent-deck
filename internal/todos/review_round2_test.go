package todos

import (
	"errors"
	"testing"
)

// R1 (round 2 review): a query word that starts a different, slightly
// longer word ("plan" in "plane") is a close match, not a match: nothing
// is ticked off even when it is the only candidate.
func TestRound2LookAlikePrefixIsNotTickedOff(t *testing.T) {
	cases := []struct{ item, query string }{
		{"pay rental insurance", "pay rent"},
		{"book the plane", "plan"},
		{"get ready for demo", "read"},
		{"check the portal", "port"},
		{"call the former landlord", "form"},
		{"fix the heater", "heat"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			s, _ := newTestStore(t)
			mustAdd(t, s, c.item)
			it, changed, err := s.Done(c.query)
			if err == nil || changed {
				t.Fatalf("Done(%q) on %q ticked off %q (changed=%v err=%v); want no change", c.query, c.item, it.Text, changed, err)
			}
			if open, _, _ := s.Counts(); open != 1 {
				t.Fatalf("Done(%q) on %q changed the list (open=%d)", c.query, c.item, open)
			}
		})
	}
}

// R1/R2: a plural or inflection ending is the same word, both ways.
func TestRound2PluralsTickOff(t *testing.T) {
	cases := []struct{ item, query string }{
		{"call the bank", "banks"},
		{"call the banks", "bank"},
		{"buy eggs", "egg"},
		{"buy an egg", "eggs"},
		{"pay taxes", "tax"},
		{"pay the tax", "taxes"},
		{"किताब खरीदो", "किताबें"},
		{"किताबें खरीदो", "किताब"},
		{"दोस्तों को बुलाओ", "दोस्त"},
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			s, _ := newTestStore(t)
			mustAdd(t, s, c.item)
			it, changed, err := s.Done(c.query)
			if err != nil || !changed || it.Text != c.item {
				t.Fatalf("Done(%q) on %q = %q changed=%v err=%v; want done", c.query, c.item, it.Text, changed, err)
			}
		})
	}
}

// An exact word still wins over a look-alike of it on another item.
func TestRound2ExactWordBeatsLookAlike(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "book the plane")
	mustAdd(t, s, "plan the trip")
	it, changed, err := s.Done("plan")
	if err != nil || !changed || it.Text != "plan the trip" {
		t.Fatalf("Done(plan) = %q changed=%v err=%v; want plan the trip", it.Text, changed, err)
	}
}

// The close match carries the look-alike, and a done look-alike is not
// reported as already done.
func TestRound2CloseMatchCarriesCandidate(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "pay rental insurance")
	_, _, err := s.Done("pay rent")
	var cm *CloseMatchError
	if !errors.As(err, &cm) || !errors.Is(err, ErrCloseMatch) || len(cm.Candidates) != 1 || cm.Candidates[0].Text != "pay rental insurance" {
		t.Fatalf("Done(pay rent) err = %v, want a close match with pay rental insurance", err)
	}
	if _, _, err := s.Done("1"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Done("pay rent"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Done(pay rent) with only the look-alike done: err = %v, want ErrNotFound", err)
	}
}
