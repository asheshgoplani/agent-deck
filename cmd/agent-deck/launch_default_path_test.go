package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Tests for resolveLaunchPath — `launch` must mirror `add`'s default-path
// resolution chain (#1303): explicit arg → group default_path → global config
// default_path → cwd. An explicit "." must keep meaning cwd even when
// defaults are configured.
//
// Reuses the env isolation helpers from add_test.go (setupAddDefaultPathTest,
// writeAddUserConfig) so both commands are tested against the same harness.

func seedGroupWithDefaultPath(t *testing.T, profile, name, groupPath, defaultPath string) {
	t.Helper()

	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		t.Fatalf("NewStorageWithProfile: %v", err)
	}
	groupTree := session.NewGroupTreeWithGroups(nil, []*session.GroupData{
		{Name: name, Path: groupPath, Expanded: true, DefaultPath: defaultPath},
	})
	if err := storage.SaveWithGroups(nil, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	if err := storage.Close(); err != nil {
		t.Fatalf("Close storage: %v", err)
	}
}

// TestResolveLaunchPathExplicitDotStaysCwd pins that an explicit "." keeps
// its "right here" meaning even when a group default_path AND a global
// default_path are configured — the default chain must only apply when no
// path argument is given at all.
func TestResolveLaunchPathExplicitDotStaysCwd(t *testing.T) {
	home, cwd, profile := setupAddDefaultPathTest(t)
	groupDefault := filepath.Join(home, "group-home")
	globalDefault := filepath.Join(home, "global-home")
	for _, dir := range []string{groupDefault, globalDefault} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
	seedGroupWithDefaultPath(t, profile, "Work", "work", groupDefault)

	got, err := resolveLaunchPath(".", "work", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != cwd {
		t.Fatalf(`explicit "." resolved to %q, want cwd %q`, got, cwd)
	}
}

func TestResolveLaunchPathExplicitPathIgnoresDefaults(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	groupDefault := filepath.Join(home, "group-home")
	globalDefault := filepath.Join(home, "global-home")
	explicitPath := filepath.Join(home, "explicit")
	for _, dir := range []string{groupDefault, globalDefault, explicitPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
	seedGroupWithDefaultPath(t, profile, "Work", "work", groupDefault)

	got, err := resolveLaunchPath(explicitPath, "work", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != explicitPath {
		t.Fatalf("explicit path resolved to %q, want %q", got, explicitPath)
	}
}

// TestResolveLaunchPathGroupDefaultPrecedesGlobal pins the first chain link:
// a pathless launch into a group with a default_path lands there, ahead of
// the global config default_path. The selector is the group's display name
// ("Work", not "work") to pin resolveGroupPathForAdd canonicalization.
func TestResolveLaunchPathGroupDefaultPrecedesGlobal(t *testing.T) {
	home, _, profile := setupAddDefaultPathTest(t)
	groupDefault := filepath.Join(home, "group-home")
	globalDefault := filepath.Join(home, "global-home")
	for _, dir := range []string{groupDefault, globalDefault} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
	seedGroupWithDefaultPath(t, profile, "Work", "work", groupDefault)

	got, err := resolveLaunchPath("", "Work", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != groupDefault {
		t.Fatalf("pathless launch resolved to %q, want group default_path %q", got, groupDefault)
	}
}

// TestResolveLaunchPathFallsBackToGlobalDefaultPath pins the second chain
// link: with no group default available the global config default_path wins,
// whether the group selector is absent or names a group without a default.
func TestResolveLaunchPathFallsBackToGlobalDefaultPath(t *testing.T) {
	tests := []struct {
		name          string
		groupSelector string
		seedGroup     bool
	}{
		{name: "no group selector", groupSelector: ""},
		{name: "group without default_path", groupSelector: "work", seedGroup: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home, _, profile := setupAddDefaultPathTest(t)
			globalDefault := filepath.Join(home, "global-home")
			if err := os.MkdirAll(globalDefault, 0o755); err != nil {
				t.Fatalf("mkdir %s: %v", globalDefault, err)
			}
			writeAddUserConfig(t, home, `default_path = "`+globalDefault+`"`+"\n")
			if tt.seedGroup {
				seedGroupWithDefaultPath(t, profile, "Work", "work", "")
			}

			got, err := resolveLaunchPath("", tt.groupSelector, profile)
			if err != nil {
				t.Fatalf("resolveLaunchPath: %v", err)
			}
			if got != globalDefault {
				t.Fatalf("pathless launch resolved to %q, want global default_path %q", got, globalDefault)
			}
		})
	}
}

func TestResolveLaunchPathFallsBackToCwd(t *testing.T) {
	_, cwd, profile := setupAddDefaultPathTest(t)

	got, err := resolveLaunchPath("", "", profile)
	if err != nil {
		t.Fatalf("resolveLaunchPath: %v", err)
	}
	if got != cwd {
		t.Fatalf("pathless launch resolved to %q, want cwd %q", got, cwd)
	}
}
