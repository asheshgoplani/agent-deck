package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The shape `recall search --json [--all-remotes]` really prints: local hits
// under result.hits, each remote's hits under remotes[].hits.
const reworkRecallJSON = `{"index":{"swept":true,"parsed":0,"elapsed_ms":1},` +
	`"result":{"query":"invoice","match":"\"invoice\"","hits":[` +
	`{"sess_id":4,"harness":"claude","profile":".claude","native_id":"046980ac","deck_id":"deck-alpha-1","title":"invoice export for alex","cwd":"/w/alpha","body_hits":2,"card_hit":true,"snippet":"please fix the invoice export"},` +
	`{"sess_id":5,"harness":"claude","native_id":"77","title":"loose notes","snippet":"an invoice somewhere"}` +
	`],"candidates":2,"elapsed_ms":2},` +
	`"remotes":[{"remote":"boxb","hits":[{"sess_id":1,"deck_id":"deck-remote-9","title":"remote notes","snippet":"the invoice dispute","remote":"boxb"}]},` +
	`{"remote":"boxc","error":"unreachable"}],"success":false}`

func reworkFakeSelf(t *testing.T, out string) string {
	t.Helper()
	self := filepath.Join(t.TempDir(), "fake-agent-deck")
	if err := os.WriteFile(self, []byte("#!/bin/sh\ncat <<'EOT'\n"+out+"\nEOT\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return self
}

func TestReworkVoiceSearchReadsRealRecallJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	d := &cliVoiceDeck{self: reworkFakeSelf(t, reworkRecallJSON), profile: "_test"}
	hits, err := d.Search(context.Background(), "invoice")
	if err != nil {
		t.Fatalf("search: %v (one remote down must keep the other hits)", err)
	}
	if len(hits) != 3 {
		t.Fatalf("got %d hits %+v, want 3 (two local from result.hits, one from remotes[].hits)", len(hits), hits)
	}
	if hits[0].DeckID != "deck-alpha-1" || hits[0].Host != "" || hits[0].Title != "invoice export for alex" {
		t.Errorf("local hit = %+v, want deck id deck-alpha-1 on this machine", hits[0])
	}
	if hits[1].DeckID != "" {
		t.Errorf("hit with no deck session = %+v, want empty deck id", hits[1])
	}
	if hits[2].DeckID != "deck-remote-9" || hits[2].Host != "boxb" {
		t.Errorf("remote hit = %+v, want deck id deck-remote-9 on boxb", hits[2])
	}
}

func TestReworkVoiceSearchRecallOffAndFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	d := &cliVoiceDeck{self: reworkFakeSelf(t, `{"success":false,"error":"recall is off"}`), profile: "_test"}
	if _, err := d.Search(context.Background(), "x"); err == nil || err.Error() != "search is off" {
		t.Errorf("recall off: err = %v, want search is off", err)
	}
	d = &cliVoiceDeck{self: reworkFakeSelf(t, `{"success":false,"error":"boom"}`), profile: "_test"}
	if _, err := d.Search(context.Background(), "x"); err == nil {
		t.Errorf("failed search with no result: want an error")
	}
}
