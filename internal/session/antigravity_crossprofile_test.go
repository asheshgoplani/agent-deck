package session

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// The Antigravity cache under ~/.gemini is shared by every agent-deck profile
// of the user, so the ownership and sibling checks must read every local
// profile's state DB, not only the current one.

// setupAntigravityCrossProfileHome isolates HOME and the data root and writes
// one shared conversation entry for workDir.
func setupAntigravityCrossProfileHome(t *testing.T, convID string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	workDir := filepath.Join(home, "project")
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeAntigravityMetadata(t, filepath.Join(home, ".gemini", "antigravity-cli"), map[string]any{
		convID: antigravityTestConversationEntry(convID, workDir, time.Now(), false),
	})
	prev := statedb.GetGlobal()
	t.Cleanup(func() { statedb.SetGlobal(prev) })
	return workDir
}

// newAntigravityProfileStorage opens the state DB of a named local profile,
// where ListProfiles finds it.
func newAntigravityProfileStorage(t *testing.T, profile string) *Storage {
	t.Helper()
	dbPath, err := GetDBPathForProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("open %s: %v", profile, err)
	}
	if err := db.Migrate(); err != nil {
		t.Fatalf("migrate %s: %v", profile, err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return &Storage{db: db, dbPath: dbPath, profile: profile}
}

func saveAntigravityProfileSession(t *testing.T, st *Storage, title, workDir, convID string) *Instance {
	t.Helper()
	inst := NewInstance(title, workDir)
	inst.Tool, inst.Command = "antigravity", "agy"
	inst.AntigravityStartedAt = time.Now().Add(-time.Minute).UnixMilli()
	if convID != "" {
		inst.AntigravitySessionID = convID
		inst.AntigravityDetectedAt = time.Now()
	}
	if err := st.SaveWithGroups([]*Instance{inst}, NewGroupTreeWithGroups([]*Instance{inst}, nil)); err != nil {
		t.Fatalf("save %s: %v", title, err)
	}
	return inst
}

// A session in profile B already owns conv-b. An unbound session in profile A
// in the same directory must not adopt it.
func TestAntigravityCrossProfileBoundConversationNotAdopted(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-b")

	storageB := newAntigravityProfileStorage(t, "profile-b")
	saveAntigravityProfileSession(t, storageB, "agy-owner-profile-b", workDir, "conv-b")

	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	x := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	if got := x.WaitForAntigravitySession(AntigravityConfirmWait); got != "" {
		t.Fatalf("profile A session bound %q, which profile B's session owns", got)
	}
}

// Two unbound sessions in two profiles, same directory: neither can tell which
// pane created the new conversation, so neither may bind it.
func TestAntigravityCrossProfileUnboundSiblingsBothBind(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-new")

	storageA := newAntigravityProfileStorage(t, "profile-a")
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")
	storageB := newAntigravityProfileStorage(t, "profile-b")
	b := saveAntigravityProfileSession(t, storageB, "agy-profile-b", workDir, "")

	statedb.SetGlobal(storageA.GetDB())
	gotA := a.WaitForAntigravitySession(AntigravityConfirmWait)
	statedb.SetGlobal(storageB.GetDB())
	gotB := b.WaitForAntigravitySession(AntigravityConfirmWait)
	if gotA != "" || gotB != "" {
		t.Fatalf("unbound sessions in two profiles bound %q and %q", gotA, gotB)
	}
}

// A stopped session in another profile cannot have created the conversation,
// and an unrelated other profile must not block binding.
func TestAntigravityCrossProfileStoppedSessionDoesNotBlock(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-mine")

	storageB := newAntigravityProfileStorage(t, "profile-b")
	stopped := NewInstance("agy-stopped-b", workDir)
	stopped.Tool, stopped.Command = "antigravity", "agy"
	stopped.Status = StatusStopped
	if err := storageB.SaveWithGroups([]*Instance{stopped}, NewGroupTreeWithGroups([]*Instance{stopped}, nil)); err != nil {
		t.Fatalf("save B: %v", err)
	}

	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")
	if got := a.WaitForAntigravitySession(AntigravityConfirmWait); got != "conv-mine" {
		t.Fatalf("session bound %q, want conv-mine", got)
	}
}

// An unreadable other profile means its sessions are unknown: fail closed.
func TestAntigravityCrossProfileUnreadableProfileFailsClosed(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-x")

	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	badPath, err := GetDBPathForProfile("profile-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(badPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !a.antigravitySessionIDBoundElsewhere("conv-x") {
		t.Fatal("unreadable profile DB must fail closed for the ownership check")
	}
	if !a.antigravityUnboundSiblingSharesWorkspace() {
		t.Fatal("unreadable profile DB must fail closed for the sibling check")
	}
}

// The fail-closed warning must be logged once per session per cause, not on
// every scan.
func TestAntigravityFailClosedWarningLoggedOncePerCause(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-x")
	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	badPath, err := GetDBPathForProfile("profile-bad")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(badPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	orig := sessionLog
	sessionLog = slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	t.Cleanup(func() { sessionLog = orig })

	for n := 0; n < 5; n++ {
		if !a.antigravitySessionIDBoundElsewhere("conv-x") || !a.antigravityUnboundSiblingSharesWorkspace() {
			t.Fatal("unreadable profile DB must keep failing closed")
		}
	}
	if got := strings.Count(buf.String(), "antigravity_binding_lookup_failed"); got != 1 {
		t.Fatalf("binding lookup warning logged %d times, want 1\n%s", got, buf.String())
	}
	if got := strings.Count(buf.String(), "antigravity_sibling_lookup_failed"); got != 1 {
		t.Fatalf("sibling lookup warning logged %d times, want 1\n%s", got, buf.String())
	}
}

// countAntigravityProfileScans counts all-profile scans for the test.
func countAntigravityProfileScans(t *testing.T) *int {
	t.Helper()
	n := 0
	antigravityProfileScanHook = func() { n++ }
	t.Cleanup(func() { antigravityProfileScanHook = nil })
	return &n
}

// A candidate that another profile's session owns stays owned, so repeated
// checks within the recheck window must not reopen every profile DB.
func TestAntigravityCrossProfileOwnedResultReused(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-b")
	storageB := newAntigravityProfileStorage(t, "profile-b")
	saveAntigravityProfileSession(t, storageB, "agy-owner-profile-b", workDir, "conv-b")
	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	scans := countAntigravityProfileScans(t)
	for n := 0; n < 5; n++ {
		if !a.antigravitySessionIDBoundElsewhere("conv-b") {
			t.Fatal("conv-b is owned by profile B's session")
		}
	}
	if *scans != 1 {
		t.Fatalf("owned-elsewhere check scanned the profiles %d times, want 1", *scans)
	}
}

// An unbound live sibling in another profile keeps the workspace ambiguous,
// so repeated checks within the recheck window must not reopen every DB.
func TestAntigravityCrossProfileAmbiguousResultReused(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-new")
	storageB := newAntigravityProfileStorage(t, "profile-b")
	saveAntigravityProfileSession(t, storageB, "agy-profile-b", workDir, "")
	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	scans := countAntigravityProfileScans(t)
	for n := 0; n < 5; n++ {
		if !a.antigravityUnboundSiblingSharesWorkspace() {
			t.Fatal("profile B's unbound sibling makes the workspace ambiguous")
		}
	}
	if *scans != 1 {
		t.Fatalf("sibling check scanned the profiles %d times, want 1", *scans)
	}
}

// A "free" answer is what allows a bind, so it is never reused: every check
// that could bind reads the DBs again.
func TestAntigravityCrossProfileFreeResultNotReused(t *testing.T) {
	workDir := setupAntigravityCrossProfileHome(t, "conv-mine")
	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")
	storageB := newAntigravityProfileStorage(t, "profile-b")

	scans := countAntigravityProfileScans(t)
	for n := 0; n < 3; n++ {
		if a.antigravitySessionIDBoundElsewhere("conv-mine") {
			t.Fatal("conv-mine is not owned")
		}
		if a.antigravityUnboundSiblingSharesWorkspace() {
			t.Fatal("no sibling shares the workspace")
		}
	}
	if *scans != 6 {
		t.Fatalf("free answers scanned the profiles %d times, want 6 (never reused)", *scans)
	}

	// Once another profile's session takes conv-mine, the very next check
	// must see it.
	saveAntigravityProfileSession(t, storageB, "agy-owner-profile-b", workDir, "conv-mine")
	if !a.antigravitySessionIDBoundElsewhere("conv-mine") {
		t.Fatal("a free answer was reused after the conversation became owned")
	}
}

// A blocking answer is reused only for the recheck window. After it, the DBs
// are read again, so a conversation its owner gave up becomes bindable.
func TestAntigravityCrossProfileBlockedResultExpires(t *testing.T) {
	prev := antigravityBlockedRecheckInterval
	antigravityBlockedRecheckInterval = 50 * time.Millisecond
	t.Cleanup(func() { antigravityBlockedRecheckInterval = prev })

	workDir := setupAntigravityCrossProfileHome(t, "conv-b")
	storageB := newAntigravityProfileStorage(t, "profile-b")
	owner := saveAntigravityProfileSession(t, storageB, "agy-owner-profile-b", workDir, "conv-b")
	storageA := newAntigravityProfileStorage(t, "profile-a")
	statedb.SetGlobal(storageA.GetDB())
	a := saveAntigravityProfileSession(t, storageA, "agy-profile-a", workDir, "")

	scans := countAntigravityProfileScans(t)
	for i := 0; i < 2; i++ {
		if !a.antigravitySessionIDBoundElsewhere("conv-b") {
			t.Fatalf("check %d: conv-b is owned by profile B's session", i+1)
		}
	}
	if *scans != 1 {
		t.Fatalf("scanned %d times within the window, want 1", *scans)
	}

	if err := storageB.GetDB().WriteAntigravitySessionBinding(owner.ID, "", time.Time{}); err != nil {
		t.Fatalf("clear owner binding: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	if a.antigravitySessionIDBoundElsewhere("conv-b") {
		t.Fatal("blocking answer outlived the recheck window")
	}
	if *scans != 2 {
		t.Fatalf("scanned %d times after the window, want 2", *scans)
	}
}
