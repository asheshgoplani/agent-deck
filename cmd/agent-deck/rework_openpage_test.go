package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/voicedesk"
)

// N6: a binary whose `open` has no --last (flag parse error, exit 1, as
// runOpen answers it) is "not in this build", not "could not open".
func TestReworkOpenPageWithoutOpenLast(t *testing.T) {
	self := filepath.Join(t.TempDir(), "agent-deck-old")
	script := "#!/bin/sh\necho 'flag provided but not defined: -last' >&2\necho 'Error: open: flag provided but not defined: -last' >&2\nexit 1\n"
	if err := os.WriteFile(self, []byte(script), 0o700); err != nil { //nolint:gosec // G306: test stub
		t.Fatal(err)
	}
	d := &cliVoiceDeck{self: self, profile: "_test"}
	_, err := d.OpenPage(context.Background(), voicedesk.Session{ID: "a1-1", Title: "alpha"})
	if !errors.Is(err, voicedesk.ErrPageUnavailable) {
		t.Fatalf("err = %v, want ErrPageUnavailable", err)
	}
}
