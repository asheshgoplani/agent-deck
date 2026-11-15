package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHookCleanupKimiOwnedSession(t *testing.T) {
	s := hookCleanupStorage(t)
	live := &Instance{ID: "kimi-live", Title: "live", Tool: "kimi", Status: StatusRunning, CreatedAt: time.Now()}
	if err := s.SaveWithGroups([]*Instance{live}, NewGroupTree([]*Instance{live})); err != nil {
		t.Fatal(err)
	}
	root := GetHooksDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	for _, id := range []string{"kimi-dead", live.ID} {
		path := filepath.Join(root, id+kimiOwnedSessionSuffix)
		if err := os.WriteFile(path, []byte(`{"id":"native"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := PruneHookArtifacts(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "kimi-dead"+kimiOwnedSessionSuffix)); !os.IsNotExist(err) {
		t.Fatalf("dead session sidecar not pruned (stat err %v)", err)
	}
	if _, err := os.Stat(filepath.Join(root, live.ID+kimiOwnedSessionSuffix)); err != nil {
		t.Fatalf("live session sidecar pruned: %v", err)
	}
}

func TestPruneHermesAbandonedScopeMovesOrRemovesKimiOwnedSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	id := "kimi-prune"
	dir := filepath.Join(GetHooksDir(), "sandbox", id)
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	abandoned := filepath.Join(dir, id+kimiOwnedSessionSuffix)
	active := filepath.Join(GetHooksDir(), id+kimiOwnedSessionSuffix)
	write(filepath.Join(dir, id+".lock"), "x")
	write(abandoned, `{"id":"native-abandoned"}`)
	pruneHermesAbandonedScope(id, dir)
	if rec, ok := readKimiOwnedSession(active); !ok || rec.ID != "native-abandoned" {
		t.Fatalf("record not moved to the active scope: %+v %v", rec, ok)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("abandoned sandbox scope not removed (stat err %v)", err)
	}

	write(filepath.Join(dir, id+".lock"), "x")
	write(abandoned, `{"id":"native-other"}`)
	pruneHermesAbandonedScope(id, dir)
	if rec, _ := readKimiOwnedSession(active); rec.ID != "native-abandoned" {
		t.Fatalf("active record replaced by the abandoned one: %+v", rec)
	}
	if _, err := os.Stat(abandoned); !os.IsNotExist(err) {
		t.Fatalf("abandoned record left (stat err %v)", err)
	}
}

func TestKimiOwnedSessionColdResumeRotationAndFreshClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("kimi-owned-resume", t.TempDir(), "kimi")
	saveKimiRow(t, db, inst)

	if err := WriteKimiOwnedSessionID(inst.ID, " native-first "); err != nil {
		t.Fatalf("WriteKimiOwnedSessionID: %v", err)
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "native-first" || inst.kimiBoundAt.IsZero() {
		t.Fatalf("cold resume binding = %q at %v", inst.KimiSessionID, inst.kimiBoundAt)
	}
	if got, _ := persistedKimiBinding(t, db, inst.ID); got != "native-first" {
		t.Fatalf("persisted cold binding = %q, want native-first", got)
	}
	if command := inst.buildKimiCommand("kimi"); !strings.HasSuffix(command, "kimi --session native-first") {
		t.Fatalf("cold resume command = %q", command)
	}

	if err := WriteKimiOwnedSessionID(inst.ID, "native-rotated"); err != nil {
		t.Fatalf("rotate owned session: %v", err)
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "native-rotated" {
		t.Fatalf("rotated binding = %q, want native-rotated", inst.KimiSessionID)
	}

	sandboxDir := hermesHookScope(inst.ID, true)
	if err := os.MkdirAll(sandboxDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sandboxDir, inst.ID+kimiOwnedSessionSuffix), []byte(`{"id":"native-sandbox"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	inst.clearSessionBindingForFreshStart()
	for _, dir := range []string{hermesHookScope(inst.ID, false), sandboxDir} {
		if _, err := os.Stat(filepath.Join(dir, inst.ID+kimiOwnedSessionSuffix)); !os.IsNotExist(err) {
			t.Fatalf("fresh restart left owned session record in %s; err=%v", dir, err)
		}
	}
	inst.prepareKimiResume()
	if inst.KimiSessionID != "" {
		t.Fatalf("fresh restart restored session %q", inst.KimiSessionID)
	}
}
