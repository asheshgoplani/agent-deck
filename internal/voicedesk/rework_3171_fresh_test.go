package voicedesk

import (
	"os"
	"path/filepath"
	"testing"
)

// A tell to the mic's session sends with no yes, but only to the session
// as it is at that moment: one that stopped, failed or shows a menu since
// the listing gets nothing, the same checks the yes path makes.
func TestRework3171DirectSendRechecksTheSession(t *testing.T) {
	for _, c := range []struct{ status, substate string }{{"stopped", ""}, {"error", ""}, {"waiting", "interactive-menu"}} {
		h := newHarness(t)
		var fresh Session
		for _, s := range h.deck.sessions {
			if s.ID == idPhone {
				fresh = s
			}
		}
		fresh.Status, fresh.Substate = c.status, c.substate
		h.deck.fresh = map[string]Session{idPhone: fresh}
		got := h.do(Request{Sentence: "tell phone build to check the logs", On: idPhone})
		if len(h.deck.sent) != 0 {
			t.Errorf("%s/%s since the listing: sent %v (%q)", c.status, c.substate, h.deck.sent, got.Say)
		}
	}
	// unchanged: still sends at once
	h := newHarness(t)
	if got := h.do(Request{Sentence: "tell phone build to check the logs", On: idPhone}); len(h.deck.sent) != 1 || got.Confirm != nil {
		t.Errorf("unchanged session: sent=%v say=%q", h.deck.sent, got.Say)
	}
}

// The pending dir is 0700 even when it already existed with wider rights.
func TestRework3171PendingDirTightened(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pending")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fs := FileStore{Dir: dir}
	tok, _ := NewToken()
	p := Pending{Token: tok, Profile: "work", Kind: "yes", Action: actionSend}
	p.Seal()
	if err := fs.Put(p); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Errorf("pending dir mode %v (%v), want 0700", info.Mode().Perm(), err)
	}
}

// The no-yes send goes to the session as re-read, not as listed: its tool
// decides the send guard (an agent gets --require-input-prompt).
func TestRework3171DirectSendUsesTheFreshSession(t *testing.T) {
	h := newHarness(t)
	var fresh Session
	for i, s := range h.deck.sessions {
		if s.ID == idPhone {
			h.deck.sessions[i].Tool = "shell"
			fresh = s
		}
	}
	fresh.Tool = "claude"
	h.deck.fresh = map[string]Session{idPhone: fresh}
	h.do(Request{Sentence: "tell phone build to check the logs", On: idPhone})
	if len(h.deck.sent) != 1 || h.deck.sent[0].tool != "claude" {
		t.Errorf("sent %+v, want one send with the re-read tool claude", h.deck.sent)
	}
}
