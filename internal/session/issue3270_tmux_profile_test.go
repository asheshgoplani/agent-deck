package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/testutil"
	"github.com/asheshgoplani/agent-deck/internal/tmux"
)

func TestDiscoverTmuxSessionsKeepsOtherProfiles(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")

	foreign := profileImportTmuxSession(t, "foreign", "work", "")
	legacy := profileImportTmuxSession(t, "legacy", "", "legacy-work-id")
	own := profileImportTmuxSession(t, "own", "personal", "")
	unmanaged := profileImportTmuxSession(t, "unmanaged", "", "")

	owner, err := NewStorageWithProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = owner.Close() })
	if err := owner.GetDB().SaveInstances([]*statedb.InstanceRow{{
		ID: "legacy-work-id", Title: "legacy", TmuxSession: legacy.Name,
	}}); err != nil {
		t.Fatal(err)
	}

	discovered, err := DiscoverExistingTmuxSessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	got := make(map[string]bool)
	for _, inst := range discovered {
		got[inst.GetTmuxSession().Name] = true
	}
	for _, sess := range []*tmux.Session{foreign, legacy} {
		if got[sess.Name] {
			t.Errorf("discovery imported another profile's session %q", sess.Name)
		}
	}
	for _, sess := range []*tmux.Session{own, unmanaged} {
		if !got[sess.Name] {
			t.Errorf("discovery skipped importable session %q", sess.Name)
		}
	}
}

func TestOldForeignTmuxCopyKeepsOwnerAlive(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")
	sess := profileImportTmuxSession(t, "old-copy", "work", "work-id")
	if err := sess.SetGroupTitleMetadata("work-projects"); err != nil {
		t.Fatal(err)
	}
	copy := &Instance{ID: "personal-copy", Title: "old copy", GroupPath: "recovered", tmuxSession: sess}
	storage, err := NewStorageWithProfile("personal")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*Instance{copy}, nil); err != nil {
		t.Fatal(err)
	}

	group, err := exec.Command("tmux", "show-options", "-v", "-t", sess.Name, "@agentdeck_group_path").CombinedOutput()
	if err != nil {
		t.Fatalf("read group metadata: %v (%s)", err, group)
	}
	if string(group) != "work-projects\n" {
		t.Errorf("saving the old copy set the owner's tmux group to %q", group)
	}

	_ = copy.KillAndWait()
	alive, err := sess.ProbeExists()
	if err != nil || !alive {
		t.Errorf("removing the old copy stopped the owner's tmux session: alive=%v, err=%v", alive, err)
	}
}

func TestStampedTmuxSessionDoesNotNeedOtherProfileDatabases(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")

	path, err := GetDBPathForProfile("unreadable")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	sess := profileImportTmuxSession(t, "known-owner", "personal", "personal-id")
	discovered, err := DiscoverExistingTmuxSessions(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, inst := range discovered {
		if inst.GetTmuxSession().Name == sess.Name {
			return
		}
	}
	t.Fatal("an unreadable unrelated database blocked recovery of the stamped session")
}

func TestRecoveredTmuxSessionUsesLoadedProfile(t *testing.T) {
	testutil.RequireTmux(t)
	isolateUserHomeForShellRestart(t)
	t.Setenv("AGENTDECK_PROFILE", "personal")

	sess := profileImportTmuxSession(t, "loaded-profile", "work", "work-id")
	storage, err := NewStorageWithProfile("work")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.Close() })
	if err := storage.SaveWithGroups([]*Instance{{
		ID: "work-copy", Title: "work session", GroupPath: "recovered", tmuxSession: sess,
	}}, nil); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil || len(loaded) != 1 {
		t.Fatalf("load recovered session: %v (%d rows)", err, len(loaded))
	}
	if err := loaded[0].KillAndWait(); err != nil {
		t.Fatalf("stop using the row's profile: %v", err)
	}
	if alive, err := sess.ProbeExists(); err != nil || alive {
		t.Fatalf("stop left the session alive=%v, err=%v", alive, err)
	}
}

func profileImportTmuxSession(t *testing.T, name, profile, id string) *tmux.Session {
	t.Helper()
	sess := &tmux.Session{Name: tmux.SessionPrefix + "profile-import-" + name + "_01234567"}
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sess.Name, "sleep 120").CombinedOutput(); err != nil {
		t.Fatalf("new tmux session: %v (%s)", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("tmux", "kill-session", "-t", "="+sess.Name).Run() })
	for key, value := range map[string]string{"AGENTDECK_PROFILE": profile, "AGENTDECK_INSTANCE_ID": id} {
		if value != "" {
			if err := sess.SetEnvironment(key, value); err != nil {
				t.Fatal(err)
			}
		}
	}
	return sess
}
