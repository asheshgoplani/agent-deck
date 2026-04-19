package integration

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/asheshgoplani/agent-deck/internal/statedb"
	"github.com/asheshgoplani/agent-deck/internal/ui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
)

// setupSkillTestEnv replicates the HOME/CLAUDE_CONFIG_DIR override pattern
// from session/skills_catalog_test.go. The original helper is unexported,
// so integration tests must maintain their own copy.
func setupSkillTestEnv(t *testing.T) (string, func()) {
	t.Helper()

	homeDir, err := os.MkdirTemp("", "agentdeck-skills-home-*")
	require.NoError(t, err, "failed to create temp home")

	claudeDir := filepath.Join(homeDir, ".claude")
	require.NoError(t, os.MkdirAll(claudeDir, 0o755), "failed to create claude dir")

	oldHome := os.Getenv("HOME")
	oldClaude := os.Getenv("CLAUDE_CONFIG_DIR")
	require.NoError(t, os.Setenv("HOME", homeDir))
	require.NoError(t, os.Setenv("CLAUDE_CONFIG_DIR", claudeDir))
	session.ClearUserConfigCache()

	cleanup := func() {
		_ = os.Setenv("HOME", oldHome)
		_ = os.Setenv("CLAUDE_CONFIG_DIR", oldClaude)
		session.ClearUserConfigCache()
		_ = os.RemoveAll(homeDir)
	}

	return homeDir, cleanup
}

func boolPtr(v bool) *bool {
	return &v
}

// TestEdge_SkillsDiscoverAttach is an end-to-end integration test for the
// skills pipeline: register a source, discover a skill, attach it to a project,
// and verify the materialized SKILL.md is readable. (EDGE-01)
func TestEdge_SkillsDiscoverAttach(t *testing.T) {
	_, cleanup := setupSkillTestEnv(t)
	defer cleanup()

	// Create a skill source directory with one directory skill.
	sourcePath := t.TempDir()
	skillDir := filepath.Join(sourcePath, "my-inttest-skill")
	require.NoError(t, os.MkdirAll(skillDir, 0o755))
	skillContent := "---\nname: my-inttest-skill\ndescription: Integration test skill\n---\n\n# my-inttest-skill\n"
	require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillContent), 0o644))

	// Register custom source via SaveSkillSources.
	err := session.SaveSkillSources(map[string]session.SkillSourceDef{
		"inttest-source": {Path: sourcePath, Enabled: boolPtr(true)},
	})
	require.NoError(t, err, "SaveSkillSources should succeed")

	// Discover skills from all enabled sources.
	skills, err := session.ListAvailableSkills()
	require.NoError(t, err, "ListAvailableSkills should succeed")
	require.NotEmpty(t, skills, "should discover at least one skill")

	found := false
	for _, s := range skills {
		if s.Name == "my-inttest-skill" {
			found = true
			assert.Equal(t, "inttest-source", s.Source, "skill should come from inttest-source")
			assert.Equal(t, "dir", s.Kind, "skill should be a directory skill")
			break
		}
	}
	require.True(t, found, "my-inttest-skill should be discovered")

	// Attach the skill to a fresh project directory.
	projectPath := t.TempDir()
	attachment, err := session.AttachSkillToProject(projectPath, "claude", "my-inttest-skill", "inttest-source")
	require.NoError(t, err, "AttachSkillToProject should succeed")
	require.NotNil(t, attachment, "attachment should not be nil")

	assert.Equal(t, "my-inttest-skill", attachment.Name)
	assert.Equal(t, "inttest-source", attachment.Source)

	// Verify the materialized SKILL.md is readable at the target path.
	targetDir := filepath.Join(projectPath, attachment.TargetPath)
	content, err := os.ReadFile(filepath.Join(targetDir, "SKILL.md"))
	require.NoError(t, err, "materialized SKILL.md should be readable")
	assert.True(t, strings.Contains(string(content), "my-inttest-skill"),
		"materialized SKILL.md should contain the skill name")
}

// TestEdge_ConcurrentPolling creates 12 real tmux sessions and concurrently
// polls UpdateStatus on all of them with the -race detector. This proves that
// the Instance mutex works correctly under concurrent access. (EDGE-02)
func TestEdge_ConcurrentPolling(t *testing.T) {
	h := NewTmuxHarness(t)

	const sessionCount = 12
	instances := make([]*session.Instance, 0, sessionCount)

	// Create and start 12 real tmux sessions.
	for i := 0; i < sessionCount; i++ {
		inst := h.CreateSession(fmt.Sprintf("poll-%02d", i), "/tmp")
		inst.Command = "sleep 60"
		require.NoError(t, inst.Start(), "session %d Start() should succeed", i)
		instances = append(instances, inst)
	}

	// Wait for all sessions to exist in tmux.
	for i, inst := range instances {
		WaitForCondition(t, 5*time.Second, 200*time.Millisecond,
			fmt.Sprintf("session %d to exist", i),
			func() bool { return inst.Exists() })
	}

	// Grace period: status detection has a 1.5s startup window where it
	// always returns "starting" for sessions that just appeared.
	time.Sleep(2 * time.Second)

	// Launch concurrent goroutines, each calling UpdateStatus 5 times.
	g, _ := errgroup.WithContext(context.Background())
	for _, inst := range instances {
		inst := inst // capture loop variable
		g.Go(func() error {
			for j := 0; j < 5; j++ {
				if err := inst.UpdateStatus(); err != nil {
					return err
				}
				time.Sleep(100 * time.Millisecond)
			}
			return nil
		})
	}
	require.NoError(t, g.Wait(), "concurrent UpdateStatus calls should not error")

	// Verify each session has a non-error status.
	for i, inst := range instances {
		s := inst.GetStatusThreadSafe()
		assert.NotEqual(t, session.StatusError, s,
			"session %d should not be in error state, got %q", i, s)
	}
}

// TestEdge_StorageWatcherCrossInstance verifies that a StorageWatcher on one
// StateDB instance detects a Touch() from a SECOND StateDB instance sharing
// the same SQLite file. This simulates cross-process coordination. (EDGE-03)
//
// Distinct from existing storage_watcher_test.go tests which use a single
// StateDB instance for both watching and touching.
func TestEdge_StorageWatcherCrossInstance(t *testing.T) {
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "state.db")

	// Open two separate StateDB instances on the same file.
	dbA, err := statedb.Open(dbPath)
	require.NoError(t, err, "statedb.Open(A) should succeed")
	require.NoError(t, dbA.Migrate(), "dbA.Migrate should succeed")
	t.Cleanup(func() { dbA.Close() })

	dbB, err := statedb.Open(dbPath)
	require.NoError(t, err, "statedb.Open(B) should succeed")
	require.NoError(t, dbB.Migrate(), "dbB.Migrate should succeed")
	t.Cleanup(func() { dbB.Close() })

	// Create a StorageWatcher on dbA.
	watcher, err := ui.NewStorageWatcher(dbA)
	require.NoError(t, err, "NewStorageWatcher should succeed")
	require.NotNil(t, watcher, "watcher should not be nil")
	defer func() { _ = watcher.Close() }()
	watcher.Start()

	// Allow the watcher goroutine to begin its first poll cycle.
	time.Sleep(100 * time.Millisecond)

	// Touch from the second instance (simulates an external process writing).
	require.NoError(t, dbB.Touch(), "dbB.Touch() should succeed")

	// Wait for the watcher to detect the change.
	select {
	case <-watcher.ReloadChannel():
		// Success: cross-instance change detected.
	case <-time.After(5 * time.Second):
		t.Fatal("StorageWatcher should detect Touch() from second StateDB instance within 5s")
	}
}
