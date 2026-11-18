package todos

// Rework round 1 of PR 3173: the independent reviewer's adversarial cases,
// kept as regression tests (a FAIL was a finding, a PASS a property that held).

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
)

// R1: Devanagari words must not be cut at vowel signs: "बैंक" (bank) is not
// "किताब" (book).
func TestReviewDevanagariLookAlikeIsNotTickedOff(t *testing.T) {
	cases := []struct{ item, query string }{
		{"किताब खरीदो", "बैंक"}, // buy a book / bank
		{"नाच सीखना", "चीनी"},   // learn to dance / sugar
		{"कमरा साफ करो", "काम"}, // clean the room / work
	}
	for _, c := range cases {
		s, _ := newTestStore(t)
		mustAdd(t, s, c.item)
		it, changed, err := s.Done(c.query)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Done(%q) on a list holding only %q: ticked off %q (changed=%v, err=%v), want ErrNotFound", c.query, c.item, it.Text, changed, err)
		}
	}
}

// R2: Devanagari words split as whole words.
func TestReviewDevanagariWordsAreWholeWords(t *testing.T) {
	got := words("बैंक को कॉल करो")
	if len(got) != 4 {
		t.Errorf("words() = %q (%d parts), want 4 whole words", got, len(got))
	}
}

// R3: a query of only filler words must never pick an item.
func TestReviewFillerOnlyQueryNeverTicksOff(t *testing.T) {
	for _, q := range []string{"a", "the", "my"} {
		s, _ := newTestStore(t)
		mustAdd(t, s, "buy a horse for the farm of my uncle")
		if it, changed, err := s.Done(q); !errors.Is(err, ErrNotFound) {
			t.Errorf("Done(%q) ticked off %q (changed=%v, err=%v), want ErrNotFound", q, it.Text, changed, err)
		}
	}
}

// R4: text that can steer a screen: bidi overrides and zero-width
// characters are format characters (Cf), not control characters (Cc).
func TestReviewCleanTextDropsBidiAndZeroWidth(t *testing.T) {
	in := "pay " + string(rune(0x202e)) + "gnp.exe" + string(rune(0x202c)) + " now" + string(rune(0x200b)) + string(rune(0x2066)) + "x" + string(rune(0x2069)) + string(rune(0xfeff))
	got := CleanText(in)
	for _, r := range []rune{0x202e, 0x202c, 0x200b, 0x2066, 0x2069, 0xfeff} {
		if strings.ContainsRune(got, r) {
			t.Errorf("CleanText kept U+%04X in %q", r, got)
		}
	}
}

// R5: a file written by a newer version must not be silently rewritten as
// version 1 with its unknown fields dropped.
func TestReviewNewerFileVersionIsNotDowngraded(t *testing.T) {
	s, _ := newTestStore(t)
	raw := `{"version":2,"next_id":5,"items":[],"lists":{"work":[1,2]}}`
	if err := os.WriteFile(s.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := s.Add("x", AddOptions{})
	after, _ := os.ReadFile(s.Path())
	if err == nil && !strings.Contains(string(after), `"lists"`) {
		t.Errorf("Add on a version 2 file succeeded and dropped its data; file is now:\n%s", after)
	}
}

// R6: a text stored with an escape sequence (hand edit, older writer) must
// not reach the caller raw.
func TestReviewStoredControlCharactersAreNotHandedOut(t *testing.T) {
	s, _ := newTestStore(t)
	raw := `{"version":1,"next_id":2,"items":[{"id":"1","text":"evil\u001b[2J\nFAKE  9  [done] all","created_at":"2026-11-17T00:00:00Z","done_at":null,"source":"cli"}]}`
	if err := os.WriteFile(s.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := s.List(false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(items[0].Text, "\x1b\n") {
		t.Errorf("List handed out raw control characters: %q", items[0].Text)
	}
}

// R7: saying the same partial words twice answers "already done", not "no
// such to-do".
func TestReviewRepeatDoneByPartialWords(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "call the bank")
	if _, changed, err := s.Done("bank"); err != nil || !changed {
		t.Fatalf("first Done: changed=%v err=%v", changed, err)
	}
	it, changed, err := s.Done("bank")
	if err != nil || changed || it.ID != "1" {
		t.Errorf("second Done(\"bank\") = item %q changed=%v err=%v, want item 1, changed=false, no error", it.ID, changed, err)
	}
}

// R8 (held): 3 stores on one directory, 20 adds each in parallel, then
// parallel done/remove/add: nothing lost, ids unique, never reused.
func TestReviewParallelWritersLoseNothing(t *testing.T) {
	dir := t.TempDir()
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for p := 0; p < 3; p++ {
		st := NewStore(dir)
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := st.Add("item", AddOptions{}); err != nil {
					errs <- err
				}
			}()
		}
	}
	wg.Wait()
	st := NewStore(dir)
	for i := 1; i <= 20; i++ {
		id := string(rune('0' + i%10))
		wg.Add(3)
		go func() { defer wg.Done(); _, _, _ = st.Done(id) }()
		go func() { defer wg.Done(); _, _ = NewStore(dir).Remove("5" + id) }()
		go func() {
			defer wg.Done()
			if _, err := NewStore(dir).Add("more", AddOptions{}); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("write failed: %v", err)
	}
	items, err := st.List(true)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.ID] {
			t.Errorf("id %s twice", it.ID)
		}
		seen[it.ID] = true
	}
	// 60 + 20 added; removed: 51..59 and 50 (ids "5"+"0".."9"), each once.
	if len(items) != 70 {
		t.Errorf("items = %d, want 70", len(items))
	}
	next, err := st.Add("last", AddOptions{})
	if err != nil || next.ID != "81" {
		t.Errorf("next id = %q (err %v), want 81", next.ID, err)
	}
}

// R9 (held): a number is only an id, in every spelling; words next to a
// number are words.
func TestReviewNumbersAreIDs(t *testing.T) {
	s, _ := newTestStore(t)
	mustAdd(t, s, "call 12 people")
	for _, q := range []string{"12", "#12", "12.", " 12 ", "0012", "no. 12"} {
		_, changed, err := s.Done(q)
		if q == "no. 12" {
			// "no" is a word, so this is a words query and may match.
			continue
		}
		if !errors.Is(err, ErrNotFound) || changed {
			t.Errorf("Done(%q) = changed %v, err %v; want ErrNotFound", q, changed, err)
		}
	}
	if it, changed, err := s.Done("12 people"); err != nil || !changed || it.ID != "1" {
		t.Errorf("Done(\"12 people\") = %q %v %v", it.ID, changed, err)
	}
}

// R10 (held): a removed id is never handed out again, also after the last
// item is removed and the store is reopened.
func TestReviewIDsSurviveRemoveAndReopen(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	mustAdd(t, s, "one")
	mustAdd(t, s, "two")
	if _, err := s.Remove("2"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Remove("1"); err != nil {
		t.Fatal(err)
	}
	if it := mustAdd(t, NewStore(dir), "three"); it.ID != "3" {
		t.Errorf("id after removing every item and reopening = %q, want 3", it.ID)
	}
}

// S1: a short prefix, a long-tail prefix or a one-letter name difference is
// a look-alike, never a match, even as the only candidate.
func TestRound1LookAlikesAreNotTickedOff(t *testing.T) {
	cases := []struct{ item, query string }{
		{"send birthday card", "car"},
		{"file the bankruptcy papers for alex", "bank"},
		{"call mario about the car", "call maria"},
		{"book the dentist", "book dentsit"},
	}
	for _, c := range cases {
		s, _ := newTestStore(t)
		mustAdd(t, s, c.item)
		if it, changed, err := s.Done(c.query); !errors.Is(err, ErrNotFound) {
			t.Errorf("Done(%q) on %q ticked off %q (changed=%v err=%v), want ErrNotFound", c.query, c.item, it.Text, changed, err)
		}
	}
	// Still matched: whole words, and a plural or short suffix.
	for _, c := range []struct{ item, query string }{
		{"call the bank", "bank"},
		{"call the banks", "bank"},
		{"book the dentist", "dentist"},
		{"किताब खरीदो", "किताब"},
	} {
		s, _ := newTestStore(t)
		mustAdd(t, s, c.item)
		if _, changed, err := s.Done(c.query); err != nil || !changed {
			t.Errorf("Done(%q) on %q: changed=%v err=%v, want done", c.query, c.item, changed, err)
		}
	}
}

// S3: the bus frame never carries the to-do's text.
func TestRound1ChangeEventHoldsNoText(t *testing.T) {
	c := NewChange(ActionAdd, Item{ID: "4", Text: "secret sk-live-1234567890abcdef", Source: "voice", SessionID: "s-1"}, 2, "voice")
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") || strings.Contains(string(b), "sk-live") || strings.Contains(string(b), `"text"`) {
		t.Fatalf("event data carries text: %s", b)
	}
	for _, k := range []string{`"action":"add"`, `"id":"4"`, `"open":2`, `"by":"voice"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("event data lacks %s: %s", k, b)
		}
	}
}

// M3: the write is the durable kind, and an unknown newer version is refused.
func TestRound1NewerVersionRefusedNotRewritten(t *testing.T) {
	s, _ := newTestStore(t)
	raw := `{"version":2,"next_id":5,"items":[]}`
	if err := os.WriteFile(s.Path(), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Add("x", AddOptions{}); !errors.Is(err, ErrNewerVersion) {
		t.Fatalf("Add on a version 2 file err = %v, want ErrNewerVersion", err)
	}
	after, _ := os.ReadFile(s.Path())
	if string(after) != raw {
		t.Fatalf("version 2 file was rewritten:\n%s", after)
	}
}
