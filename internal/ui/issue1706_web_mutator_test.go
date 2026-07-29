package ui

import (
	"os"
	"strings"
	"testing"
)

// Related to #1706. The web mutator's create path takes project_path straight
// from an HTTP request, so it must go through the same resolution as every other
// local writer: a relative value would be resolved against the tmux server's cwd
// when the session starts, and against a different directory again for the
// Claude project slug.
//
// Structural pin, following TestWebMutatorForkSessionUsesSharedToolDispatcher:
// exercising CreateSession for real needs a live storage transaction, and the
// resolution behaviour itself is covered by internal/session
// (TestResolveProjectPath, TestSetField_PathIsStoredAbsolute).
func TestIssue1706_WebMutatorResolvesProjectPathOnCreate(t *testing.T) {
	src, err := os.ReadFile("web_mutator.go")
	if err != nil {
		t.Fatalf("read web_mutator.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "session.ResolveProjectPath(projectPath)") {
		t.Fatal("WebMutator.CreateSession must resolve projectPath via session.ResolveProjectPath (#1706)")
	}
	// SetField canonicalizes a path, so comparing the raw request value against
	// the old one reports another spelling of the stored path as a real change
	// (and as restart-required).
	if !strings.Contains(body, "newValue = inst.ProjectPath") {
		t.Fatal("WebMutator.UpdateSession must compare the stored path, not the raw request value (#1706)")
	}
}
