package session

import (
	"encoding/json"
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

func TestKimiOwnedSessionRejectsForeignCwdOnRestart(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	workspace := t.TempDir()
	inst := NewInstanceWithTool("kimi-foreign-restart", workspace, "kimi")
	saveKimiRow(t, db, inst)
	path := filepath.Join(GetHooksDir(), inst.ID+kimiOwnedSessionSuffix)
	writeRecord := func(sessionID, cwd string) {
		t.Helper()
		data, err := json.Marshal(kimiOwnedSession{ID: sessionID, Cwd: cwd, At: time.Now().UnixNano()})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	writeRecord("foreign", t.TempDir())
	inst.prepareKimiResume()
	if inst.KimiSessionID != "" {
		t.Fatalf("restart adopted foreign Kimi session %q", inst.KimiSessionID)
	}
	if got, _ := persistedKimiBinding(t, db, inst.ID); got != "" {
		t.Fatalf("restart persisted foreign Kimi session %q", got)
	}

	writeRecord("owned", filepath.Join(workspace, "subdir"))
	inst.prepareKimiResume()
	if inst.KimiSessionID != "owned" {
		t.Fatalf("restart ignored owned Kimi session: %q", inst.KimiSessionID)
	}
}

func TestKimiOwnedSessionColdResumeRotationAndFreshClear(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	db := withTempGlobalStateDB(t)
	inst := NewInstanceWithTool("kimi-owned-resume", t.TempDir(), "kimi")
	saveKimiRow(t, db, inst)

	if err := WriteKimiOwnedSessionID(inst.ID, " native-first ", inst.ProjectPath, inst.hermesCwdRoots()); err != nil {
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

	if err := WriteKimiOwnedSessionID(inst.ID, "native-rotated", inst.ProjectPath, inst.hermesCwdRoots()); err != nil {
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

func TestKimiOwnedSessionIDValidation(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	inst := NewInstanceWithTool("kimi-invalid-owned", t.TempDir(), "kimi")
	if err := WriteKimiOwnedSessionID(inst.ID, "known-good", inst.ProjectPath, inst.hermesCwdRoots()); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(GetHooksDir(), inst.ID+kimiOwnedSessionSuffix)
	base := time.Now().Add(-time.Hour)

	for n, tc := range kimiSessionIDValidationCases() {
		if tc.valid {
			continue
		}
		t.Run(tc.name, func(t *testing.T) {
			if err := WriteKimiOwnedSessionID(inst.ID, "known-good", inst.ProjectPath, inst.hermesCwdRoots()); err != nil {
				t.Fatal(err)
			}
			if err := WriteKimiOwnedSessionID(inst.ID, tc.id, inst.ProjectPath, inst.hermesCwdRoots()); err == nil {
				t.Errorf("WriteKimiOwnedSessionID(%q) succeeded, want validation error", tc.id)
			}
			if record, ok := readKimiOwnedSession(path); !ok || record.ID != "known-good" {
				t.Errorf("invalid write replaced sidecar: record=%+v ok=%v", record, ok)
			}

			data, err := json.Marshal(kimiOwnedSession{
				ID: tc.id, Cwd: inst.ProjectPath, At: base.Add(time.Duration(n+1) * time.Second).UnixNano(),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			inst.KimiSessionID = "known-good"
			inst.kimiBoundAt = base
			inst.prepareKimiResume()
			if inst.KimiSessionID != "known-good" {
				t.Errorf("malformed sidecar replaced valid binding with %q", inst.KimiSessionID)
			}
		})
	}
}

// TestRestartKimiSeedFailureRestoresLiveBinding covers a failure after the
// fallback recreate has replaced the in-memory tmux binding but before the
// replacement has been started. The initial restart generation seed succeeds;
// the kill-session shim then makes the later waiting-baseline seed fail while
// leaving the old pane alive. Restart must not strand that pane by retaining
// the name of a replacement that does not exist.
func TestRestartKimiSeedFailureRestoresLiveBinding(t *testing.T) {
	skipIfNoTmuxBinary(t)

	const oldName = "agentdeck_restartbinding_kimi_seed"
	_, inst := restartPersistFixture(t, "_test_restart_binding_kimi_seed", oldName)
	inst.Tool = "kimi"
	realTmux := seedRealTmuxSession(t, oldName)
	killSessionsCreatedDuring(t, realTmux)

	generationPath := filepath.Join(GetHooksDir(), inst.ID+".generation.json")
	shimDir := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in\n" +
		`  *" kill-session "*) rm -f ` + generationPath + `; mkdir ` + generationPath + `; kill -9 $$ ;;` + "\n" +
		"esac\nexec " + realTmux + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(shimDir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatalf("write tmux shim: %v", err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	err := inst.Restart()
	if err == nil || !strings.Contains(err.Error(), "seed kimi restart baseline") {
		t.Fatalf("Restart() error = %v, want the waiting-baseline seed failure", err)
	}
	if !realTmuxHasSession(realTmux, oldName) {
		t.Fatalf("fixture: old session %q is gone; this test needs it alive", oldName)
	}
	live := inst.GetTmuxSession()
	if live == nil || live.Name != oldName {
		name := "<nil>"
		if live != nil {
			name = live.Name
		}
		t.Fatalf("binding after failed Kimi seed = %q, want the still-live %q", name, oldName)
	}
}
