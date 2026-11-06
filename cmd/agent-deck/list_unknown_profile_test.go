package main

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// ASK-233: a shell loop of the form
//
//	cd /tmp; for p in $(agent-deck profile list | awk '{print $1}'); do agent-deck -p $p list --json; done
//
// picks up the "*" that marks the default profile, the shell expands it to
// every entry of the working directory, and each `-p <entry> list` used to
// create a brand-new profile store (state.db) for that name. On deckbox that
// left 3,074 empty profiles named after /tmp entries, which every
// all-profile pass then had to open. `list` only reads, so it must never
// create a profile.

// profileDirsNamed returns every directory under home called name.
func profileDirsNamed(t *testing.T, home, name string) []string {
	t.Helper()
	var found []string
	_ = filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == name {
			found = append(found, path)
		}
		return nil
	})
	return found
}

func TestListUnknownProfileDoesNotCreateStore(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()

	// Names copied from the deckbox incident: a root-owned log file and a
	// temp directory, both entries of /tmp.
	for _, name := range []string{"ad-ashesh.log", "vbh-A8eSh8"} {
		stdout, stderr, code := runAgentDeck(t, home, "-p", name, "list", "--json")
		if code != 0 {
			t.Fatalf("-p %s list --json: exit %d\nstdout: %s\nstderr: %s", name, code, stdout, stderr)
		}
		if strings.TrimSpace(stdout) != "[]" {
			t.Fatalf("-p %s list --json: want [] on stdout, got %q (stderr %q)", name, stdout, stderr)
		}
		if dirs := profileDirsNamed(t, home, name); len(dirs) != 0 {
			t.Fatalf("-p %s list --json created a profile store: %v", name, dirs)
		}

		stdout, stderr, code = runAgentDeck(t, home, "-p", name, "list")
		if code != 0 {
			t.Fatalf("-p %s list: exit %d\nstdout: %s\nstderr: %s", name, code, stdout, stderr)
		}
		if dirs := profileDirsNamed(t, home, name); len(dirs) != 0 {
			t.Fatalf("-p %s list created a profile store: %v", name, dirs)
		}
	}
}

// An existing profile still lists normally after the guard.
func TestListExistingProfileStillLists(t *testing.T) {
	parallelSubprocessTest(t)
	home := t.TempDir()

	if stdout, stderr, code := runAgentDeck(t, home, "profile", "create", "work"); code != 0 {
		t.Fatalf("profile create work: exit %d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}
	stdout, stderr, code := runAgentDeck(t, home, "-p", "work", "list", "--json")
	if code != 0 || strings.TrimSpace(stdout) != "[]" {
		t.Fatalf("-p work list --json: exit %d stdout %q stderr %q", code, stdout, stderr)
	}
	if dirs := profileDirsNamed(t, home, "work"); len(dirs) == 0 {
		t.Fatalf("profile create work left no profile directory")
	}
}
