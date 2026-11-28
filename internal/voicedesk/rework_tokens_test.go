package voicedesk

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Rework of review findings N2 (too-late line of a used or foreign token on
// an action) and N8 (no token in a dry-run answer).

func TestReworkUsedOrForeignTokenOnStopSaysNothingDone(t *testing.T) {
	h := numbersHarness(t)
	q := h.say("stop worker 1")
	if got := h.yes(q); got.Say != "Stopped worker 1." {
		t.Fatalf("first yes: %q", got.Say)
	}
	if again := h.yes(q); again.Say != sayTooLateDone || refusedOf(again) != RefusedExpired {
		t.Errorf("reused stop token: %q", again.Say)
	}

	h2 := numbersHarness(t)
	q2 := h2.say("stop worker 2")
	other := h2.do(Request{Sentence: "yes", Pending: q2.Confirm.Token, Profile: "other", AnswerDelayMs: 500, HasAnswerDelay: true})
	if other.Say != sayTooLateDone || len(h2.deck.acts) != 0 {
		t.Errorf("foreign stop token: %q acts=%v", other.Say, h2.deck.acts)
	}

	// a send keeps its line
	h3 := numbersHarness(t)
	q3 := h3.say("Tell deck conductor to check the phone build")
	h3.yes(q3)
	if again := h3.yes(q3); again.Say != sayTooLate {
		t.Errorf("reused send token: %q", again.Say)
	}
}

func TestReworkFileStoreRemembersUsedAction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	now := t0
	fs := FileStore{Dir: dir, Now: func() time.Time { return now }}
	tok, _ := NewToken()
	p := Pending{Token: tok, Profile: "work", Kind: "yes", Action: actionStop, CreatedAt: now}
	p.Seal()
	if err := fs.Put(p); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Take(tok); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Take(tok); err == nil {
		t.Fatal("token used twice")
	}
	if got := fs.UsedAction(tok); got != actionStop {
		t.Fatalf("used action %q", got)
	}
	// the mark dies with the sweep, like the token
	old := now.Add(-2 * HardTTL)
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		_ = os.Chtimes(filepath.Join(dir, e.Name()), old, old)
	}
	fs.Sweep()
	if got := fs.UsedAction(tok); got != "" {
		t.Fatalf("mark not swept: %q", got)
	}
}

func TestReworkDryRunGivesNoToken(t *testing.T) {
	h := numbersHarness(t)
	for _, s := range []string{"stop worker 1", "Tell deck conductor to check the phone build", "find the phone build session"} {
		r := h.do(Request{Sentence: s, DryRun: true})
		if r.Confirm == nil {
			t.Fatalf("%q: no question in a dry run: %+v", s, r)
		}
		if r.Confirm.Token != "" {
			t.Errorf("%q: dry run handed out token %s that was never stored", s, r.Confirm.Token)
		}
	}
	if h.store.Len() != 0 {
		t.Fatalf("dry run stored %d", h.store.Len())
	}
}
