package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
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

// writeMarker pins the XDG root the way `migrate-paths` does.
func writeMarker(t *testing.T, xdgRoot string) {
	t.Helper()
	dir := filepath.Join(xdgRoot, ProfilesDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, StoreRootMarkerName), []byte("xdg\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// makeUnreadable takes away every permission on a store so the read-only
// count fails; restored on cleanup so t.TempDir can remove it.
func makeUnreadable(t *testing.T, dbPath string) {
	t.Helper()
	if os.Getuid() == 0 {
		t.Skip("root reads mode 000 files")
	}
	if err := os.Chmod(dbPath, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dbPath, 0o600) })
}

// TestSelectStoreRoot_Matrix is the resolution table: which data root holds
// the profile stores for every combination of legacy and XDG state. Counts
// only matter as "empty or not"; the marker decides the rest.
func TestSelectStoreRoot_Matrix(t *testing.T) {
	const absent = -1 // no profiles/ at this root
	cases := []struct {
		name       string
		legacy     int // absent, or session rows in profiles/personal/state.db
		xdg        int
		marker     bool
		wantKind   string
		wantReason string
		wantDiverg bool
		wantWarn   bool
	}{
		{"neither", absent, absent, false, "xdg", StoreRootReasonDefaultNew, false, false},
		{"legacy only", 3, absent, false, "legacy", StoreRootReasonLegacyOnly, false, false},
		{"legacy only empty", 0, absent, false, "legacy", StoreRootReasonLegacyOnly, false, false},
		{"xdg only", absent, 3, false, "xdg", StoreRootReasonXDGOnly, false, false},
		{"xdg only empty", absent, 0, false, "xdg", StoreRootReasonXDGOnly, false, false},
		{"xdg only with marker", absent, 3, true, "xdg", StoreRootReasonXDGOnly, false, false},
		{"legacy populated, xdg empty (stray)", 70, 0, false, "legacy", StoreRootReasonStrayXDG, true, true},
		{"legacy empty, xdg populated (stray legacy)", 0, 4, false, "xdg", StoreRootReasonStrayLegacy, true, true},
		{"both populated, legacy larger, no marker", 70, 1, false, "legacy", StoreRootReasonNoMarkerLegacy, true, true},
		{"both populated, xdg larger, no marker", 1, 70, false, "legacy", StoreRootReasonNoMarkerLegacy, true, true},
		{"both equal, no marker", 5, 5, false, "legacy", StoreRootReasonNoMarkerLegacy, true, true},
		{"both empty, no marker", 0, 0, false, "legacy", StoreRootReasonNoMarkerLegacy, true, true},
		{"both populated, marker (migrated)", 70, 1, true, "xdg", StoreRootReasonMarker, true, false},
		{"marker, xdg emptied after migration", 3, 0, true, "xdg", StoreRootReasonMarker, true, false},
		{"marker, both empty", 0, 0, true, "xdg", StoreRootReasonMarker, true, false},
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
			if tc.marker {
				writeMarker(t, xdgRoot)
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
			if tc.wantDiverg && (sel.Legacy.Sessions != tc.legacy || sel.XDG.Sessions != tc.xdg) {
				t.Fatalf("counts legacy=%d xdg=%d, want %d/%d", sel.Legacy.Sessions, sel.XDG.Sessions, tc.legacy, tc.xdg)
			}
			if (sel.Warning() != "") != tc.wantWarn {
				t.Fatalf("warning = %q, want warn=%v", sel.Warning(), tc.wantWarn)
			}
			if w := sel.Warning(); w != "" && !strings.Contains(w, "migrate-paths") && !strings.Contains(w, "move") {
				t.Fatalf("warning names no remedy: %q", w)
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
	// The marker appears behind this process's back (migrate-paths ran).
	writeMarker(t, xdgRoot)
	second, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if second.Active != first.Active {
		t.Fatalf("selection flipped mid-process: %q -> %q", first.Active, second.Active)
	}
	// A fresh process (reset) re-evaluates and honours the marker.
	ResetStoreRootSelection()
	third, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if third.Kind != "xdg" || third.Reason != StoreRootReasonMarker {
		t.Fatalf("fresh evaluation = %s/%s, want xdg/%s", third.Kind, third.Reason, StoreRootReasonMarker)
	}
}

// TestSelectStoreRoot_UnreadableIsUnknownNeverEmpty: a store that cannot be
// opened is unknown. It never loses to an empty store (the stray shape with
// the legacy store locked away must still fail closed to legacy), beside a
// readable populated store the readable one is used with a WARN, and the
// marker still pins XDG.
func TestSelectStoreRoot_UnreadableIsUnknownNeverEmpty(t *testing.T) {
	cases := []struct {
		name       string
		legacy     int
		xdg        int
		unreadable string // "legacy" or "xdg"
		marker     bool
		wantKind   string
		wantReason string
	}{
		{"legacy unreadable beside empty xdg stray", 3, 0, "legacy", false, "legacy", StoreRootReasonStrayXDG},
		{"xdg unreadable beside populated legacy", 3, 2, "xdg", false, "legacy", StoreRootReasonXDGUnreadable},
		{"legacy unreadable beside populated xdg", 3, 2, "legacy", false, "xdg", StoreRootReasonLegacyUnreadable},
		{"marker beats an unreadable legacy copy", 3, 2, "legacy", true, "xdg", StoreRootReasonMarker},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			xdgRoot, legacyRoot := setupStoreRootEnv(t)
			legacyDB := seedProfileStore(t, legacyRoot, "personal", tc.legacy)
			xdgDB := seedProfileStore(t, xdgRoot, "personal", tc.xdg)
			if tc.marker {
				writeMarker(t, xdgRoot)
			}
			if tc.unreadable == "legacy" {
				makeUnreadable(t, legacyDB)
			} else {
				makeUnreadable(t, xdgDB)
			}
			sel, err := SelectStoreRoot()
			if err != nil {
				t.Fatal(err)
			}
			if sel.Kind != tc.wantKind || sel.Reason != tc.wantReason {
				t.Fatalf("selection = %s/%s, want %s/%s (legacy %+v, xdg %+v)", sel.Kind, sel.Reason, tc.wantKind, tc.wantReason, sel.Legacy, sel.XDG)
			}
			broken := sel.Legacy
			if tc.unreadable == "xdg" {
				broken = sel.XDG
			}
			if broken.Unreadable != 1 || broken.Profiles["personal"] != -1 || broken.Empty() {
				t.Fatalf("unreadable root reported as %+v, want 1 unreadable store, never empty", broken)
			}
			if tc.wantReason != StoreRootReasonMarker && !strings.Contains(sel.Warning(), "unreadable") && tc.wantReason != StoreRootReasonStrayXDG {
				t.Fatalf("warning = %q", sel.Warning())
			}
		})
	}
}

// TestStoreRootReport_CountsCleanLayout: doctor's report carries real row
// counts in the single-root layout too, not 0.
func TestStoreRootReport_CountsCleanLayout(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	seedProfileStore(t, legacyRoot, "personal", 3)
	seedProfileStore(t, legacyRoot, "work", 2)

	sel, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if sel.Legacy.Counted || sel.Legacy.Sessions != 0 {
		t.Fatalf("SelectStoreRoot must not count a clean layout: %+v", sel.Legacy)
	}
	report, err := StoreRootReport()
	if err != nil {
		t.Fatal(err)
	}
	if !report.Legacy.Counted || report.Legacy.Sessions != 5 || report.Legacy.Profiles["work"] != 2 {
		t.Fatalf("report legacy = %+v, want 5 sessions (personal=3, work=2)", report.Legacy)
	}
	if !report.XDG.Counted || report.XDG.HasProfiles {
		t.Fatalf("report xdg = %+v, want counted, no profiles", report.XDG)
	}
	if _, err := os.Stat(filepath.Join(xdgRoot, ProfilesDirName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("report created the XDG profiles dir: %v", err)
	}
}

// TestSelectStoreRoot_MigratedUserStaysOnXDG is the review's P1: after
// `migrate-paths` (marker written, legacy copy left in place) removing
// sessions from the XDG store and creating a profile there must keep every
// fresh process on XDG and the new profile reachable.
func TestSelectStoreRoot_MigratedUserStaysOnXDG(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")
	seedProfileStore(t, legacyRoot, "personal", 3)
	seedProfileStore(t, xdgRoot, "personal", 3)
	marker, err := MarkXDGStoreActive()
	if err != nil {
		t.Fatal(err)
	}
	if marker != filepath.Join(xdgRoot, ProfilesDirName, StoreRootMarkerName) {
		t.Fatalf("marker at %q", marker)
	}

	storage, err := NewStorageWithProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	instances, err := storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.Save(instances[:1]); err != nil { // remove two of three
		t.Fatal(err)
	}
	storage.Close()
	work, err := NewStorageWithProfile("work")
	if err != nil {
		t.Fatalf("profile created after migration: %v", err)
	}
	work.Close()

	for i := 0; i < 3; i++ { // fresh processes
		ResetStoreRootSelection()
		sel, err := SelectStoreRoot()
		if err != nil {
			t.Fatal(err)
		}
		if sel.Kind != "xdg" || sel.Reason != StoreRootReasonMarker {
			t.Fatalf("fresh process %d flipped to %s/%s (legacy %d rows, xdg %d rows)", i, sel.Kind, sel.Reason, sel.Legacy.Sessions, sel.XDG.Sessions)
		}
		if sel.Warning() != "" {
			t.Fatalf("migrated layout must not warn: %q", sel.Warning())
		}
		w, err := NewStorageWithProfile("work")
		if err != nil {
			t.Fatalf("work profile unreachable from a fresh process: %v", err)
		}
		w.Close()
	}
	if _, err := os.Stat(filepath.Join(legacyRoot, ProfilesDirName, "work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("work profile leaked into the legacy root: %v", err)
	}
}

// TestSetAsideStrayXDGStore renames only the stray shape and nothing else.
func TestSetAsideStrayXDGStore(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	seedProfileStore(t, legacyRoot, "personal", 3)
	strayDB := seedProfileStore(t, xdgRoot, "personal", 0)

	aside, err := SetAsideStrayXDGStore()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(aside, filepath.Join(xdgRoot, ProfilesDirName)+".stray-") {
		t.Fatalf("aside = %q", aside)
	}
	if _, err := os.Stat(strayDB); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stray still in place: %v", err)
	}
	if _, err := os.Stat(filepath.Join(aside, "personal", "state.db")); err != nil {
		t.Fatalf("stray not preserved under %s: %v", aside, err)
	}
	sel, err := SelectStoreRoot()
	if err != nil {
		t.Fatal(err)
	}
	if sel.Reason != StoreRootReasonLegacyOnly {
		t.Fatalf("after set-aside reason = %s", sel.Reason)
	}

	// A populated XDG store is never set aside.
	seedProfileStore(t, xdgRoot, "personal", 2)
	ResetStoreRootSelection()
	if aside, err := SetAsideStrayXDGStore(); err != nil || aside != "" {
		t.Fatalf("populated store set aside: %q, %v", aside, err)
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
// the active root is XDG (marker) but a profile exists only under legacy,
// opening that profile must not silently create an empty twin.
func TestNewStorageWithProfile_RefusesSecondStoreForProfileUnderOtherRoot(t *testing.T) {
	xdgRoot, legacyRoot := setupStoreRootEnv(t)
	seedProfileStore(t, xdgRoot, "personal", 5)
	seedProfileStore(t, legacyRoot, "personal", 1)
	seedProfileStore(t, legacyRoot, "work", 4)
	writeMarker(t, xdgRoot)

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
