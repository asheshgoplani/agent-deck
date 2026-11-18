package todos

import (
	"errors"
	"testing"
)

// Regression tests from the review of the first cut.

func TestDoneAgainDoesNotTickASimilarItem(t *testing.T) {
	cases := []struct {
		name  string
		items []string
		query string
	}{
		{name: "prefix: pay rent then pay rental insurance", items: []string{"pay rent", "pay rental insurance"}, query: "pay rent"},
		{name: "typo: email marco then email marcy", items: []string{"email marco", "email marcy"}, query: "email marco"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			for _, it := range tc.items {
				mustAdd(t, s, it)
			}
			first, changed, err := s.Done(tc.query)
			if err != nil || !changed || first.Text != tc.items[0] {
				t.Fatalf("first done = %+v %v %v", first, changed, err)
			}
			again, changed, err := s.Done(tc.query)
			if err != nil || changed || again.ID != first.ID {
				t.Fatalf("second done = %+v changed=%v err=%v; want already done %s", again, changed, err, first.ID)
			}
			if open, _, _ := s.Counts(); open != 1 {
				t.Fatalf("saying it twice ticked off another item (open=%d)", open)
			}
		})
	}
}

func TestSingleWordTypoIsNotEnough(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "buy a horse")
	for _, q := range []string{"house", "buy hourse"} {
		if _, _, err := s.Done(q); !errors.Is(err, ErrNotFound) {
			t.Fatalf("done %q err = %v, want ErrNotFound (a typo is never a match)", q, err)
		}
	}
}

func TestNumberSpellingsAreOnlyIDs(t *testing.T) {
	for _, q := range []string{"#12", "12.", "(12)", "1234567890123"} {
		t.Run(q, func(t *testing.T) {
			s, _ := newTestStore(t)
			mustAdd(t, s, "call 12 people")
			mustAdd(t, s, "dial 1234567890123")
			if _, _, err := s.Done(q); !errors.Is(err, ErrNotFound) {
				t.Fatalf("Done(%q) err = %v, want ErrNotFound", q, err)
			}
			if open, _, _ := s.Counts(); open != 2 {
				t.Fatalf("Done(%q) ticked off an item by its text", q)
			}
		})
	}
	s, _ := newTestStore(t)
	mustAdd(t, s, "x")
	if it, _, err := s.Done("#1"); err != nil || it.ID != "1" {
		t.Fatalf("Done(#1) = %+v %v", it, err)
	}
}

func TestToDoIsAFillerPhrase(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "call the bank")
	if it, _, err := s.Done("the to-do about the bank"); err == nil || it.ID == "1" {
		// "about" is not in the text, so this must not match...
		t.Fatalf("matched with an extra word: %+v %v", it, err)
	}
	if it, _, err := s.Done("the bank to-do"); err != nil || it.ID != "1" {
		t.Fatalf("Done(the bank to-do) = %+v %v", it, err)
	}
}
