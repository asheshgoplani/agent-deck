// Package session — regression tests for the nested-scratch credential chain.
//
// Successor to issue #1222 (single-source-of-truth credential symlink). #1222
// healed the case where a worker's OWN scratch .credentials.json was clobbered
// into a real-file copy by an in-session /login. This file covers the residual
// 401 class it does NOT cover: NESTED scratch.
//
// When agent-deck runs INSIDE a scratch-pinned worker (a conductor/worker that
// itself spawns children via `agent-deck add/launch`), the parent's
// CLAUDE_CONFIG_DIR — a worker-scratch path — leaks into the agent-deck process
// env. resolveClaudeConfigDir then resolves the CHILD's source profile to the
// PARENT's scratch dir (env priority). The child's scratch credentials are
// symlinked to `<parent-scratch>/.credentials.json`, which may itself be a
// forked real-file copy (post-/login clobber). The child inherits a
// non-canonical token that its OWN reassert can never collapse to the real
// profile canonical — so it 401s, and the 401 PERSISTS ACROSS RESTARTS because
// every restart re-links to the same forked parent copy.
//
// Two layers, both preserving the #1222 no-promote invariant (canonical is
// NEVER written):
//
//	A. resolveClaudeConfigDir ignores a leaked worker-scratch CLAUDE_CONFIG_DIR
//	   so children resolve the real profile, not a parent scratch.
//	B. reassertCredentialSymlink collapses a nested-scratch credential chain to
//	   the true profile canonical instead of linking to a forked scratch copy.

package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// nestedScratchSandbox sandboxes HOME + XDG so workerScratchDirRoot resolves
// under a temp dir, and returns the resolved worker-scratch root.
func nestedScratchSandbox(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	root, err := WorkerScratchDirRoot()
	if err != nil {
		t.Fatalf("resolve worker-scratch root: %v", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir worker-scratch root: %v", err)
	}
	return root
}

// LAYER B (b1): source is a NESTED scratch dir whose .credentials.json is a
// forked real-file copy. reassert must collapse to the TRUE profile canonical
// (recovered from a mirrored sibling symlink), never to the forked copy, and
// must never write canonical.
func TestReassertCredentialSymlink_SourceIsNestedScratch_CollapsesToProfileCanonical(t *testing.T) {
	scratchRoot := nestedScratchSandbox(t)

	// Real profile (OUTSIDE the scratch tree) — the single source of truth.
	profileRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(profileRoot, "projects"), 0o700); err != nil {
		t.Fatalf("mkdir profile projects: %v", err)
	}
	canonToken := `{"token":"true-canonical"}`
	writeCredFile(t, filepath.Join(profileRoot, ".credentials.json"), canonToken, time.Now())

	// Parent scratch dir (a nested source), seeded from profileRoot: a sibling
	// symlink reveals the profile root; its credentials are a FORKED real-file
	// copy (the in-session /login clobber that #1222 only heals on the parent's
	// own restart).
	parentScratch := filepath.Join(scratchRoot, "parent-worker-scratch")
	if err := os.MkdirAll(parentScratch, 0o700); err != nil {
		t.Fatalf("mkdir parent scratch: %v", err)
	}
	if err := os.Symlink(filepath.Join(profileRoot, "projects"), filepath.Join(parentScratch, "projects")); err != nil {
		t.Fatalf("seed parent sibling symlink: %v", err)
	}
	forkedToken := `{"token":"forked-parent-copy"}`
	writeCredFile(t, filepath.Join(parentScratch, ".credentials.json"), forkedToken, time.Now())

	// Child scratch (the worker we are seeding) sourced from the PARENT scratch.
	childScratch := filepath.Join(scratchRoot, "child-worker-scratch")
	if err := os.MkdirAll(childScratch, 0o700); err != nil {
		t.Fatalf("mkdir child scratch: %v", err)
	}

	if err := mirrorProfileEntries(childScratch, parentScratch); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	// Child credentials must be a symlink to the TRUE profile canonical, not the
	// forked parent copy.
	childCred := filepath.Join(childScratch, ".credentials.json")
	assertIsSymlinkTo(t, childCred, filepath.Join(profileRoot, ".credentials.json"))
	viaLink, err := os.ReadFile(childCred)
	if err != nil {
		t.Fatalf("read child cred via link: %v", err)
	}
	if string(viaLink) != canonToken {
		t.Fatalf("child must resolve to the true canonical token; got %q want %q", string(viaLink), canonToken)
	}

	// No-promote invariant: the forked parent copy and the canonical are both
	// untouched.
	if got, _ := os.ReadFile(filepath.Join(parentScratch, ".credentials.json")); string(got) != forkedToken {
		t.Fatalf("parent forked copy must be untouched; got %q", string(got))
	}
	if got, _ := os.ReadFile(filepath.Join(profileRoot, ".credentials.json")); string(got) != canonToken {
		t.Fatalf("canonical must NEVER be written; got %q", string(got))
	}
}

// LAYER B (b2): nested scratch with a forked real-file copy but NO sibling that
// reveals a real profile. reassert must refuse to propagate the forked copy —
// the child must not end up with a credentials symlink pointing into the
// scratch tree (a poisoned, un-healable token). Better a clean login prompt
// than a forked token.
func TestReassertCredentialSymlink_NestedScratchNoSibling_DoesNotPropagateForkedCopy(t *testing.T) {
	scratchRoot := nestedScratchSandbox(t)

	parentScratch := filepath.Join(scratchRoot, "parent-worker-scratch")
	if err := os.MkdirAll(parentScratch, 0o700); err != nil {
		t.Fatalf("mkdir parent scratch: %v", err)
	}
	// Forked real-file copy, and NO sibling symlink to a real profile.
	writeCredFile(t, filepath.Join(parentScratch, ".credentials.json"), `{"token":"forked"}`, time.Now())

	childScratch := filepath.Join(scratchRoot, "child-worker-scratch")
	if err := os.MkdirAll(childScratch, 0o700); err != nil {
		t.Fatalf("mkdir child scratch: %v", err)
	}

	if err := mirrorProfileEntries(childScratch, parentScratch); err != nil {
		t.Fatalf("mirrorProfileEntries: %v", err)
	}

	childCred := filepath.Join(childScratch, ".credentials.json")
	fi, err := os.Lstat(childCred)
	if err != nil {
		if os.IsNotExist(err) {
			return // acceptable: no poisoned link created
		}
		t.Fatalf("lstat child cred: %v", err)
	}
	// If anything exists, it must NOT be a symlink resolving into the scratch tree.
	if fi.Mode()&os.ModeSymlink != 0 {
		resolved, _ := filepath.EvalSymlinks(childCred)
		if pathUnderWorkerScratch(resolved) {
			t.Fatalf("child credentials must not point at a forked scratch copy; resolves to %q (under scratch root)", resolved)
		}
	}
}

// LAYER A (a1): a leaked worker-scratch CLAUDE_CONFIG_DIR must NOT be used as a
// profile source — resolveClaudeConfigDir falls through to the real profile
// (default ~/.claude in a clean sandbox), so children seed from canonical.
func TestResolveClaudeConfigDir_LeakedWorkerScratchEnv_FallsThroughToProfile(t *testing.T) {
	scratchRoot := nestedScratchSandbox(t)
	home := os.Getenv("HOME")

	leaked := filepath.Join(scratchRoot, "some-parent-worker")
	if err := os.MkdirAll(leaked, 0o700); err != nil {
		t.Fatalf("mkdir leaked scratch: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", leaked)

	inst := &Instance{ID: "00000000-0000-0000-0000-000000001377", Tool: "claude", Title: "child-worker"}
	path, source := GetClaudeConfigDirSourceForInstance(inst)
	if source == "env" || path == leaked {
		t.Fatalf("leaked worker-scratch CLAUDE_CONFIG_DIR must be ignored; got path=%q source=%q", path, source)
	}
	if want := filepath.Join(home, ".claude"); path != want {
		t.Fatalf("must fall through to default profile; got path=%q want %q (source=%q)", path, want, source)
	}

	// Group chain (env-wins branch) must also ignore the leaked scratch.
	gpath, gsource := GetClaudeConfigDirSourceForGroup("")
	if gsource == "env" || gpath == leaked {
		t.Fatalf("group chain must also ignore leaked scratch env; got path=%q source=%q", gpath, gsource)
	}
}

// LAYER A (a2) CONTROL: a NORMAL (non-scratch) CLAUDE_CONFIG_DIR must still be
// honored — we only ignore the leaked-scratch case.
func TestResolveClaudeConfigDir_NormalEnv_StillHonored(t *testing.T) {
	nestedScratchSandbox(t)
	home := os.Getenv("HOME")

	normal := filepath.Join(home, ".claude-work")
	if err := os.MkdirAll(normal, 0o700); err != nil {
		t.Fatalf("mkdir normal config dir: %v", err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", normal)

	inst := &Instance{ID: "00000000-0000-0000-0000-000000001378", Tool: "claude", Title: "child-worker"}
	path, source := GetClaudeConfigDirSourceForInstance(inst)
	if source != "env" || path != normal {
		t.Fatalf("normal CLAUDE_CONFIG_DIR must be honored as env; got path=%q source=%q want %q/env", path, source, normal)
	}
}
