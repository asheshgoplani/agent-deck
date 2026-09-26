package ui

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/web"
)

// #2368: the web move endpoint goes through WebMutator.MoveSessionToGroup,
// which must land a session exactly where `agent-deck group move` would and
// persist it, including in headless (`web --no-tui`) mode.
func TestIssue2368_WebMoveSessionToGroup(t *testing.T) {
	h, storage := newHeadlessHomeForTest(t, "_test_2368_move")
	s1 := seedSession(t, storage, nil, "issue2368-mv-001", "mover")
	_ = seedSession(t, storage, []*session.Instance{s1}, "issue2368-mv-002", "stayer")
	m := NewWebMutator(h)

	if _, err := m.CreateGroup("Review", ""); err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	groupOf := func(id string) string {
		t.Helper()
		instances, _, err := storage.LoadWithGroups()
		if err != nil {
			t.Fatalf("reload: %v", err)
		}
		for _, inst := range instances {
			if inst.ID == id {
				return inst.GroupPath
			}
		}
		t.Fatalf("session %s missing after move", id)
		return ""
	}

	// Case-insensitive match against the existing group, like the CLI.
	movedTo, restart, err := m.MoveSessionToGroup("issue2368-mv-001", "review")
	if err != nil {
		t.Fatalf("MoveSessionToGroup: %v", err)
	}
	if movedTo != "Review" || groupOf("issue2368-mv-001") != "Review" {
		t.Fatalf("moved to %q (stored %q), want the existing Review group", movedTo, groupOf("issue2368-mv-001"))
	}
	if restart {
		t.Fatal("a shell session never needs a restart for a config dir change")
	}
	if got := groupOf("issue2368-mv-002"); got != session.DefaultGroupPath {
		t.Fatalf("untouched session moved to %q", got)
	}

	// A missing group is created, and "root" goes back to the default group.
	if movedTo, _, err = m.MoveSessionToGroup("issue2368-mv-001", "brand-new"); err != nil || movedTo != "brand-new" {
		t.Fatalf("move to new group: movedTo=%q err=%v", movedTo, err)
	}
	if _, groups, _ := storage.LoadWithGroups(); !hasGroup(groups, "brand-new") {
		t.Fatalf("auto-created group not persisted: %+v", groups)
	}
	if movedTo, _, err = m.MoveSessionToGroup("issue2368-mv-001", "root"); err != nil || movedTo != session.DefaultGroupPath {
		t.Fatalf("move to root: movedTo=%q err=%v", movedTo, err)
	}
	if got := groupOf("issue2368-mv-001"); got != session.DefaultGroupPath {
		t.Fatalf("stored group after root move = %q", got)
	}

	if _, _, err := m.MoveSessionToGroup("does-not-exist", "review"); !errors.Is(err, web.ErrSessionNotFound) {
		t.Fatalf("unknown id: err=%v, want ErrSessionNotFound", err)
	}
}

// Moving a Claude session into a group with its own config_dir changes which
// Claude account it launches under on the next restart; like `group move`,
// nothing is migrated, and the endpoint reports restartRequired instead.
func TestIssue2368_WebMoveReportsConfigDirChange(t *testing.T) {
	configPath, err := session.GetUserConfigPath()
	if err != nil {
		t.Fatalf("GetUserConfigPath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	config := "[groups.\"team-a\".claude]\nconfig_dir = \"~/.claude-team-a\"\n"
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	session.ClearUserConfigCache()
	t.Cleanup(func() {
		_ = os.Remove(configPath)
		session.ClearUserConfigCache()
	})

	h, storage := newHeadlessHomeForTest(t, "_test_2368_configdir")
	inst := seedSession(t, storage, nil, "issue2368-cd-001", "claude-one")
	inst.Tool, inst.Command = "claude", "claude"
	if err := storage.SaveWithGroups([]*session.Instance{inst}, session.NewGroupTree([]*session.Instance{inst})); err != nil {
		t.Fatal(err)
	}
	m := NewWebMutator(h)

	if _, restart, err := m.MoveSessionToGroup(inst.ID, "plain"); err != nil || restart {
		t.Fatalf("move to plain group: restart=%v err=%v, want no restart", restart, err)
	}
	if _, restart, err := m.MoveSessionToGroup(inst.ID, "team-a"); err != nil || !restart {
		t.Fatalf("move into team-a: restart=%v err=%v, want restartRequired", restart, err)
	}
}

func hasGroup(groups []*session.GroupData, path string) bool {
	for _, g := range groups {
		if g.Path == path {
			return true
		}
	}
	return false
}
