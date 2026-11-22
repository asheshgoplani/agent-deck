package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// The voice desk lists the profile it runs in, never another local profile
// (owner rule), so "find X" and "take me to the next one" cannot reach one.
func TestVoiceDeckListsOnlyTheCurrentProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AGENTDECK_PROFILE", "")
	dir, err := session.GetProfilesDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"work", "otherprof"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	d := &cliVoiceDeck{self: "/bin/true", profile: "work"}
	var local []string
	for _, s := range d.sources() {
		if s.Host == "" {
			local = append(local, s.Profile)
		}
	}
	if len(local) != 1 || local[0] != "work" {
		t.Fatalf("voice desk lists local profiles %v, want only [work]", local)
	}
}
