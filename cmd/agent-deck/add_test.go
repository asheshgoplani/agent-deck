package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func TestApplyCLIYoloOverride(t *testing.T) {
	t.Run("enabled for gemini sets override", func(t *testing.T) {
		inst := session.NewInstanceWithTool("gemini-test", "/tmp/test", "gemini")
		if err := applyCLIYoloOverride(inst, true); err != nil {
			t.Fatalf("applyCLIYoloOverride() error = %v", err)
		}
		if inst.GeminiYoloMode == nil || !*inst.GeminiYoloMode {
			t.Fatalf("GeminiYoloMode = %v, want true override", inst.GeminiYoloMode)
		}
	})

	t.Run("enabled for codex sets override", func(t *testing.T) {
		inst := session.NewInstanceWithTool("codex-test", "/tmp/test", "codex")
		if err := applyCLIYoloOverride(inst, true); err != nil {
			t.Fatalf("applyCLIYoloOverride() error = %v", err)
		}
		opts := inst.GetCodexOptions()
		if opts == nil || opts.YoloMode == nil || !*opts.YoloMode {
			t.Fatalf("CodexOptions.YoloMode = %v, want true override", opts)
		}
	})

	t.Run("disabled is a no-op", func(t *testing.T) {
		inst := session.NewInstanceWithTool("gemini-test", "/tmp/test", "gemini")
		if err := applyCLIYoloOverride(inst, false); err != nil {
			t.Fatalf("applyCLIYoloOverride() error = %v", err)
		}
		if inst.GeminiYoloMode != nil {
			t.Fatalf("GeminiYoloMode = %v, want nil when flag is not set", inst.GeminiYoloMode)
		}
	})

	t.Run("non-gemini-codex returns error", func(t *testing.T) {
		inst := session.NewInstanceWithTool("claude-test", "/tmp/test", "claude")
		if err := applyCLIYoloOverride(inst, true); err == nil {
			t.Fatal("applyCLIYoloOverride() error = nil, want non-nil")
		}
	})
}

// TestMain is in testmain_test.go - sets AGENTDECK_PROFILE=_test

// =============================================================================
// Tests for isDuplicateSession
// =============================================================================

// isDuplicateSession checks if a session with the same title AND path already exists.
// Returns (isDuplicate bool, existingInstance *session.Instance)
//
// Key behavior:
// - Same path + different title = NOT a duplicate (allow multiple sessions per project)
// - Same path + same title = IS a duplicate (prevent exact duplicates)

func TestIsDuplicateSession_SamePath_DifferentTitle_NotDuplicate(t *testing.T) {
	// Setup: existing session at /home/user/project with title "API Work"
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "API Work",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: adding new session at same path with DIFFERENT title "Frontend Work"
	isDup, existing := isDuplicateSession(instances, "Frontend Work", "/home/user/project")

	// Expect: NOT a duplicate - different titles should be allowed at same path
	if isDup {
		t.Errorf("Expected isDuplicateSession to return false for different title at same path, got true")
	}
	if existing != nil {
		t.Errorf("Expected existing instance to be nil for non-duplicate, got %v", existing)
	}
}

func TestIsDuplicateSession_SamePath_SameTitle_IsDuplicate(t *testing.T) {
	// Setup: existing session at /home/user/project with title "API Work"
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "API Work",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: adding new session at same path with SAME title "API Work"
	isDup, existing := isDuplicateSession(instances, "API Work", "/home/user/project")

	// Expect: IS a duplicate - exact same title and path
	if !isDup {
		t.Errorf("Expected isDuplicateSession to return true for same title at same path, got false")
	}
	if existing == nil {
		t.Errorf("Expected existing instance to be returned, got nil")
	}
	if existing != nil && existing.ID != "test-123" {
		t.Errorf("Expected existing instance ID to be 'test-123', got '%s'", existing.ID)
	}
}

func TestIsDuplicateSession_DifferentPath_SameTitle_NotDuplicate(t *testing.T) {
	// Setup: existing session at /home/user/project-a with title "My Work"
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My Work",
			ProjectPath: "/home/user/project-a",
		},
	}

	// Test: adding new session at DIFFERENT path with same title
	isDup, existing := isDuplicateSession(instances, "My Work", "/home/user/project-b")

	// Expect: NOT a duplicate - different paths, even if same title
	if isDup {
		t.Errorf("Expected isDuplicateSession to return false for same title at different path, got true")
	}
	if existing != nil {
		t.Errorf("Expected existing instance to be nil for non-duplicate, got %v", existing)
	}
}

func TestIsDuplicateSession_EmptyInstances(t *testing.T) {
	// Setup: no existing sessions
	instances := []*session.Instance{}

	// Test: adding first session
	isDup, existing := isDuplicateSession(instances, "New Project", "/home/user/project")

	// Expect: NOT a duplicate - no existing sessions
	if isDup {
		t.Errorf("Expected isDuplicateSession to return false for empty instances, got true")
	}
	if existing != nil {
		t.Errorf("Expected existing instance to be nil, got %v", existing)
	}
}

func TestIsDuplicateSession_CaseInsensitiveTitle(t *testing.T) {
	// Setup: existing session with title "api work" (lowercase)
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "api work",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: adding session with "API Work" (different case) at same path
	// This tests whether title comparison is case-sensitive or not
	// The expected behavior depends on implementation - adjust if needed
	isDup, _ := isDuplicateSession(instances, "API Work", "/home/user/project")

	// Expect: This may or may not be a duplicate depending on implementation
	// If case-insensitive: isDup = true
	// If case-sensitive: isDup = false
	// For now, we test case-SENSITIVE (different titles)
	if isDup {
		t.Logf("Note: Title comparison is case-insensitive (API Work == api work)")
	} else {
		t.Logf("Note: Title comparison is case-sensitive (API Work != api work)")
	}
	// This test documents the behavior, adjust assertion based on desired behavior
}

func TestIsDuplicateSession_PathNormalization(t *testing.T) {
	// Setup: existing session with path "/home/user/project/"
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My Project",
			ProjectPath: "/home/user/project/",
		},
	}

	// Test: adding session with same path but without trailing slash
	isDup, existing := isDuplicateSession(instances, "My Project", "/home/user/project")

	// Expect: IS a duplicate - paths should be normalized
	if !isDup {
		t.Errorf("Expected isDuplicateSession to normalize paths (trailing slash), got false")
	}
	if existing == nil {
		t.Errorf("Expected existing instance to be returned for normalized path match")
	}
}

func TestIsDuplicateSession_MultipleExistingSessions(t *testing.T) {
	// Setup: multiple sessions, some at same path with different titles
	instances := []*session.Instance{
		{
			ID:          "session-1",
			Title:       "Frontend",
			ProjectPath: "/home/user/project",
		},
		{
			ID:          "session-2",
			Title:       "Backend",
			ProjectPath: "/home/user/project",
		},
		{
			ID:          "session-3",
			Title:       "API",
			ProjectPath: "/home/user/other-project",
		},
	}

	// Test 1: Adding "Backend" at same path - should be duplicate
	isDup, existing := isDuplicateSession(instances, "Backend", "/home/user/project")
	if !isDup {
		t.Errorf("Expected duplicate for 'Backend' at /home/user/project")
	}
	if existing == nil || existing.ID != "session-2" {
		t.Errorf("Expected existing instance to be session-2")
	}

	// Test 2: Adding "Testing" at same path - should NOT be duplicate
	isDup, existing = isDuplicateSession(instances, "Testing", "/home/user/project")
	if isDup {
		t.Errorf("Expected non-duplicate for 'Testing' at /home/user/project")
	}
	if existing != nil {
		t.Errorf("Expected nil existing instance for non-duplicate")
	}
}

func TestIsWorktreeAlreadyExistsError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "git path exists error",
			err:  errors.New("failed to create worktree: fatal: '/tmp/repo-feature' already exists"),
			want: true,
		},
		{
			name: "case insensitive match",
			err:  errors.New("FAILED: ALREADY EXISTS"),
			want: true,
		},
		{
			name: "different git failure",
			err:  errors.New("failed to create worktree: fatal: invalid reference: bad-branch"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isWorktreeAlreadyExistsError(tt.err)
			if got != tt.want {
				t.Errorf("isWorktreeAlreadyExistsError() = %v, want %v", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Tests for generateUniqueTitle
// =============================================================================

// generateUniqueTitle generates a unique title for a session at a given path.
// If the baseTitle doesn't conflict with any existing session at the same path,
// it returns baseTitle unchanged.
// If there's a conflict, it appends "(2)", "(3)", etc.
//
// Key behavior:
// - No conflict at same path = returns baseTitle
// - One conflict at same path = returns "baseTitle (2)"
// - Multiple conflicts = returns next available number
// - Same title at DIFFERENT path = NOT a conflict (returns baseTitle)

func TestGenerateUniqueTitle_NoConflict(t *testing.T) {
	// Setup: no sessions with this title at this path
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "Other Project",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for "My Project" at same path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project")

	// Expect: baseTitle unchanged
	if title != "My Project" {
		t.Errorf("Expected 'My Project', got '%s'", title)
	}
}

func TestGenerateUniqueTitle_OneConflict(t *testing.T) {
	// Setup: existing "My Project" at same path
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My Project",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for "My Project" at same path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project")

	// Expect: "My Project (2)"
	if title != "My Project (2)" {
		t.Errorf("Expected 'My Project (2)', got '%s'", title)
	}
}

func TestGenerateUniqueTitle_MultipleConflicts(t *testing.T) {
	// Setup: existing "My Project", "My Project (2)", "My Project (3)" at same path
	instances := []*session.Instance{
		{
			ID:          "session-1",
			Title:       "My Project",
			ProjectPath: "/home/user/project",
		},
		{
			ID:          "session-2",
			Title:       "My Project (2)",
			ProjectPath: "/home/user/project",
		},
		{
			ID:          "session-3",
			Title:       "My Project (3)",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for "My Project" at same path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project")

	// Expect: "My Project (4)"
	if title != "My Project (4)" {
		t.Errorf("Expected 'My Project (4)', got '%s'", title)
	}
}

func TestGenerateUniqueTitle_GapInNumbers(t *testing.T) {
	// Setup: existing "My Project", "My Project (3)" - note: (2) is missing
	instances := []*session.Instance{
		{
			ID:          "session-1",
			Title:       "My Project",
			ProjectPath: "/home/user/project",
		},
		{
			ID:          "session-3",
			Title:       "My Project (3)",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for "My Project" at same path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project")

	// Expect: "My Project (2)" - fills the gap
	if title != "My Project (2)" {
		t.Errorf("Expected 'My Project (2)' (fill gap), got '%s'", title)
	}
}

func TestGenerateUniqueTitle_SameTitleDifferentPath_NoConflict(t *testing.T) {
	// Setup: existing "My Project" at /home/user/project-a
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My Project",
			ProjectPath: "/home/user/project-a",
		},
	}

	// Test: generate title for "My Project" at DIFFERENT path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project-b")

	// Expect: baseTitle unchanged - different paths don't conflict
	if title != "My Project" {
		t.Errorf("Expected 'My Project' (different path = no conflict), got '%s'", title)
	}
}

func TestGenerateUniqueTitle_EmptyInstances(t *testing.T) {
	// Setup: no existing sessions
	instances := []*session.Instance{}

	// Test: generate title
	title := generateUniqueTitle(instances, "New Project", "/home/user/project")

	// Expect: baseTitle unchanged
	if title != "New Project" {
		t.Errorf("Expected 'New Project', got '%s'", title)
	}
}

func TestGenerateUniqueTitle_EmptyBaseTitle(t *testing.T) {
	// Setup: no existing sessions
	instances := []*session.Instance{}

	// Test: generate title with empty base
	title := generateUniqueTitle(instances, "", "/home/user/project")

	// Expect: empty string (or implementation may provide a default)
	// This documents edge case behavior
	if title != "" {
		t.Logf("Note: Empty base title generates '%s'", title)
	}
}

func TestGenerateUniqueTitle_SpecialCharactersInTitle(t *testing.T) {
	// Setup: existing session with special characters
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My (Project) #1",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for same special title at same path
	title := generateUniqueTitle(instances, "My (Project) #1", "/home/user/project")

	// Expect: "My (Project) #1 (2)" - handles special chars correctly
	if title != "My (Project) #1 (2)" {
		t.Errorf("Expected 'My (Project) #1 (2)', got '%s'", title)
	}
}

func TestGenerateUniqueTitle_TitleWithExistingNumber(t *testing.T) {
	// Setup: existing "My Project (2)" but NOT "My Project"
	instances := []*session.Instance{
		{
			ID:          "test-123",
			Title:       "My Project (2)",
			ProjectPath: "/home/user/project",
		},
	}

	// Test: generate title for "My Project" at same path
	title := generateUniqueTitle(instances, "My Project", "/home/user/project")

	// Expect: "My Project" unchanged - base title doesn't exist
	if title != "My Project" {
		t.Errorf("Expected 'My Project' (base doesn't exist), got '%s'", title)
	}
}

func TestGenerateUniqueTitle_MixedPathsMultipleTitles(t *testing.T) {
	// Setup: complex scenario with multiple paths and titles
	instances := []*session.Instance{
		{
			ID:          "session-1",
			Title:       "Work",
			ProjectPath: "/home/user/project-a",
		},
		{
			ID:          "session-2",
			Title:       "Work",
			ProjectPath: "/home/user/project-b",
		},
		{
			ID:          "session-3",
			Title:       "Work (2)",
			ProjectPath: "/home/user/project-a",
		},
	}

	// Test 1: Adding "Work" at project-a - should get (3) since (2) exists
	title := generateUniqueTitle(instances, "Work", "/home/user/project-a")
	if title != "Work (3)" {
		t.Errorf("Expected 'Work (3)' for project-a, got '%s'", title)
	}

	// Test 2: Adding "Work" at project-b - should get (2) since only base exists
	title = generateUniqueTitle(instances, "Work", "/home/user/project-b")
	if title != "Work (2)" {
		t.Errorf("Expected 'Work (2)' for project-b, got '%s'", title)
	}

	// Test 3: Adding "Work" at project-c - should stay unchanged
	title = generateUniqueTitle(instances, "Work", "/home/user/project-c")
	if title != "Work" {
		t.Errorf("Expected 'Work' for project-c (no conflict), got '%s'", title)
	}
}

func TestResolveGroupPathForAdd_ByExactPath(t *testing.T) {
	tree := session.NewGroupTree([]*session.Instance{
		{ID: "1", GroupPath: "platform/backend"},
	})

	got := resolveGroupPathForAdd(tree, "platform/backend")
	if got != "platform/backend" {
		t.Fatalf("Expected exact group path match, got %q", got)
	}
}

func TestResolveGroupPathForAdd_ByNameAndNormalizedPath(t *testing.T) {
	stored := []*session.GroupData{
		{Name: "My Team", Path: "my-team", Expanded: true, Order: 0},
	}
	tree := session.NewGroupTreeWithGroups(nil, stored)

	if got := resolveGroupPathForAdd(tree, "My Team"); got != "my-team" {
		t.Fatalf("Expected name selector to resolve to my-team, got %q", got)
	}

	if got := resolveGroupPathForAdd(tree, "my team"); got != "my-team" {
		t.Fatalf("Expected normalized selector to resolve to my-team, got %q", got)
	}
}

// TestResolveAddPath covers the path-arg resolver used by `agent-deck add`.
// Regression: prior to the fix, `~` was passed through filepath.Abs and resolved
// to <cwd>/~, breaking remote SSH-driven adds where the shell preserves a
// literal tilde. ExpandPath handles ~, ~/foo, $HOME, and ${VAR}.
func TestResolveAddPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"dot uses cwd", ".", cwd},
		{"bare tilde expands to home", "~", home},
		{"tilde with subdir", "~/projects/foo", filepath.Join(home, "projects/foo")},
		{"HOME env var", "$HOME", home},
		{"HOME env var with subdir", "$HOME/bar", filepath.Join(home, "bar")},
		{"absolute path passes through", "/tmp/abs", "/tmp/abs"},
		{"relative resolves against cwd", "rel/sub", filepath.Join(cwd, "rel/sub")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveAddPath(tt.in)
			if err != nil {
				t.Fatalf("resolveAddPath(%q) error = %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("resolveAddPath(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestHandleAddUsesGlobalDefaultPath(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	defaultPath := filepath.Join(home, "workspace")
	if err := os.MkdirAll(defaultPath, 0o755); err != nil {
		t.Fatalf("mkdir default path: %v", err)
	}
	writeAddUserConfig(t, home, `default_path = "`+defaultPath+`"`+"\n")

	handleAdd(profile, []string{"--title", "global-default", "--quiet"})

	if got := onlyAddedSessionPath(t, profile); got != defaultPath {
		t.Fatalf("added path = %q, want global default_path %q", got, defaultPath)
	}
}

func TestHandleAddExplicitPathIgnoresGlobalDefaultPath(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	defaultPath := filepath.Join(home, "workspace")
	explicitPath := filepath.Join(home, "explicit")
	for _, dir := range []string{defaultPath, explicitPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+defaultPath+`"`+"\n")

	handleAdd(profile, []string{"--title", "explicit", "--quiet", explicitPath})

	if got := onlyAddedSessionPath(t, profile); got != explicitPath {
		t.Fatalf("added path = %q, want explicit path %q", got, explicitPath)
	}
}

func TestHandleAddGroupDefaultPathPrecedesGlobalDefaultPath(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	globalPath := filepath.Join(home, "global")
	groupPath := filepath.Join(home, "group")
	for _, dir := range []string{globalPath, groupPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalPath+`"`+"\n")

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	groupTree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{
		{Name: "Work", Path: "work", Expanded: true, DefaultPath: groupPath},
	})
	if err := storage.SaveWithGroups(nil, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}

	handleAdd(profile, []string{"--group", "work", "--title", "group-default", "--quiet"})

	if got := onlyAddedSessionPath(t, profile); got != groupPath {
		t.Fatalf("added path = %q, want group default_path %q", got, groupPath)
	}
}

func TestHandleAddFallsBackToCwdWithoutGlobalDefaultPath(t *testing.T) {
	_, cwd, profile := setupAddDefaultPathTest(t)

	handleAdd(profile, []string{"--title", "cwd-default", "--quiet"})

	if got := onlyAddedSessionPath(t, profile); got != cwd {
		t.Fatalf("added path = %q, want cwd %q", got, cwd)
	}
}

func TestHandleAddExpandsGlobalDefaultPath(t *testing.T) {
	tests := []struct {
		name        string
		configValue func(t *testing.T, home string) (string, string)
	}{
		{
			name: "tilde",
			configValue: func(t *testing.T, home string) (string, string) {
				want := filepath.Join(home, "workspace")
				return "~/workspace", want
			},
		},
		{
			name: "env var",
			configValue: func(t *testing.T, home string) (string, string) {
				root := filepath.Join(home, "env-root")
				t.Setenv("AGENT_DECK_TEST_ROOT", root)
				want := filepath.Join(root, "workspace")
				return "$AGENT_DECK_TEST_ROOT/workspace", want
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, _, profile := setupAddDefaultPathTest(t)
			configPath, want := tt.configValue(t, home)
			if err := os.MkdirAll(want, 0o755); err != nil {
				t.Fatalf("mkdir default path: %v", err)
			}
			writeAddUserConfig(t, home, `default_path = "`+configPath+`"`+"\n")

			handleAdd(profile, []string{"--title", tt.name, "--quiet"})

			if got := onlyAddedSessionPath(t, profile); got != want {
				t.Fatalf("added path = %q, want expanded default_path %q", got, want)
			}
		})
	}
}

func TestHandleAddFallsBackToCwdWhenGlobalDefaultPathMissing(t *testing.T) {
	home, cwd, profile := setupAddDefaultPathTest(t)
	missingPath := filepath.Join(home, "missing")
	writeAddUserConfig(t, home, `default_path = "`+missingPath+`"`+"\n")

	handleAdd(profile, []string{"--title", "missing-default", "--quiet"})

	if got := onlyAddedSessionPath(t, profile); got != cwd {
		t.Fatalf("added path = %q, want cwd %q", got, cwd)
	}
}

func setupAddDefaultPathTest(t *testing.T) (home, cwd, profile string) {
	t.Helper()

	home = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	cwd = filepath.Join(home, "cwd")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatalf("mkdir cwd: %v", err)
	}
	t.Chdir(cwd)

	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	return home, cwd, "add_default_path"
}

func writeAddUserConfig(t *testing.T, home, content string) {
	t.Helper()

	configDir := filepath.Join(home, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir config dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	session.ClearUserConfigCache()
}

func onlyAddedSessionPath(t *testing.T, profile string) string {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	defer storage.Close()
	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	if len(instances) != 1 {
		t.Fatalf("loaded %d sessions, want 1", len(instances))
	}
	return instances[0].ProjectPath
}
