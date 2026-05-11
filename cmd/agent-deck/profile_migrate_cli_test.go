package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// CLI subprocess tests for issue #928: `session move --to-profile`,
// `group move --to-profile`, and `conductor move --to-profile`.
//
// These complement the unit tests in internal/session/profile_migrate_test.go
// by exercising the actual binary, argv parsing, and JSON output shape.

// bootstrapProfile runs a harmless `list` command in the named profile so
// `~/.agent-deck/profiles/<name>/state.db` is created on disk. The cross-
// profile migration deliberately refuses missing target profiles, so every
// test that migrates into "<dst>" must call this first.
func bootstrapProfile(t *testing.T, home, profile string) {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "-p", profile, "list", "--json")
	if code != 0 {
		t.Fatalf("bootstrap profile %q failed: code=%d\nstdout: %s\nstderr: %s",
			profile, code, stdout, stderr)
	}
}

// addInProfile adds a stopped session in the given profile and returns its ID.
func addInProfile(t *testing.T, home, profile, title, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	stdout, stderr, code := runAgentDeck(t, home,
		"-p", profile, "add",
		"-t", title,
		"--no-parent",
		"--json",
		path,
	)
	if code != 0 {
		t.Fatalf("add in profile %q failed: code=%d\nstdout: %s\nstderr: %s",
			profile, code, stdout, stderr)
	}
	var resp struct{ ID string `json:"id"` }
	if err := json.Unmarshal([]byte(stdout), &resp); err != nil {
		t.Fatalf("parse add response: %v\nstdout: %s", err, stdout)
	}
	if resp.ID == "" {
		t.Fatalf("add returned empty id: %s", stdout)
	}
	return resp.ID
}

func listJSONForProfile(t *testing.T, home, profile string) string {
	t.Helper()
	stdout, stderr, code := runAgentDeck(t, home, "-p", profile, "list", "--json")
	if code != 0 {
		t.Fatalf("list -p %q failed: code=%d\nstderr: %s", profile, code, stderr)
	}
	return stdout
}

// TestSessionMoveToProfile_Basic — happy path: row absent in src, present in dst.
func TestSessionMoveToProfile_Basic(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	bootstrapProfile(t, home, "dst")
	id := addInProfile(t, home, "src", "basic-migrate", filepath.Join(home, "proj"))

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", "src", "session", "move", id,
		"--to-profile", "dst",
		"--json",
	)
	if code != 0 {
		t.Fatalf("migrate failed: code=%d\nstdout: %s\nstderr: %s", code, stdout, stderr)
	}

	srcList := listJSONForProfile(t, home, "src")
	if strings.Contains(srcList, id) {
		t.Errorf("src still has session %s: %s", id, srcList)
	}
	dstList := listJSONForProfile(t, home, "dst")
	if !strings.Contains(dstList, id) {
		t.Errorf("dst missing session %s: %s", id, dstList)
	}
}

func TestSessionMoveToProfile_RefusesMissingTargetProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	id := addInProfile(t, home, "src", "no-target", filepath.Join(home, "p"))

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", "src", "session", "move", id,
		"--to-profile", "ghost",
		"--json",
	)
	if code == 0 {
		t.Fatalf("expected failure for missing target profile; got success\nstdout: %s", stdout)
	}
	combined := strings.ToLower(stdout + stderr)
	if !strings.Contains(combined, "ghost") || !strings.Contains(combined, "does not exist") {
		t.Errorf("error should mention the missing profile name; got stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestSessionMoveToProfile_RefusesSameProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	id := addInProfile(t, home, "src", "same-profile", filepath.Join(home, "p"))

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", "src", "session", "move", id,
		"--to-profile", "src",
		"--json",
	)
	if code == 0 {
		t.Fatalf("expected failure for same profile; got success\nstdout: %s", stdout)
	}
	combined := strings.ToLower(stdout + stderr)
	if !strings.Contains(combined, "same") {
		t.Errorf("error should mention same-profile; got stdout=%s stderr=%s", stdout, stderr)
	}
}

func TestSessionMoveToProfile_RejectsPathAndToProfileTogether(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	bootstrapProfile(t, home, "dst")
	id := addInProfile(t, home, "src", "two-args", filepath.Join(home, "p"))

	_, stderr, code := runAgentDeck(t, home,
		"-p", "src", "session", "move", id,
		filepath.Join(home, "new-path"),
		"--to-profile", "dst",
		"--json",
	)
	if code == 0 {
		t.Fatal("expected exit-1 when both <new-path> and --to-profile are given")
	}
	if !strings.Contains(strings.ToLower(stderr), "incompatible") {
		t.Errorf("stderr should mention incompatibility; got %s", stderr)
	}
}

func TestSessionMoveToProfile_Idempotent(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	bootstrapProfile(t, home, "dst")
	id := addInProfile(t, home, "src", "idem", filepath.Join(home, "p"))

	for i := 0; i < 2; i++ {
		stdout, stderr, code := runAgentDeck(t, home,
			"-p", "src", "session", "move", id,
			"--to-profile", "dst", "--json",
		)
		if code != 0 {
			t.Fatalf("migrate iteration %d: code=%d\nstdout: %s\nstderr: %s",
				i, code, stdout, stderr)
		}
	}
	dstList := listJSONForProfile(t, home, "dst")
	if strings.Count(dstList, id) == 0 {
		t.Errorf("dst missing session after idempotent re-run: %s", dstList)
	}
}

func TestGroupMoveToProfile_BatchMigratesAllSessions(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	bootstrapProfile(t, home, "dst")

	// Add 3 sessions and assign them to a custom group.
	var ids []string
	for i, name := range []string{"a", "b", "c"} {
		path := filepath.Join(home, "proj-"+name)
		id := addInProfile(t, home, "src", "grp-"+name, path)
		ids = append(ids, id)
		_, stderr, code := runAgentDeck(t, home,
			"-p", "src", "group", "move", id, "work/api",
			"--json",
		)
		if code != 0 {
			t.Fatalf("assign session %d to group: code=%d stderr=%s", i, code, stderr)
		}
	}

	stdout, stderr, code := runAgentDeck(t, home,
		"-p", "src", "group", "move", "work/api",
		"--to-profile", "dst", "--json",
	)
	if code != 0 {
		t.Fatalf("group migrate failed: code=%d\nstdout: %s\nstderr: %s",
			code, stdout, stderr)
	}

	srcList := listJSONForProfile(t, home, "src")
	for _, id := range ids {
		if strings.Contains(srcList, id) {
			t.Errorf("src still has session %s after group migrate", id)
		}
	}
	dstList := listJSONForProfile(t, home, "dst")
	for _, id := range ids {
		if !strings.Contains(dstList, id) {
			t.Errorf("dst missing session %s after group migrate", id)
		}
	}
}

// TestSessionMoveToProfile_RejectsIncompatibleFlags asserts that --to-profile
// errors out when combined with --group / --no-restart / --copy rather than
// silently ignoring them (Copilot fix #6).
func TestSessionMoveToProfile_RejectsIncompatibleFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")
	bootstrapProfile(t, home, "dst")
	id := addInProfile(t, home, "src", "incompat", filepath.Join(home, "p"))

	cases := []struct {
		name string
		flag string
	}{
		{"group", "--group"},
		{"no-restart", "--no-restart"},
		{"copy", "--copy"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"-p", "src", "session", "move", id,
				"--to-profile", "dst", "--json", tc.flag}
			if tc.flag == "--group" {
				args = append(args, "work/api")
			}
			stdout, stderr, code := runAgentDeck(t, home, args...)
			if code == 0 {
				t.Fatalf("expected failure when --to-profile is combined with %s; stdout=%s", tc.flag, stdout)
			}
			combined := strings.ToLower(stdout + stderr)
			if !strings.Contains(combined, "incompatible") {
				t.Errorf("error should mention incompatibility; got stdout=%s stderr=%s", stdout, stderr)
			}
		})
	}
}

func TestConductorMoveToProfile_RequiresToProfile(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home := t.TempDir()
	bootstrapProfile(t, home, "src")

	_, stderr, code := runAgentDeck(t, home,
		"-p", "src", "conductor", "move", "alpha",
	)
	if code == 0 {
		t.Fatal("expected non-zero exit when --to-profile is missing")
	}
	if !strings.Contains(strings.ToLower(stderr), "to-profile") {
		t.Errorf("stderr should mention --to-profile; got %s", stderr)
	}
}
