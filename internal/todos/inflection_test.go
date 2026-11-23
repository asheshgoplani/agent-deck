package todos

import (
	"errors"
	"testing"
	"time"
)

// F1: "es" is an ending only after a stem ending in s, x, z, ch or sh. After
// any other stem it made a different word the same word (times/tim,
// jones/jon, notes/not, cares/car, planes/plan, james/jam).
func TestInflectionEs_OnlySibilantStems(t *testing.T) {
	same := [][2]string{
		{"bank", "banks"}, {"egg", "eggs"}, {"box", "boxes"}, {"tax", "taxes"},
		{"bus", "buses"}, {"church", "churches"}, {"dish", "dishes"},
		{"किताब", "किताबें"}, {"दोस्त", "दोस्तों"},
	}
	for _, p := range same {
		for _, pair := range [][2]string{{p[0], p[1]}, {p[1], p[0]}} {
			if got := wordMatches(pair[0], pair[1]); got != wordSame {
				t.Errorf("wordMatches(%q, %q) = %v, want same", pair[0], pair[1], got)
			}
		}
	}
	for _, p := range [][2]string{
		{"tim", "times"}, {"jon", "jones"}, {"jam", "james"}, {"not", "notes"},
		{"car", "cares"}, {"plan", "planes"},
	} {
		for _, pair := range [][2]string{{p[0], p[1]}, {p[1], p[0]}} {
			if got := wordMatches(pair[0], pair[1]); got == wordSame {
				t.Errorf("wordMatches(%q, %q) = same, want not the same word", pair[0], pair[1])
			}
		}
	}
}

func TestMatch_F1Table(t *testing.T) {
	done := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	it := func(id, text string, isDone bool) Item {
		x := Item{ID: id, Text: text}
		if isDone {
			x.DoneAt = &done
		}
		return x
	}
	cases := []struct {
		name    string
		items   []Item
		query   string
		wantID  string
		wantErr error
	}{
		{"tim again is already done, not train times", []Item{it("1", "call tim", true), it("2", "check train times", false)}, "tim", "1", nil},
		{"jon is call jon, not email jones", []Item{it("1", "call jon", false), it("2", "email jones", false)}, "jon", "1", nil},
		{"jones is email jones", []Item{it("1", "call jon", false), it("2", "email jones", false)}, "jones", "2", nil},
		{"notes does not tick not", []Item{it("1", "do not forget the passport", false)}, "notes", "", ErrNotFound},
		{"car does not tick cares", []Item{it("1", "who cares about lint", false)}, "car", "", ErrNotFound},
		{"james does not tick jam", []Item{it("1", "buy jam", false)}, "james", "", ErrNotFound},
		{"jon does not tick jones", []Item{it("1", "email jones", false)}, "jon", "", ErrNotFound},
		{"plan does not tick planes (asks, never ticks)", []Item{it("1", "book the planes", false)}, "plan", "", ErrCloseMatch},
		{"james and jam each find their own", []Item{it("1", "call james", false), it("2", "buy jam", false)}, "james", "1", nil},
		{"jam finds buy jam", []Item{it("1", "call james", false), it("2", "buy jam", false)}, "jam", "2", nil},
		{"bank / banks", []Item{it("1", "visit the banks", false)}, "bank", "1", nil},
		{"banks / bank", []Item{it("1", "call the bank", false)}, "banks", "1", nil},
		{"box / boxes", []Item{it("1", "pack the boxes", false)}, "box", "1", nil},
		{"boxes / box", []Item{it("1", "pack the box", false)}, "boxes", "1", nil},
		{"tax / taxes", []Item{it("1", "file the taxes", false)}, "tax", "1", nil},
		{"taxes / tax", []Item{it("1", "file the tax return", false)}, "taxes", "1", nil},
		{"bus / buses", []Item{it("1", "book the buses", false)}, "bus", "1", nil},
		{"church / churches", []Item{it("1", "visit churches", false)}, "church", "1", nil},
		{"dish / dishes", []Item{it("1", "wash the dishes", false)}, "dish", "1", nil},
		{"hindi book plural", []Item{it("1", "किताबें खरीदो", false)}, "किताब खरीदो", "1", nil},
		{"hindi friends plural", []Item{it("1", "दोस्त को फोन करो", false)}, "दोस्तों को फोन करो", "1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Match(tc.items, tc.query)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Match(%q) = %+v, %v; want %v", tc.query, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Fatalf("Match(%q) = %+v, %v; want id %s", tc.query, got, err, tc.wantID)
			}
		})
	}
}
