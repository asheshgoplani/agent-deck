package session

// Issue #666 — group disappearance regression tests.
//
// Two hazards are covered here:
//
//   1. Load-time fallback: a row with group_path = '' in SQLite must NOT be
//      silently re-derived from ProjectPath via extractGroupPath. That is
//      how sessions "disappear from their assigned group" and surface under
//      a path-derived name (e.g. a /tmp session ends up in group "tmp").
//      The safe behavior is to route the survivor to DefaultGroupPath and
//      log the event — explicit, recoverable, non-silent.
//
//   2. Save-time guard: if an in-memory Instance somehow has GroupPath = ""
//      (regression in a future fork/move path, or direct field mutation),
//      SaveWithGroups must normalize to DefaultGroupPath rather than writing
//      an empty string that the next load has to defend against.

import (
	"testing"
	"time"
)

// TestIssue666_LoadRowWithEmptyGroupPath_FallsBackToDefaultNotPathDerived
// reproduces the primary #666 symptom: a session stored with an empty
// group_path reloads under a path-derived group name (e.g. "tmp" for
// /tmp sessions) without any user action.
//
// The safe contract: empty group_path at load time → DefaultGroupPath.
// NOT extractGroupPath(ProjectPath), which is non-deterministic and
// silently lies about where the session used to live.
func TestIssue666_LoadRowWithEmptyGroupPath_FallsBackToDefaultNotPathDerived(t *testing.T) {
	s := newTestStorage(t)

	// Simulate a legacy row / future regression: group_path stored as ''.
	// ProjectPath intentionally chosen so extractGroupPath would return
	// something observable ("tmp") — the test proves we do NOT do that.
	_, err := s.db.DB().Exec(`
		INSERT INTO instances (
			id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session,
			created_at, last_accessed,
			parent_session_id, is_conductor, worktree_path, worktree_repo, worktree_branch,
			tool_data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"issue666-empty-group", "Empty Group Session", "/tmp", "", 0,
		"", "", "shell", "idle", "",
		time.Now().Unix(), time.Now().Unix(),
		"", 0, "", "", "",
		"{}",
	)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}

	insts, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(insts))
	}

	got := insts[0].GroupPath
	if got == "tmp" {
		t.Fatalf("GroupPath was silently re-derived from ProjectPath "+
			"(%q → %q). This is the #666 bug: sessions disappear from "+
			"their assigned group and show up under a path-derived name.",
			insts[0].ProjectPath, got)
	}
	if got != DefaultGroupPath {
		t.Fatalf("GroupPath = %q, want %q (the safe fallback); "+
			"anything else is silent re-parenting", got, DefaultGroupPath)
	}
}

// TestIssue666_LoadRowWithExplicitGroupPath_IsPreserved guards against
// the fix over-reaching: a session with a real, explicit group_path must
// not be touched by the fallback.
func TestIssue666_LoadRowWithExplicitGroupPath_IsPreserved(t *testing.T) {
	s := newTestStorage(t)

	_, err := s.db.DB().Exec(`
		INSERT INTO instances (
			id, title, project_path, group_path, sort_order,
			command, wrapper, tool, status, tmux_session,
			created_at, last_accessed,
			parent_session_id, is_conductor, worktree_path, worktree_repo, worktree_branch,
			tool_data
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		"issue666-keeps-group", "Real Group Session", "/tmp/proj", "agent-deck", 0,
		"", "", "shell", "idle", "",
		time.Now().Unix(), time.Now().Unix(),
		"", 0, "", "", "",
		"{}",
	)
	if err != nil {
		t.Fatalf("seed row: %v", err)
	}

	insts, _, err := s.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(insts) != 1 {
		t.Fatalf("expected 1 instance, got %d", len(insts))
	}
	if got := insts[0].GroupPath; got != "agent-deck" {
		t.Fatalf("explicit GroupPath must be preserved; got %q, want %q", got, "agent-deck")
	}
}

// TestIssue666_SaveWithGroups_NormalizesEmptyGroupPath covers the save-side
// guard. If an Instance slips through with GroupPath = "" (future regression
// in a fork path, direct field mutation, etc.), SaveWithGroups must normalize
// to DefaultGroupPath so the next load is clean.
func TestIssue666_SaveWithGroups_NormalizesEmptyGroupPath(t *testing.T) {
	s := newTestStorage(t)

	instances := []*Instance{
		{
			ID:          "issue666-save-empty",
			Title:       "Save With Empty Group",
			ProjectPath: "/tmp/project-x",
			GroupPath:   "", // <-- the hazard we're guarding against
			Tool:        "shell",
			Status:      StatusIdle,
			CreatedAt:   time.Now(),
		},
	}

	if err := s.SaveWithGroups(instances, nil); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}

	// Verify by reading raw column — no load-time defense may mask it.
	var stored string
	row := s.db.DB().QueryRow(
		"SELECT group_path FROM instances WHERE id = ?",
		"issue666-save-empty",
	)
	if err := row.Scan(&stored); err != nil {
		t.Fatalf("read back group_path: %v", err)
	}
	if stored == "" {
		t.Fatalf("SaveWithGroups persisted empty group_path; the guard " +
			"at the save boundary must normalize to DefaultGroupPath")
	}
	if stored != DefaultGroupPath {
		t.Fatalf("SaveWithGroups normalized empty group_path to %q, want %q",
			stored, DefaultGroupPath)
	}
}
