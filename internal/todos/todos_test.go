package todos

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, *time.Time) {
	t.Helper()
	s := NewStore(t.TempDir())
	now := time.Date(2026, 11, 17, 22, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return now })
	return s, &now
}

func mustAdd(t *testing.T, s *Store, text string) Item {
	t.Helper()
	it, err := s.Add(text, AddOptions{})
	if err != nil {
		t.Fatalf("Add(%q): %v", text, err)
	}
	return it
}

func TestAddValidation(t *testing.T) {
	cases := []struct {
		name    string
		text    string
		opts    AddOptions
		want    Item
		wantErr error
	}{
		{name: "plain", text: "call the bank", want: Item{ID: "1", Text: "call the bank", Source: "cli"}},
		{name: "whitespace folded", text: "  call\n the\tbank  ", want: Item{ID: "1", Text: "call the bank", Source: "cli"}},
		{name: "source and session", text: "x", opts: AddOptions{Source: "voice", SessionID: "abc-123_x.y"}, want: Item{ID: "1", Text: "x", Source: "voice", SessionID: "abc-123_x.y"}},
		{name: "every source", text: "x", opts: AddOptions{Source: "tui"}, want: Item{ID: "1", Text: "x", Source: "tui"}},
		{name: "empty", text: "   \n", wantErr: ErrEmptyText},
		{name: "control only", text: "\x00\x01", wantErr: ErrEmptyText},
		{name: "too long", text: strings.Repeat("é", MaxTextRunes+1), wantErr: ErrTextTooLong},
		{name: "max length ok", text: strings.Repeat("é", MaxTextRunes), want: Item{ID: "1", Text: strings.Repeat("é", MaxTextRunes), Source: "cli"}},
		{name: "bad source", text: "x", opts: AddOptions{Source: "email"}, wantErr: ErrBadSource},
		{name: "bad session", text: "x", opts: AddOptions{SessionID: "a b"}, wantErr: ErrBadSession},
		{name: "long session", text: "x", opts: AddOptions{SessionID: strings.Repeat("a", 129)}, wantErr: ErrBadSession},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, now := newTestStore(t)
			got, err := s.Add(tc.text, tc.opts)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if items, _ := s.List(true); len(items) != 0 {
					t.Fatalf("refused add stored %v", items)
				}
				return
			}
			if err != nil {
				t.Fatalf("Add: %v", err)
			}
			tc.want.CreatedAt = *now
			if got.ID != tc.want.ID || got.Text != tc.want.Text || got.Source != tc.want.Source || got.SessionID != tc.want.SessionID || !got.CreatedAt.Equal(tc.want.CreatedAt) || got.DoneAt != nil {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestIDsAreNeverReused(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "one")
	two := mustAdd(t, s, "two")
	if _, err := s.Remove(two.ID); err != nil {
		t.Fatal(err)
	}
	three := mustAdd(t, s, "three")
	if three.ID != "3" {
		t.Fatalf("id after remove = %q, want 3", three.ID)
	}
}

func TestDoneUndoRemoveFlow(t *testing.T) {
	s, now := newTestStore(t)
	bank := mustAdd(t, s, "call the bank")
	mustAdd(t, s, "buy milk")

	it, changed, err := s.Done("bank")
	if err != nil || !changed || it.ID != bank.ID || it.DoneAt == nil || !it.DoneAt.Equal(*now) {
		t.Fatalf("Done(bank) = %+v %v %v", it, changed, err)
	}
	// Done again by id is a no-op, not an error.
	if it, changed, err = s.Done(bank.ID); err != nil || changed || !it.Done() {
		t.Fatalf("second Done = %+v %v %v", it, changed, err)
	}
	// A done item is no longer found by words.
	if _, _, err = s.Done("bank"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Done(bank) on done item err = %v, want ErrNotFound", err)
	}
	open, _ := s.List(false)
	all, _ := s.List(true)
	if len(open) != 1 || open[0].Text != "buy milk" || len(all) != 2 || all[1].ID != bank.ID {
		t.Fatalf("open=%v all=%v", open, all)
	}
	if it, changed, err = s.Undo(bank.ID); err != nil || !changed || it.Done() {
		t.Fatalf("Undo = %+v %v %v", it, changed, err)
	}
	if _, changed, err = s.Undo(bank.ID); err != nil || changed {
		t.Fatalf("second Undo changed=%v err=%v", changed, err)
	}
	if _, _, err = s.Undo("99"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Undo(99) err = %v", err)
	}
	if _, _, err = s.Undo("bank"); !errors.Is(err, ErrBadID) {
		t.Fatalf("Undo(words) err = %v, want ErrBadID", err)
	}
	if rm, err := s.Remove(bank.ID); err != nil || rm.Text != "call the bank" {
		t.Fatalf("Remove = %+v %v", rm, err)
	}
	if _, err := s.Remove(bank.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove err = %v", err)
	}
	if o, d, _ := s.Counts(); o != 1 || d != 0 {
		t.Fatalf("counts %d %d", o, d)
	}
}

func TestDoneBareNumberIsOnlyAnID(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "call 12 people")
	if _, _, err := s.Done("12"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Done(12) err = %v, want ErrNotFound (no item 12)", err)
	}
	if open, _, _ := s.Counts(); open != 1 {
		t.Fatal("Done(12) ticked off an item by its text")
	}
	if it, _, err := s.Done("12 people"); err != nil || it.ID != "1" {
		t.Fatalf("Done(12 people) = %+v %v", it, err)
	}
}

func TestDoneAmbiguousNeverGuesses(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "call the bank about the card")
	mustAdd(t, s, "call the bank about the loan")
	_, _, err := s.Done("bank")
	var amb *AmbiguousError
	if !errors.As(err, &amb) || !errors.Is(err, ErrAmbiguous) || len(amb.Candidates) != 2 {
		t.Fatalf("err = %v, want ambiguous with 2 candidates", err)
	}
	if open, _, _ := s.Counts(); open != 2 {
		t.Fatalf("an ambiguous done changed the list: open=%d", open)
	}
	if it, _, err := s.Done("bank loan"); err != nil || it.ID != "2" {
		t.Fatalf("Done(bank loan) = %+v %v", it, err)
	}
}

func TestRetentionDropsOldDoneItems(t *testing.T) {
	s, now := newTestStore(t)
	mustAdd(t, s, "old")
	mustAdd(t, s, "keep open")
	if _, _, err := s.Done("1"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(DoneRetention + time.Hour)
	mustAdd(t, s, "new")
	all, _ := s.List(true)
	if len(all) != 2 || all[0].Text != "keep open" || all[1].Text != "new" {
		t.Fatalf("after retention: %v", all)
	}
}

func TestFileShapeAndMode(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "x")
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	data, _ := os.ReadFile(s.Path())
	var f map[string]json.RawMessage
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"version", "next_id", "items"} {
		if _, ok := f[k]; !ok {
			t.Fatalf("file has no %q: %s", k, data)
		}
	}
	// The item JSON always has done_at (null while open).
	b, _ := json.Marshal(Item{ID: "1", Text: "x", Source: "cli"})
	if !strings.Contains(string(b), `"done_at":null`) || strings.Contains(string(b), "session_id") {
		t.Fatalf("item json %s", b)
	}
}

func TestCorruptFileIsAnErrorNotAnEmptyList(t *testing.T) {
	s, _ := newTestStore(t)
	if err := os.WriteFile(s.Path(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List(false); err == nil {
		t.Fatal("List on a corrupt file returned no error")
	}
	if _, err := s.Add("x", AddOptions{}); err == nil {
		t.Fatal("Add on a corrupt file overwrote it")
	}
}

func TestConcurrentAddsKeepEveryItem(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	const n = 20
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// A Store per goroutine, like separate processes.
			if _, err := NewStore(dir).Add("item", AddOptions{}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	items, err := NewStore(dir).List(false)
	if err != nil || len(items) != n {
		t.Fatalf("got %d items (err %v), want %d", len(items), err, n)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Fatalf("duplicate id %s", it.ID)
		}
		seen[it.ID] = true
	}
}
