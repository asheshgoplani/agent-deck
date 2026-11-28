package needsme

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestDefaultSourcesUnreadableProfileIsAnError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("permissions are not enforced for root")
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	dir, err := session.GetProfilesDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"good", "locked"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(filepath.Join(dir, "locked"), 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(filepath.Join(dir, "locked"), 0o700) })

	srcs := DefaultSources("/bin/true", "default")
	res := Collect(context.Background(), Options{Sources: srcs, Timeout: time.Second, Now: fixed})
	var sawGood bool
	for _, s := range srcs {
		if s.Profile == "good" {
			sawGood = true
		}
	}
	if !sawGood {
		t.Errorf("readable profile was not asked: %+v", srcs)
	}
	if len(res.Errors) == 0 {
		t.Fatalf("unreadable profile dir must be an errors entry, got none")
	}
	for _, e := range res.Errors {
		if e.Host != "" || e.Error == "" {
			t.Errorf("bad entry %+v", e)
		}
	}
}

// The voice desk lists the current profile only: a session in another local
// profile must never reach it (owner rule: voice never targets other
// profiles). `needs-me` itself keeps every local profile (docs/voice.md).
func TestProfileSourcesListOnlyTheCurrentProfile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("AGENTDECK_PROFILE", "")
	dir, err := session.GetProfilesDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"mine", "otherprof"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, p, "state.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var local []string
	for _, s := range ProfileSources("/bin/true", "mine") {
		if s.Host == "" {
			local = append(local, s.Profile)
		}
	}
	if len(local) != 1 || local[0] != "mine" {
		t.Fatalf("voice sources ask local profiles %v, want only [mine]", local)
	}
	// needs-me (the CLI) still asks every local profile
	var all []string
	for _, s := range DefaultSources("/bin/true", "mine") {
		if s.Host == "" {
			all = append(all, s.Profile)
		}
	}
	if len(all) != 2 {
		t.Fatalf("needs-me asks %v, want both profiles", all)
	}
}

// A conductor is known by what it is: `list --json` names it in
// conductor_name (set from the conductor flag, so a renamed conductor keeps
// it). The rows, local and remote, and the needs-me sessions carry it, so
// the voice desk can keep every conductor out of find, switch and next.
func TestConductorNameReachesRowsAndSessions(t *testing.T) {
	var row Row
	if err := json.Unmarshal([]byte(`{"id":"c1","title":"night-boss","status":"waiting","conductor_name":"ops"}`), &row); err != nil {
		t.Fatal(err)
	}
	if row.ConductorName != "ops" {
		t.Fatalf("local row lost conductor_name: %+v", row)
	}
	var info session.RemoteSessionInfo
	if err := json.Unmarshal([]byte(`{"id":"r1","title":"night-boss","status":"waiting","conductor_name":"ops"}`), &info); err != nil {
		t.Fatal(err)
	}
	if rr := remoteRows([]session.RemoteSessionInfo{info}, "default"); rr[0].ConductorName != "ops" {
		t.Fatalf("remote row lost conductor_name: %+v", rr[0])
	}
	res := Collect(context.Background(), Options{Sources: []Source{{Fetch: func(context.Context) ([]Row, error) {
		return []Row{row}, nil
	}}}, Timeout: time.Second, Now: fixed})
	if len(res.Sessions) != 1 || res.Sessions[0].ConductorName != "ops" {
		t.Fatalf("needs-me session lost conductor_name: %+v", res.Sessions)
	}
}
