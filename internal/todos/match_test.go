package todos

import (
	"errors"
	"testing"
	"time"
)

func TestMatch(t *testing.T) {
	done := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	items := []Item{
		{ID: "1", Text: "Call the bank"},
		{ID: "2", Text: "Book the dentist appointment"},
		{ID: "3", Text: "buy milk"},
		{ID: "4", Text: "buy milk and bread"},
		{ID: "5", Text: "pay rent", DoneAt: &done},
		{ID: "6", Text: "email sam about the release"},
		{ID: "7", Text: "email sam about the invoice"},
	}
	cases := []struct {
		query   string
		wantID  string
		wantErr error
		cands   []string
	}{
		{query: "bank", wantID: "1"},
		{query: "BANK!", wantID: "1"},
		{query: "the bank", wantID: "1"},
		{query: "call bank", wantID: "1"},
		{query: "ban", wantErr: ErrNotFound},     // a 3-letter prefix is a look-alike
		{query: "banks", wantID: "1"},            // a plural is the same word
		{query: "dentsit", wantErr: ErrNotFound}, // a typo is not a match
		{query: "book dentsit", wantErr: ErrNotFound},
		{query: "dentist appointmnt", wantErr: ErrNotFound},
		{query: "dentist appointment", wantID: "2"},
		{query: "buy milk", wantID: "3"}, // exact text wins over "buy milk and bread"
		{query: "milk bread", wantID: "4"},
		{query: "milk", wantErr: ErrAmbiguous, cands: []string{"3", "4"}},
		{query: "email sam", wantErr: ErrAmbiguous, cands: []string{"6", "7"}},
		{query: "sam invoice", wantID: "7"},
		{query: "rent", wantID: "5"},        // nothing open matches: the done item (already done)
		{query: "ba", wantErr: ErrNotFound}, // too short for a prefix
		{query: "bank loan", wantErr: ErrNotFound},
		{query: "cat", wantErr: ErrNotFound},
		{query: "  ", wantErr: ErrNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			got, err := Match(items, tc.query)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v (got %+v)", err, tc.wantErr, got)
				}
				var amb *AmbiguousError
				if errors.As(err, &amb) {
					if len(amb.Candidates) != len(tc.cands) {
						t.Fatalf("candidates %v, want %v", amb.Candidates, tc.cands)
					}
					for i, c := range amb.Candidates {
						if c.ID != tc.cands[i] {
							t.Fatalf("candidates %v, want %v", amb.Candidates, tc.cands)
						}
					}
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Fatalf("Match(%q) = %+v, %v; want id %s", tc.query, got, err, tc.wantID)
			}
		})
	}
}

func TestIsID(t *testing.T) {
	for q, want := range map[string]bool{"1": true, " 12 ": true, "007": true, "": false, "1a": false, "bank": false, "1234567890": false} {
		if got := IsID(q); got != want {
			t.Errorf("IsID(%q) = %v, want %v", q, got, want)
		}
	}
}
