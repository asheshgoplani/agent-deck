package main

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// setupArchiveTestSession creates an isolated temp profile with a single
// session at the given status. It sets HOME and AGENTDECK_PROFILE so that
// in-process calls to loadSessionData / handleSessionArchive use the temp dir.
// Returns the profile name (empty string — callers rely on AGENTDECK_PROFILE)
// and the persisted *Instance.
func setupArchiveTestSession(t *testing.T, status session.Status) (string, *session.Instance) {
	t.Helper()

	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	// Use a unique profile to avoid colliding with the global _test profile
	// set by TestMain. A constant name is fine because HOME is isolated.
	profileName := "archive_test"
	t.Setenv("AGENTDECK_PROFILE", profileName)

	projectDir := filepath.Join(tmpHome, "proj")

	// Create the instance
	inst := session.NewInstance("archive-session-test", projectDir)
	inst.Status = status

	// Persist it
	storage, err := session.NewStorageWithProfile("")
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	instances := []*session.Instance{inst}
	groups := []*session.GroupData{}
	tree := session.NewGroupTreeWithGroups(instances, groups)
	if err := storage.SaveWithGroups(instances, tree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	// Return "" so that handlers look up the profile from AGENTDECK_PROFILE
	return "", inst
}

// findByTitle does a linear scan of instances and returns the first one whose
// Title matches. Returns nil if not found.
func findByTitle(instances []*session.Instance, title string) *session.Instance {
	for _, inst := range instances {
		if inst.Title == title {
			return inst
		}
	}
	return nil
}

func TestArchiveSetsArchivedAt(t *testing.T) {
	profile, inst := setupArchiveTestSession(t, session.StatusStopped)

	handleSessionArchive(profile, []string{inst.Title, "--quiet"})

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded := findByTitle(instances, inst.Title)
	if reloaded == nil || !reloaded.IsArchived() {
		t.Fatalf("session %q must be archived after `session archive`", inst.Title)
	}
}

func TestUnarchiveClearsArchivedAt(t *testing.T) {
	profile, inst := setupArchiveTestSession(t, session.StatusStopped)
	handleSessionArchive(profile, []string{inst.Title, "--quiet"})
	handleSessionUnarchive(profile, []string{inst.Title, "--quiet"})

	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	reloaded := findByTitle(instances, inst.Title)
	if reloaded == nil || reloaded.IsArchived() {
		t.Fatalf("session %q must be unarchived after `session unarchive`", inst.Title)
	}
}

func TestExcludeArchivedForList(t *testing.T) {
	now := timeNowForListTest()
	a := &session.Instance{ID: "1", Title: "active"}
	arch := &session.Instance{ID: "2", Title: "archived", ArchivedAt: now}
	in := []*session.Instance{a, arch}

	// Default (include=false) excludes archived.
	got := excludeArchivedForList(in, false)
	if len(got) != 1 || got[0].Title != "active" {
		t.Fatalf("default list must exclude archived; got %d sessions", len(got))
	}

	// --archived (include=true) keeps everything.
	got = excludeArchivedForList(in, true)
	if len(got) != 2 {
		t.Fatalf("--archived must include archived; got %d sessions", len(got))
	}
}

func timeNowForListTest() time.Time { return time.Now() }
