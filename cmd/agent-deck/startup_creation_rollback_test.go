package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestStartupCreationRollbackPostInsertFailures(t *testing.T) {
	for _, stage := range []string{"configure MCPs", "start session"} {
		t.Run(stage, func(t *testing.T) {
			storage, err := session.NewStorageWithProfile("query-rollback-" + strings.ReplaceAll(stage, " ", "-"))
			if err != nil {
				t.Fatal(err)
			}
			defer storage.Close()
			original := creationRepo(t)
			sentinel := filepath.Join(original, "keep.txt")
			if err := os.WriteFile(sentinel, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			inst := session.NewInstance("rollback", original)
			owned := filepath.Join(t.TempDir(), inst.ID)
			if err := os.Mkdir(owned, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(original, filepath.Join(owned, "project")); err != nil {
				t.Fatal(err)
			}
			worktree := filepath.Join(owned, "isolated")
			creationGit(t, original, "worktree", "add", "-b", "rollback-branch", worktree)
			inst.MultiRepoWorktrees = []session.MultiRepoWorktree{{OriginalPath: original, RepoRoot: original, WorktreePath: worktree, Branch: "rollback-branch"}}
			inst.MultiRepoTempDir = owned
			inst.MultiRepoEnabled = true
			if err := storage.InsertSessionAndVerify(inst, nil); err != nil {
				t.Fatal(err)
			}
			stopped := false
			rollback := &startupCreationRollback{id: inst.ID, removeRow: func() error { return storage.RemoveSessionAndVerify(inst.ID, nil, nil) }, stop: func() error { stopped = true; return nil }, cleanup: func() error { return cleanupOwnedCreationArtifacts(inst) }}
			err = rollback.run(stage, func() error { return errors.New("injected failure") })
			if err == nil || !strings.Contains(err.Error(), "injected failure") {
				t.Fatalf("lost stage error: %v", err)
			}
			exists, err := storage.InstanceExists(inst.ID)
			if err != nil || exists {
				t.Fatalf("failed creation row remains: %v %v", exists, err)
			}
			if _, err := os.Stat(owned); !os.IsNotExist(err) {
				t.Fatalf("owned artifacts remain: %v", err)
			}
			if registered := creationGit(t, original, "worktree", "list", "--porcelain"); strings.Contains(registered, worktree) {
				t.Fatalf("worktree still registered: %s", registered)
			}
			if content, err := os.ReadFile(sentinel); err != nil || string(content) != "original" {
				t.Fatalf("original harmed: %s %v", content, err)
			}
			if stopped != (stage == "start session") {
				t.Fatalf("stop=%v for %s", stopped, stage)
			}
		})
	}
}

func TestStartupCreationRollbackReportsRecoveryIdentity(t *testing.T) {
	removed, cleaned := false, false
	rollback := &startupCreationRollback{id: "new-session-id", removeRow: func() error { removed = true; return nil }, stop: func() error { return errors.New("cannot stop") }, cleanup: func() error { cleaned = true; return nil }}
	err := rollback.run("start session", func() error { return errors.New("start failed") })
	if err == nil || !strings.Contains(err.Error(), "new-session-id") || !strings.Contains(err.Error(), "cannot stop") {
		t.Fatalf("missing recovery detail: %v", err)
	}
	if removed || cleaned {
		t.Fatal("removed registry/artifacts despite unconfirmed stop")
	}
}

func TestStartupCreationRollbackCLIRespectsWorktreeOwnership(t *testing.T) {
	for _, reuse := range []bool{false, true} {
		t.Run(fmt.Sprintf("reuse=%v", reuse), func(t *testing.T) {
			home := t.TempDir()
			configDir := filepath.Join(home, ".agent-deck")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("[worktree]\nbranch_prefix = \"\"\n[mcps.fixture]\ncommand = \"echo\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			repo := creationRepo(t)
			blocker := filepath.Join(repo, ".mcp.json")
			if err := os.Mkdir(blocker, 0o700); err != nil {
				t.Fatal(err)
			}
			original := filepath.Join(blocker, "keep")
			if err := os.WriteFile(original, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			creationGit(t, repo, "add", ".mcp.json")
			creationGit(t, repo, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "-m", "MCP write blocker")
			existing := ""
			if reuse {
				existing = filepath.Join(t.TempDir(), "existing")
				creationGit(t, repo, "worktree", "add", "-b", "query-rollback", existing)
			}
			before := creationGit(t, repo, "worktree", "list", "--porcelain")
			stdout, stderr, code := runAgentDeck(t, home, "launch", repo, "-c", "claude", "--startup-query", "hello", "--mcp", "fixture", "--no-parent", "--no-wait", "--json", "--title", "failed-query-fixture", "--worktree", "query-rollback")
			if code == 0 || !strings.Contains(stdout+stderr, "failed to configure MCPs") || !strings.Contains(stdout+stderr, "rolled back") {
				t.Fatalf("wrong failure: code=%d stdout=%s stderr=%s", code, stdout, stderr)
			}
			if reuse && !strings.Contains(stderr, "Reusing existing worktree at "+existing) {
				t.Fatalf("fixture did not exercise reuse: %s", stderr)
			}
			listed, stderr, code := runAgentDeck(t, home, "list", "--json")
			if code != 0 || strings.Contains(listed, "failed-query-fixture") {
				t.Fatalf("failed creation retained row: %d %s %s", code, listed, stderr)
			}
			after := creationGit(t, repo, "worktree", "list", "--porcelain")
			if before != after {
				t.Fatalf("worktree registration changed across failed creation:\nbefore=%s\nafter=%s", before, after)
			}
			if content, err := os.ReadFile(original); err != nil || string(content) != "original" {
				t.Fatalf("original damaged: %s %v", content, err)
			}
			if reuse {
				if _, err := os.Stat(filepath.Join(existing, ".mcp.json", "keep")); err != nil {
					t.Fatalf("reused worktree removed: %v", err)
				}
			}
		})
	}
}
