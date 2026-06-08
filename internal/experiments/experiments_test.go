package experiments

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/testutil"
)

func TestMain(m *testing.M) {
	os.Exit(runTestMain(m))
}

// runTestMain holds the real TestMain body so the deferred cleanup below
// actually runs: TestMain calls os.Exit, which does NOT run deferred functions,
// so registering the HOME-restore defer here and returning the exit code is the
// only way to guarantee the temp dir is removed (2026-06-07 pty-exhaustion
// incident class).
func runTestMain(m *testing.M) int {
	// Isolate HOME+XDG so agent-deck path resolution lands in a temp dir, never
	// the real ~/.agent-deck (2026-06-04 data-loss incident, S5).
	// See internal/testutil/homeenv.go for the postmortem.
	cleanupHome := testutil.IsolateHome()
	defer cleanupHome()

	os.Setenv("AGENTDECK_PROFILE", "_test")

	// Run tests
	code := m.Run()

	// Cleanup: Kill any orphaned test sessions after tests complete
	// This prevents RAM waste from lingering test sessions
	// See CLAUDE.md: "2026-01-20 Incident: 20+ Test-Skip-Regen sessions orphaned, wasting ~3GB RAM"
	cleanupTestSessions()

	return code
}

// cleanupTestSessions kills any tmux sessions created during testing.
// IMPORTANT: Only match specific known test artifacts, NOT broad patterns.
// Broad patterns like HasPrefix("agentdeck_test") or Contains("test_") kill
// real user sessions with "test" in their title. Each test already has
// defer Kill() which handles cleanup reliably (runs on panic, Fatal, etc).
func cleanupTestSessions() {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return
	}

	sessions := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, sess := range sessions {
		if strings.Contains(sess, "Test-Skip-Regen") {
			_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		}
	}
}

func TestListExperiments(t *testing.T) {
	tmpDir := t.TempDir()

	// Create some experiment folders
	folders := []string{
		"2025-01-15-redis-cache",
		"2025-01-16-api-test",
		"2025-01-18-database-migration",
		"not-dated-folder",
	}
	for _, f := range folders {
		if err := os.MkdirAll(filepath.Join(tmpDir, f), 0755); err != nil {
			t.Fatalf("failed to create folder %s: %v", f, err)
		}
	}

	experiments, err := ListExperiments(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	if len(experiments) != 4 {
		t.Errorf("expected 4 experiments, got %d", len(experiments))
	}
}

func TestFuzzyFind(t *testing.T) {
	experiments := []Experiment{
		{Name: "redis-cache", Path: "/tmp/2025-01-15-redis-cache"},
		{Name: "redis-server", Path: "/tmp/2025-01-16-redis-server"},
		{Name: "api-test", Path: "/tmp/2025-01-17-api-test"},
	}

	matches := FuzzyFind(experiments, "redis")
	if len(matches) != 2 {
		t.Errorf("expected 2 matches for 'redis', got %d", len(matches))
	}

	matches = FuzzyFind(experiments, "rds")
	if len(matches) < 1 {
		t.Error("expected at least 1 fuzzy match for 'rds'")
	}
}

func TestCreateExperiment(t *testing.T) {
	tmpDir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	exp, err := CreateExperiment(tmpDir, "my-project", true)
	if err != nil {
		t.Fatal(err)
	}

	expectedName := today + "-my-project"
	if !strings.HasSuffix(exp.Path, expectedName) {
		t.Errorf("expected path ending with %q, got %q", expectedName, exp.Path)
	}

	// Verify directory was created
	if _, err := os.Stat(exp.Path); os.IsNotExist(err) {
		t.Error("experiment directory was not created")
	}
}

func TestCreateExperiment_NoDuplicates(t *testing.T) {
	tmpDir := t.TempDir()
	today := time.Now().Format("2006-01-02")

	// Create first experiment
	exp1, _ := CreateExperiment(tmpDir, "my-project", true)

	// Create second with same name - should add suffix
	exp2, err := CreateExperiment(tmpDir, "my-project", true)
	if err != nil {
		t.Fatal(err)
	}

	if exp1.Path == exp2.Path {
		t.Error("expected different paths for duplicate names")
	}

	expectedSuffix := today + "-my-project-2"
	if !strings.HasSuffix(exp2.Path, expectedSuffix) {
		t.Errorf("expected path ending with %q, got %q", expectedSuffix, exp2.Path)
	}
}
