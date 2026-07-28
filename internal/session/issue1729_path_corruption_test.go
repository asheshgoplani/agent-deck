package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Issue #1729 (data-corruption class): instances.project_path was silently
// rewritten from the cwd reported by Claude hook payloads, and any first-seen
// session id could bind to a cold instance. Three failure modes:
//
//  1. Headless `claude -p --no-session-persistence` workers (cwd=$TMPDIR) that
//     inherit AGENTDECK_INSTANCE_ID poison unrelated sessions: reject/rebind
//     loops in session-id-lifecycle.jsonl and project_path flapping to $TMPDIR.
//  2. WORST: a legitimate `cd` into a subdir rewrites project_path; the next
//     start resolves the transcript under the SUBDIR's Claude project slug,
//     finds nothing, and the session cannot resume.
//  3. Path flapping — user intent (path fixed at creation) is unenforceable.
//
// The fix: project_path is immutable post-creation except explicit operations
// (`session set path`, or the declared multi-repo additional_paths swap), and
// session-id binding rejects candidates whose hook cwd is provably outside
// every path the instance owns.
//
// Written test-first: each guard test FAILS on pre-#1729 main (with the
// behavior-neutral cwd-plumbing commit applied so they compile).

// isolateHome1729 gives the test a sandboxed HOME + cleared XDG/Claude env so
// nothing touches real user data.
func isolateHome1729(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("AGENT_DECK_HOME", "")
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	ClearUserConfigCache()
	t.Cleanup(func() { ClearUserConfigCache() })
	return home
}

// TestSyncInstanceCwd_RefusesRewriteFromHookCwd pins failure mode 2: a hook
// payload reporting a subdirectory (a legitimate in-session `cd`) must NOT
// rewrite the persisted project_path. The transcript lives under the ORIGINAL
// path's Claude project slug; relocating project_path breaks the next resume.
func TestSyncInstanceCwd_RefusesRewriteFromHookCwd(t *testing.T) {
	const profile = "_test_issue1729_mode2"
	_, storage := bootstrapDaemonProfile(t, profile)

	projDir := filepath.Join(os.Getenv("HOME"), "workspace")
	subDir := filepath.Join(projDir, "sub", "repo")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	inst := &Instance{
		ID:          "inst-1729-mode2",
		Title:       "mode2",
		ProjectPath: projDir,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	found, err := storage.SyncInstanceCwd(inst.ID, subDir)
	if err != nil {
		t.Fatalf("SyncInstanceCwd: %v", err)
	}
	if !found {
		t.Fatalf("instance not found in profile")
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, in := range instances {
		if in.ID == inst.ID && in.ProjectPath != projDir {
			t.Fatalf("project_path was silently rewritten from hook cwd: want %q, got %q (this breaks resume — issue #1729 mode 2)",
				projDir, in.ProjectPath)
		}
	}
}

// TestSyncInstanceCwd_ForeignTmpdirCannotRewritePath pins the project_path half
// of failure mode 1: a foreign headless worker's $TMPDIR cwd must never land in
// project_path (the observed flap to /private/var/folders/…/T and back).
func TestSyncInstanceCwd_ForeignTmpdirCannotRewritePath(t *testing.T) {
	const profile = "_test_issue1729_mode1_path"
	_, storage := bootstrapDaemonProfile(t, profile)

	projDir := filepath.Join(os.Getenv("HOME"), "realproject")
	foreignTmp := filepath.Join(os.Getenv("HOME"), "fake-tmpdir", "T")
	for _, d := range []string{projDir, foreignTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	inst := &Instance{
		ID:          "inst-1729-mode1-path",
		Title:       "mode1",
		ProjectPath: projDir,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := storage.SyncInstanceCwd(inst.ID, foreignTmp); err != nil {
		t.Fatalf("SyncInstanceCwd: %v", err)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, in := range instances {
		if in.ID == inst.ID && in.ProjectPath != projDir {
			t.Fatalf("foreign $TMPDIR cwd rewrote project_path: want %q, got %q (issue #1729 mode 1 flap)",
				projDir, in.ProjectPath)
		}
	}
}

// TestSyncInstanceCwd_AllowsDeclaredMultiRepoSwap pins the one sanctioned
// hook-driven mutation: when the reported cwd IS a user-declared
// additional_paths entry, the multi-repo primary swap still applies.
func TestSyncInstanceCwd_AllowsDeclaredMultiRepoSwap(t *testing.T) {
	const profile = "_test_issue1729_swap"
	_, storage := bootstrapDaemonProfile(t, profile)

	dirA := filepath.Join(os.Getenv("HOME"), "repoA")
	dirB := filepath.Join(os.Getenv("HOME"), "repoB")
	for _, d := range []string{dirA, dirB} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	inst := &Instance{
		ID:              "inst-1729-swap",
		Title:           "swap",
		ProjectPath:     dirA,
		AdditionalPaths: []string{dirB},
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusRunning,
		CreatedAt:       time.Now(),
	}
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := storage.SyncInstanceCwd(inst.ID, dirB); err != nil {
		t.Fatalf("SyncInstanceCwd: %v", err)
	}

	instances, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	for _, in := range instances {
		if in.ID != inst.ID {
			continue
		}
		if in.ProjectPath != dirB {
			t.Fatalf("declared multi-repo swap regressed: want primary %q, got %q", dirB, in.ProjectPath)
		}
		swappedBack := false
		for _, p := range in.AdditionalPaths {
			if p == dirA {
				swappedBack = true
			}
		}
		if !swappedBack {
			t.Fatalf("multi-repo swap lost original primary %q from additional_paths: %v", dirA, in.AdditionalPaths)
		}
	}
}

// TestUpdateHookStatus_ColdStartRejectsForeignCwdCandidate pins failure mode 1
// at the binding layer: a cold (unbound) instance previously accepted ANY first
// candidate session id — including a headless `claude -p` worker firing hooks
// from $TMPDIR with our inherited AGENTDECK_INSTANCE_ID. A candidate whose
// hook cwd is outside every instance path must be excluded from binding
// entirely.
func TestUpdateHookStatus_ColdStartRejectsForeignCwdCandidate(t *testing.T) {
	home := isolateHome1729(t)

	projDir := filepath.Join(home, "realproject")
	foreignTmp := filepath.Join(home, "fake-tmpdir", "T")
	for _, d := range []string{projDir, foreignTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	const sessForeign = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	inst := &Instance{
		ID:          "inst-1729-coldstart",
		Title:       "coldstart",
		ProjectPath: projDir,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: sessForeign,
		Event:     "PreToolUse",
		UpdatedAt: time.Now(),
		Cwd:       foreignTmp,
	})

	if inst.ClaudeSessionID == sessForeign {
		t.Fatalf("cold instance bound a foreign-cwd candidate session id %q (cwd %q outside project %q) — issue #1729 mode 1",
			sessForeign, foreignTmp, projDir)
	}

	sawReject := false
	for _, ev := range readLifecycleEventsFor(t, inst.ID) {
		if ev.Action == "reject" && ev.Reason == "candidate_cwd_outside_instance_paths" {
			sawReject = true
		}
	}
	if !sawReject {
		t.Fatalf("expected a candidate_cwd_outside_instance_paths reject lifecycle event")
	}
}

// TestUpdateHookStatus_ForeignCwdDoesNotUsurpEstablishedBinding: even a
// candidate with MORE conversation data (which passes the v1.7.7/v1.7.23 size
// guards) must not rebind an established instance when its hook cwd is foreign,
// and the status flip it carried must not stick.
func TestUpdateHookStatus_ForeignCwdDoesNotUsurpEstablishedBinding(t *testing.T) {
	home := isolateHome1729(t)

	projDir := filepath.Join(home, "realproject")
	foreignTmp := filepath.Join(home, "fake-tmpdir", "T")
	for _, d := range []string{projDir, foreignTmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	const sessX = "dddddddd-dddd-dddd-dddd-dddddddddddd"
	const sessY = "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	inst := &Instance{
		ID:              "inst-1729-established",
		Title:           "established",
		ProjectPath:     projDir,
		GroupPath:       DefaultGroupPath,
		Tool:            "claude",
		Status:          StatusRunning,
		ClaudeSessionID: sessX,
		CreatedAt:       time.Now(),
	}
	// Y is richer than X, so the pre-#1729 data/size guards allow the rebind.
	writeTranscript1349(t, inst, sessX, 1)
	writeTranscript1349(t, inst, sessY, 50)

	// Establish the instance's own hook status first.
	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: sessX,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now().Add(-time.Second),
		Cwd:       projDir,
	})

	inst.UpdateHookStatus(&HookStatus{
		Status:    "waiting",
		SessionID: sessY,
		Event:     "Stop",
		UpdatedAt: time.Now(),
		Cwd:       foreignTmp,
	})

	if inst.ClaudeSessionID != sessX {
		t.Fatalf("foreign-cwd candidate usurped the binding: want %q, got %q (issue #1729 mode 1)",
			sessX, inst.ClaudeSessionID)
	}
	if inst.hookStatus != "running" {
		t.Fatalf("foreign hook's status stuck: want restored %q, got %q", "running", inst.hookStatus)
	}
}

// TestUpdateHookStatus_SubdirCwdStillBinds guards against over-blocking: a
// session that legitimately `cd`s deeper inside its project tree still binds
// normally — subdirectories of owned paths are same-session evidence, not
// foreign.
func TestUpdateHookStatus_SubdirCwdStillBinds(t *testing.T) {
	home := isolateHome1729(t)

	projDir := filepath.Join(home, "realproject")
	subDir := filepath.Join(projDir, "sub", "repo")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	const sessNew = "ffffffff-ffff-ffff-ffff-ffffffffffff"
	inst := &Instance{
		ID:          "inst-1729-subdir",
		Title:       "subdir",
		ProjectPath: projDir,
		GroupPath:   DefaultGroupPath,
		Tool:        "claude",
		Status:      StatusRunning,
		CreatedAt:   time.Now(),
	}

	inst.UpdateHookStatus(&HookStatus{
		Status:    "running",
		SessionID: sessNew,
		Event:     "UserPromptSubmit",
		UpdatedAt: time.Now(),
		Cwd:       subDir,
	})

	if inst.ClaudeSessionID != sessNew {
		t.Fatalf("subdir cwd was over-blocked: cold bind should accept cwd %q under project %q; ClaudeSessionID=%q",
			subDir, projDir, inst.ClaudeSessionID)
	}
}
