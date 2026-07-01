package session

import (
	"reflect"
	"testing"
	"time"
)

// TestForkInheritsEnv verifies a forked claude session carries the parent's
// per-session env (Feature B fork-inheritance half of the launch-overrides work).
func TestForkInheritsEnv(t *testing.T) {
	parent := NewInstanceWithTool("parent", t.TempDir(), "claude")
	parent.ClaudeSessionID = "00000000-0000-0000-0000-000000000001"
	parent.ClaudeDetectedAt = time.Now() // satisfy CanFork freshness
	parent.Env = []string{"FOO=bar", "BAZ=qux"}

	forked, _, err := parent.CreateForkedInstanceWithOptions("fork", "", nil)
	if err != nil {
		t.Fatalf("fork: %v", err)
	}
	if !reflect.DeepEqual(forked.Env, []string{"FOO=bar", "BAZ=qux"}) {
		t.Fatalf("fork did not inherit env: got %v", forked.Env)
	}
	// Copied, not aliased: mutating the parent must not affect the fork.
	parent.Env[0] = "FOO=changed"
	if forked.Env[0] != "FOO=bar" {
		t.Fatalf("fork env aliased parent slice: %v", forked.Env)
	}
}

// TestForkInheritsEnv_NonClaudeTools closes the coverage gap flagged in review:
// the OpenCode/Pi/Codex fork constructors must also inherit per-session env
// (copied, not aliased), not just claude.
func TestForkInheritsEnv_NonClaudeTools(t *testing.T) {
	want := []string{"FOO=bar", "BAZ=qux"}
	assertInherited := func(t *testing.T, parent, forked *Instance) {
		t.Helper()
		if !reflect.DeepEqual(forked.Env, want) {
			t.Fatalf("fork did not inherit env: got %v want %v", forked.Env, want)
		}
		parent.Env[0] = "FOO=changed"
		if forked.Env[0] != "FOO=bar" {
			t.Fatalf("fork env aliased parent slice: %v", forked.Env)
		}
	}

	t.Run("opencode", func(t *testing.T) {
		parent := NewInstanceWithTool("parent", t.TempDir(), "opencode")
		parent.OpenCodeSessionID = "ses_parent_123"
		parent.OpenCodeDetectedAt = time.Now()
		parent.Env = append([]string(nil), want...)
		forked, _, err := parent.CreateForkedOpenCodeInstanceWithOptions("fork", "", nil)
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		assertInherited(t, parent, forked)
	})

	t.Run("pi", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		parent := NewInstanceWithTool("parent", "/tmp/project", "pi")
		parent.ID = "parent-pi-id"
		parent.Command = "pi"
		parent.Env = append([]string(nil), want...)
		seedLocalPiSessionFile(t, parent)
		forked, _, err := parent.CreateForkedPiInstance("fork", "")
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		assertInherited(t, parent, forked)
	})

	t.Run("codex", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("CODEX_HOME", home)
		sid := "11111111-2222-3333-4444-555555555555"
		seedCodexRollout(t, home, sid)
		parent := NewInstanceWithTool("parent", "/tmp/project", "codex")
		parent.CodexSessionID = sid
		parent.CodexDetectedAt = time.Now()
		parent.Env = append([]string(nil), want...)
		forked, _, err := parent.CreateForkedCodexInstanceWithOptions("fork", "", nil)
		if err != nil {
			t.Fatalf("fork: %v", err)
		}
		assertInherited(t, parent, forked)
	})
}
