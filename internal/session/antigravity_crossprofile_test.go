package session

import (
	"os"
	"path/filepath"
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
