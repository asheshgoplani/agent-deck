package session

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// seedProfileStore creates profiles/<profile>/state.db under root with n
// session rows, bypassing the resolver on purpose so a test can shape both
// roots independently. n == 0 leaves an empty (schema-only) store, which is
// exactly what the stray XDG stores of 2026-09-19/20 looked like.
func seedProfileStore(t *testing.T, root, profile string, n int) string {
	t.Helper()
	dir := filepath.Join(root, ProfilesDirName, profile)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", dbPath, err)
	}
	defer func() { _ = db.Close() }()
	if err := migrateStateDBWithRetry(db); err != nil {
		t.Fatalf("migrate %s: %v", dbPath, err)
	}
	for i := 0; i < n; i++ {
		row := &statedb.InstanceRow{
			ID:           profile + "-" + string(rune('a'+i)),
			Title:        "seed",
			ProjectPath:  root,
			Tool:         "shell",
			Status:       "idle",
			CreatedAt:    time.Now(),
			LastAccessed: time.Now(),
		}
		if err := db.SaveInstance(row); err != nil {
			t.Fatalf("save row: %v", err)
		}
	}
	return dbPath
}

func setupStoreRootEnv(t *testing.T) (xdgRoot, legacyRoot string) {
	t.Helper()
	home, _, xdgDataHome := setupSessionXDGPathEnv(t)
	ResetStoreRootSelection()
	t.Cleanup(ResetStoreRootSelection)
	return filepath.Join(xdgDataHome, "agent-deck"), filepath.Join(home, ".agent-deck")
}

// TestSelectStoreRoot_Matrix is the resolution table: which data root holds
// the profile stores for every combination of legacy and XDG state.
func TestSelectStoreRoot_Matrix(t *testing.T) {
	const absent = -1 // no profiles/ at this root
	cases := []struct {
		name       string
		legacy     int // absent, or session rows in profiles/personal/state.db
		xdg        int
		wantKind   string
		wantReason string
		wantDiverg bool
	}{
		{"neither", absent, absent, "xdg", StoreRootReasonDefaultNew, false},
		{"legacy only", 3, absent, "legacy", StoreRootReasonLegacyOnly, false},
		{"legacy only empty", 0, absent, "legacy", StoreRootReasonLegacyOnly, false},
		{"xdg only", absent, 3, "xdg", StoreRootReasonXDGOnly, false},
		{"xdg only empty", absent, 0, "xdg", StoreRootReasonXDGOnly, false},
		{"legacy populated, xdg empty (stray)", 70, 0, "legacy", StoreRootReasonStrayXDG, true},
		{"legacy larger", 70, 1, "legacy", StoreRootReasonLegacyLarger, true},
		{"xdg larger", 1, 70, "xdg", StoreRootReasonXDGLarger, true},
		{"both equal", 5, 5, "xdg", StoreRootReasonTiePreferXDG, true},
		{"both empty", 0, 0, "xdg", StoreRootReasonTiePreferXDG, true},
		{"legacy empty, xdg populated", 0, 4, "xdg", StoreRootReasonXDGLarger, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdgRoot, legacyRoot := setupStoreRootEnv(t)
			if tc.legacy != absent {
				seedProfileStore(t, legacyRoot, "personal", tc.legacy)
			}
			if tc.xdg != absent {
				seedProfileStore(t, xdgRoot, "personal", tc.xdg)
			}

			sel, err := SelectStoreRoot()
			if err != nil {
				t.Fatalf("SelectStoreRoot(): %v", err)
			}
			want := xdgRoot
			if tc.wantKind == "legacy" {
				want = legacyRoot
			}
			if sel.Active != want || sel.Kind != tc.wantKind {
				t.Fatalf("active = %q (%s), want %q (%s)", sel.Active, sel.Kind, want, tc.wantKind)
			}
			if sel.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", sel.Reason, tc.wantReason)
			}
			if sel.Divergent != tc.wantDiverg {
				t.Fatalf("divergent = %v, want %v", sel.Divergent, tc.wantDiverg)
			}
			if tc.wantDiverg {
				if sel.Legacy.Sessions != tc.legacy || sel.XDG.Sessions != tc.xdg {
					t.Fatalf("counts legacy=%d xdg=%d, want %d/%d", sel.Legacy.Sessions, sel.XDG.Sessions, tc.legacy, tc.xdg)
				}
				if sel.Warning() == "" {
					t.Fatal("divergent layout must carry a warning")
				}
			} else if sel.Warning() != "" {
				t.Fatalf("clean layout must not warn: %q", sel.Warning())
			}

			// The resolvers every CLI/TUI/hook path goes through agree.
			profilesDir, err := GetProfilesDir()
			if err != nil {
				t.Fatal(err)
			}
			if profilesDir != filepath.Join(want, ProfilesDirName) {
				t.Fatalf("GetProfilesDir() = %q, want under %q", profilesDir, want)
			}
		})
	}
}

// TestSelectStoreRoot_DivergentDecisionIsStablePerProcess pins the
// per-process memo: once a process decided on a divergent layout it keeps
// that root, the way the running TUI kept the legacy store during both
// incidents, and never flips mid-run.
func TestSelectStoreRoot_DivergentDecisionIsStablePerProcess(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	seedProfileStore(t, legacyRoot, "personal", 3)
	seedProfileStore(t, xdgRoot, "personal", 0)

	first, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind != "legacy" {
		t.Fatalf("first selection = %s, want legacy", first.Kind)
	}
	// The stray store grows past the legacy one behind this process's back.
	seedProfileStore(t, xdgRoot, "personal", 9)
	second, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if second.Active != first.Active {
		t.Fatalf("selection flipped mid-process: %q -> %q", first.Active, second.Active)
	}
	// A fresh process (reset) re-evaluates and sees the larger XDG store.
	ResetStoreRootSelection()
	third, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if third.Kind != "xdg" || third.Reason != StoreRootReasonXDGLarger {
		t.Fatalf("fresh evaluation = %s/%s, want xdg/%s", third.Kind, third.Reason, StoreRootReasonXDGLarger)
	}
}

// TestNewStorageWithProfile_LegacyPopulatedXDGEmpty_NeverCreatesSecondStore is
// the incident: an empty XDG store beside a populated legacy one. Every path
// that opens the profile store (launch, list, session current, the hook
// handler's status writes, the notifier) goes through NewStorageWithProfile
// and must land on the legacy store, leaving the stray untouched and never
// creating any further store.
func TestNewStorageWithProfile_LegacyPopulatedXDGEmpty_NeverCreatesSecondStore(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")
	legacyDB := seedProfileStore(t, legacyRoot, "personal", 3)
	strayDB := seedProfileStore(t, xdgRoot, "personal", 0)
	strayBefore, err := os.Stat(strayDB)
	if err != nil {
		t.Fatal(err)
	}

	storage, err := NewStorageWithProfile("personal")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	if storage.dbPath != legacyDB {
		t.Fatalf("opened %q, want the populated legacy store %q", storage.dbPath, legacyDB)
	}
	instances, err := storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(instances) != 3 {
		t.Fatalf("loaded %d sessions from the legacy store, want 3", len(instances))
	}
	strayAfter, err := os.Stat(strayDB)
	if err != nil {
		t.Fatal(err)
	}
	if strayAfter.ModTime() != strayBefore.ModTime() || strayAfter.Size() != strayBefore.Size() {
		t.Fatalf("stray XDG store was touched: %v/%d -> %v/%d", strayBefore.ModTime(), strayBefore.Size(), strayAfter.ModTime(), strayAfter.Size())
	}
}

// TestNewStorageWithProfile_LegacyOnly_NeverCreatesXDGStore covers the
// `launch` and hook-handler shape with no XDG data at all: opening the store
// must not create ~/.local/share/agent-deck/profiles.
func TestNewStorageWithProfile_LegacyOnly_NeverCreatesXDGStore(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")
	legacyDB := seedProfileStore(t, legacyRoot, "personal", 2)

	for i := 0; i < 2; i++ { // two processes' worth of opens
		storage, err := NewStorageWithProfile("personal")
		if err != nil {
			t.Fatalf("NewStorageWithProfile: %v", err)
		}
		if storage.dbPath != legacyDB {
			t.Fatalf("opened %q, want %q", storage.dbPath, legacyDB)
		}
		inst := NewInstance("launched-child", legacyRoot)
		if err := storage.Save([]*Instance{inst}); err != nil {
			t.Fatalf("save: %v", err)
		}
		storage.Close()
	}
	if _, err := os.Stat(filepath.Join(xdgRoot, ProfilesDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("XDG profiles dir must not exist, stat err = %v", err)
	}
}

// TestNewStorageWithProfile_RefusesSecondStoreForProfileUnderOtherRoot: when
// the active root is XDG (it holds more sessions) but a profile exists only
// under legacy, opening that profile must not silently create an empty twin.
func TestNewStorageWithProfile_RefusesSecondStoreForProfileUnderOtherRoot(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	seedProfileStore(t, xdgRoot, "personal", 5)
	seedProfileStore(t, legacyRoot, "personal", 1)
	seedProfileStore(t, legacyRoot, "work", 4)

	sel, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if sel.Kind != "xdg" {
		t.Fatalf("active = %s, want xdg", sel.Kind)
	}

	_, err = NewStorageWithProfile("work")
	if !errors.Is(err, ErrStoreExistsElsewhere) {
		t.Fatalf("err = %v, want ErrStoreExistsElsewhere", err)
	}
	if _, statErr := os.Stat(filepath.Join(xdgRoot, ProfilesDirName, "work")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("refused create must leave no directory behind, stat err = %v", statErr)
	}

	// A profile that exists nowhere is still created normally under the
	// active root.
	storage, err := NewStorageWithProfile("fresh")
	if err != nil {
		t.Fatalf("fresh profile: %v", err)
	}
	storage.Close()
	if storage.dbPath != filepath.Join(xdgRoot, ProfilesDirName, "fresh", "state.db") {
		t.Fatalf("fresh profile created at %q", storage.dbPath)
	}
}
