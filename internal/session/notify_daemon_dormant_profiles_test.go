package session

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ASK-229: a host whose profiles directory holds thousands of empty profiles
// (left behind by test runs under the real HOME) made notify-daemon load every
// one of them on every 1-3 s pass and keep each SQLite store open for its whole
// life: ~29 % of a core and ~1 GB RSS for one real profile. An empty profile
// whose store has not changed since the daemon last looked at it must cost a
// stat, not a store open and a registry load, and must not hold its store open.

// newDormantProfilesFixture builds `empty` empty profiles plus one populated
// profile ("work") whose statuses a live TUI owns, so no pass probes tmux.
func newDormantProfilesFixture(t testing.TB, empty int) *TransitionDaemon {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("AGENT_DECK_PROFILE", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatal(err)
	}
	for p := 0; p < empty; p++ {
		st, err := NewStorageWithProfile(fmt.Sprintf("empty-%04d", p))
		if err != nil {
			t.Fatal(err)
		}
		st.Close()
	}
	seedDormantFixtureProfile(t, "work", 3)

	d := NewTransitionDaemon()
	d.turnLiveCheck = func(*Instance) bool { return true }
	t.Cleanup(func() {
		d.Flush()
		for _, s := range d.storages {
			s.Close()
		}
	})
	return d
}

// seedDormantFixtureProfile writes n waiting shell sessions into profile and
// registers a live TUI that owns their statuses.
func seedDormantFixtureProfile(t testing.TB, profile string, n int) []string {
	st, err := NewStorageWithProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	home, _ := os.UserHomeDir()
	now := time.Now()
	var all []*Instance
	var ids []string
	for i := 0; i < n; i++ {
		inst := &Instance{
			ID:          fmt.Sprintf("%s-s%02d", profile, i),
			Title:       fmt.Sprintf("%s-%02d", profile, i),
			ProjectPath: filepath.Join(home, "work", profile, fmt.Sprintf("p%02d", i)),
			GroupPath:   DefaultGroupPath,
			Tool:        "shell",
			Status:      StatusWaiting,
			CreatedAt:   now,
		}
		all = append(all, inst)
		ids = append(ids, inst.ID)
	}
	if err := st.SaveWithGroups(all, nil); err != nil {
		t.Fatal(err)
	}
	db := st.GetDB()
	if err := db.RegisterInstance(false); err != nil {
		t.Fatal(err)
	}
	for _, inst := range all {
		if err := db.WriteStatus(inst.ID, string(inst.Status), inst.Tool); err != nil {
			t.Fatal(err)
		}
	}
	return ids
}

// The daemon keeps no store open for an empty profile: before the fix it held
// one SQLite store (3 fds, ~270 KB of C heap) per profile directory forever.
func TestNotifyDaemon_EmptyProfilesHoldNoOpenStore(t *testing.T) {
	d := newDormantProfilesFixture(t, 120)
	for i := 0; i < 3; i++ {
		d.SyncOnce(context.Background())
	}
	if len(d.storages) != 1 || d.storages["work"] == nil {
		names := make([]string, 0, 3)
		for p := range d.storages {
			if len(names) == 3 {
				break
			}
			names = append(names, p)
		}
		t.Fatalf("daemon holds %d open profile stores (e.g. %v), want only the populated profile \"work\"", len(d.storages), names)
	}
	if _, ok := d.lastStatus["work"]["work-s00"]; !ok {
		t.Fatalf("populated profile not synced: lastStatus[work]=%v", d.lastStatus["work"])
	}
}

// An empty profile starts no session-event journal writer (a goroutine and a
// 28 KB queue each; 84 MB of heap for 3,000 empty profiles before the fix).
func TestNotifyDaemon_EmptyProfilesStartNoJournalWriter(t *testing.T) {
	d := newDormantProfilesFixture(t, 50)
	for i := 0; i < 2; i++ {
		d.SyncOnce(context.Background())
	}
	for p := range d.journalWriters {
		if p != "work" {
			t.Fatalf("empty profile %q has a journal writer (%d writers in all)", p, len(d.journalWriters))
		}
	}
}

// A dormant (empty) profile that gains a session is synced on the next pass,
// exactly as an always-loaded profile would be.
func TestNotifyDaemon_DormantProfileWakesWhenItGainsASession(t *testing.T) {
	d := newDormantProfilesFixture(t, 20)
	for i := 0; i < 3; i++ {
		d.SyncOnce(context.Background())
	}
	ids := seedDormantFixtureProfile(t, "empty-0007", 2)
	d.SyncOnce(context.Background())
	got := d.lastStatus["empty-0007"]
	for _, id := range ids {
		if _, ok := got[id]; !ok {
			t.Fatalf("new session %s in a dormant profile not seen on the next pass: lastStatus=%v", id, got)
		}
	}
}

// A profile that is emptied while the daemon runs lets its store go.
func TestNotifyDaemon_ProfileThatEmptiesReleasesItsStore(t *testing.T) {
	d := newDormantProfilesFixture(t, 2)
	d.SyncOnce(context.Background())
	if d.storages["work"] == nil {
		t.Fatal("populated profile has no open store after a pass")
	}
	st, err := NewStorageWithProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := st.DeleteInstance(fmt.Sprintf("work-s%02d", i)); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	d.SyncOnce(context.Background())
	d.SyncOnce(context.Background())
	if s, ok := d.storages["work"]; ok && s != nil {
		t.Fatal("emptied profile still holds an open store")
	}
}

// BenchmarkNotifyDaemonPass_ManyEmptyProfiles is one steady-state daemon pass
// over 300 empty profiles and one populated one (the deckbox shape, scaled
// down 10x).
func BenchmarkNotifyDaemonPass_ManyEmptyProfiles(b *testing.B) {
	d := newDormantProfilesFixture(b, 300)
	d.SyncOnce(context.Background())
	d.SyncOnce(context.Background())
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.SyncOnce(context.Background())
	}
}

// A steady-state pass fully syncs only the populated profile; the backstop
// re-sync of dormant profiles is bounded per pass.
func TestNotifyDaemon_DormantProfilesSkipTheirLoad(t *testing.T) {
	d := newDormantProfilesFixture(t, 120)
	d.SyncOnce(context.Background())
	if d.lastPassSynced != 121 {
		t.Fatalf("first pass synced %d profiles, want all 121", d.lastPassSynced)
	}
	d.SyncOnce(context.Background())
	if d.lastPassSynced != 1 {
		t.Fatalf("steady-state pass synced %d profiles, want 1 (only the populated one)", d.lastPassSynced)
	}
	old := dormantProfileRecheck
	dormantProfileRecheck = 0
	t.Cleanup(func() { dormantProfileRecheck = old })
	d.SyncOnce(context.Background())
	if want := 1 + dormantRechecksPerPass; d.lastPassSynced != want {
		t.Fatalf("backstop pass synced %d profiles, want %d", d.lastPassSynced, want)
	}
	if len(d.storages) != 1 {
		t.Fatalf("backstop re-sync left %d stores open, want 1", len(d.storages))
	}
}
