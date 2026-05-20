package jujutsu

import (
	"os/exec"
	"reflect"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/vcs"
)

// TestJujutsuDetect_NonJJDir verifies that IsJJRepo returns false for a
// directory that is not a jj repository. When jj is not installed at all
// this still returns false (LookPath miss), which matches the "git
// fallback" path in detectAndCreateBackend.
func TestJujutsuDetect_NonJJDir(t *testing.T) {
	tmp := t.TempDir()
	if IsJJRepo(tmp) {
		t.Fatalf("IsJJRepo returned true for non-jj dir %s", tmp)
	}
}

// TestJujutsuDetect_JJNotInstalled covers the missing-binary path. We can't
// guarantee jj is absent on every dev machine, so this test only asserts
// the contract: when LookPath fails, IsJJRepo returns false without panic.
func TestJujutsuDetect_JJNotInstalled(t *testing.T) {
	if _, err := exec.LookPath("jj"); err == nil {
		t.Skip("jj binary is available; skipping not-installed path")
	}
	if IsJJRepo(t.TempDir()) {
		t.Fatal("IsJJRepo should return false when jj is not installed")
	}
}

// TestJujutsuDetect_NewJJBackendOnNonRepo verifies the error path of
// NewJJBackend on a non-jj directory.
func TestJujutsuDetect_NewJJBackendOnNonRepo(t *testing.T) {
	tmp := t.TempDir()
	if _, err := NewJJBackend(tmp); err == nil {
		t.Fatalf("NewJJBackend(%s) should error for non-jj dir", tmp)
	}
}

// TestJujutsuParseWorkspacesList exercises the parser used by ListWorktrees
// without invoking the jj binary. Keeps coverage useful on machines without jj.
func TestJujutsuParseWorkspacesList(t *testing.T) {
	output := "default: abc123\nfeature-x: def456\n\nempty-line-ignored:\n"
	got, err := parseWorkspacesList(output)
	if err != nil {
		t.Fatalf("parseWorkspacesList errored: %v", err)
	}
	want := []vcs.Worktree{
		{Branch: "default", Commit: "abc123"},
		{Branch: "feature-x", Commit: "def456"},
		{Branch: "empty-line-ignored", Commit: ""},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseWorkspacesList mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

// TestJujutsuWorkspaceNameFromPath asserts path-to-name sanitization used
// by CreateWorktree/RemoveWorktree.
func TestJujutsuWorkspaceNameFromPath(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/tmp/agent-deck-feature-x", "agent-deck-feature-x"},
		{"/tmp/with space dir", "with-space-dir"},
		{"feature-y", "feature-y"},
	}
	for _, tc := range cases {
		got := workspaceNameFromPath(tc.path)
		if got != tc.want {
			t.Errorf("workspaceNameFromPath(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
