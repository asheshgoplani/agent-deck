package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Tests for issue #786: `session set-parent` must NOT silently rewrite the
// child session's group. Group inheritance is opt-in via --inherit-group.
//
// Background: Until v1.7.70, `set-parent` mutated two fields atomically —
// parent_session_id AND group. `unset-parent` only reversed the first,
// leaving the group permanently shifted. A maintenance script that
// retroactively re-linked orphan sessions silently relocated all of them
// into the conductor's group; recovery was manual and lossy.
//
// These tests lock in the invariant: post-hoc parent linking does not
// mutate group unless the user explicitly asks for it.

// addSessionInGroup is a small helper that registers a session and returns
// its ID. Keeps the body of each test focused on the assertion of interest.
func addSessionInGroup(t *testing.T, home, projectDir, title, group string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home,
		"add", "-t", title, "-c", "claude", "-g", group,
		"--no-parent", "--json", projectDir,
	)
	if code != 0 {
		t.Fatalf("add %s (group=%s) failed (%d)\nstdout: %s\nstderr: %s",
			title, group, code, stdout, stderr)
	}
	var resp struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal add %s: %v\n%s", title, err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add %s returned empty id\n%s", title, stdout)
	}
	return resp.ID
}

// showGroup returns the persisted group field of a session via
// `session show --json`.
func showGroup(t *testing.T, home, id string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 {
		t.Fatalf("session show %s failed (%d)\nstdout: %s\nstderr: %s",
			id, code, stdout, stderr)
	}
	var resp struct {
		Group  string `json:"group"`
		Parent string `json:"parent_session_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal show: %v\n%s", err, stdout)
	}
	return resp.Group
}

// showParent returns the parent_session_id field of a session.
func showParent(t *testing.T, home, id string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "session", "show", id, "--json")
	if code != 0 {
		t.Fatalf("session show %s failed (%d)\nstdout: %s\nstderr: %s",
			id, code, stdout, stderr)
	}
	var resp struct {
		Parent string `json:"parent_session_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("unmarshal show: %v\n%s", err, stdout)
	}
	return resp.Parent
}

// TestSetParent_DoesNotInheritGroupByDefault is the primary #786 regression.
// Linking a session in group "innotrade" under a parent in group "conductor"
// must leave the child in "innotrade".
func TestSetParent_DoesNotInheritGroupByDefault(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	childID := addSessionInGroup(t, home, projectDir, "demo-victim", "innotrade")
	parentID := addSessionInGroup(t, home, projectDir, "the-conductor", "conductor")

	if got := showGroup(t, home, childID); got != "innotrade" {
		t.Fatalf("pre-set-parent group = %q, want %q", got, "innotrade")
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "set-parent", childID, parentID, "--json",
	)
	if code != 0 {
		t.Fatalf("set-parent failed (%d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	if got := showGroup(t, home, childID); got != "innotrade" {
		t.Fatalf("post-set-parent group = %q, want %q (#786 regression: "+
			"set-parent must not silently rewrite group)",
			got, "innotrade")
	}
	if got := showParent(t, home, childID); got != parentID {
		t.Fatalf("post-set-parent parent_session_id = %q, want %q",
			got, parentID)
	}
}

// TestSetParent_InheritGroupOptIn verifies the prior behavior is still
// available when the user explicitly opts in via --inherit-group. This
// preserves the workflow for callers who genuinely want post-hoc
// inheritance (e.g. moving a freshly-spawned orphan into a conductor's
// world).
func TestSetParent_InheritGroupOptIn(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	childID := addSessionInGroup(t, home, projectDir, "opt-in-child", "innotrade")
	parentID := addSessionInGroup(t, home, projectDir, "opt-in-parent", "conductor")

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "set-parent", childID, parentID, "--inherit-group", "--json",
	)
	if code != 0 {
		t.Fatalf("set-parent --inherit-group failed (%d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	if got := showGroup(t, home, childID); got != "conductor" {
		t.Fatalf("--inherit-group did not inherit: group = %q, want %q",
			got, "conductor")
	}
}

// TestUnsetParent_DoesNotChangeGroup locks in that unset-parent leaves the
// group untouched. (This was already true on main, but the issue's
// round-trip invariant makes it part of the public contract.)
func TestUnsetParent_DoesNotChangeGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	childID := addSessionInGroup(t, home, projectDir, "unset-child", "innotrade")
	parentID := addSessionInGroup(t, home, projectDir, "unset-parent", "conductor")

	// Link with explicit inheritance so the child has a non-original
	// group when unset runs. This makes the test's invariant precise:
	// unset-parent must not touch group, period — not "must restore",
	// not "must clear".
	if _, _, code := runAgentDeck(t, home,
		"session", "set-parent", childID, parentID, "--inherit-group", "--json",
	); code != 0 {
		t.Fatalf("set-parent --inherit-group failed (%d)", code)
	}
	if got := showGroup(t, home, childID); got != "conductor" {
		t.Fatalf("setup: expected group=conductor after inherit, got %q", got)
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"session", "unset-parent", childID, "--json",
	)
	if code != 0 {
		t.Fatalf("unset-parent failed (%d)\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	if got := showGroup(t, home, childID); got != "conductor" {
		t.Fatalf("unset-parent rewrote group: got %q, want %q (must not touch group)",
			got, "conductor")
	}
	if got := showParent(t, home, childID); got != "" {
		t.Fatalf("unset-parent left parent_session_id = %q, want empty", got)
	}
}

// TestSetParent_RoundTripPreservesGroup is the headline invariant: a full
// link/unlink cycle on the default (non-inherit) path must leave the
// session's group bit-identical.
func TestSetParent_RoundTripPreservesGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	projectDir := filepath.Join(home, "proj")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	const originalGroup = "innotrade"
	childID := addSessionInGroup(t, home, projectDir, "rt-child", originalGroup)
	parentID := addSessionInGroup(t, home, projectDir, "rt-parent", "conductor")

	if _, _, code := runAgentDeck(t, home,
		"session", "set-parent", childID, parentID, "--json",
	); code != 0 {
		t.Fatalf("set-parent failed (%d)", code)
	}

	if _, _, code := runAgentDeck(t, home,
		"session", "unset-parent", childID, "--json",
	); code != 0 {
		t.Fatalf("unset-parent failed (%d)", code)
	}

	if got := showGroup(t, home, childID); got != originalGroup {
		t.Fatalf("round-trip group = %q, want %q (#786 invariant)",
			got, originalGroup)
	}
	if got := showParent(t, home, childID); got != "" {
		t.Fatalf("round-trip parent_session_id = %q, want empty", got)
	}
}

// TestSetParent_HelpMentionsInheritGroup ensures discoverability: a user
// running `--help` should see the new flag and not the misleading old
// "will inherit" sentence.
func TestSetParent_HelpMentionsInheritGroup(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	stdout, stderr, _ := runAgentDeck(t, home, "session", "set-parent", "--help")
	combined := stdout + stderr
	if !strings.Contains(combined, "--inherit-group") &&
		!strings.Contains(combined, "-inherit-group") {
		t.Errorf("set-parent --help does not mention --inherit-group:\n%s", combined)
	}
	if strings.Contains(combined, "will inherit the parent's group") {
		t.Errorf("set-parent --help still claims unconditional group inheritance:\n%s", combined)
	}
}
