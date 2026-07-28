package session

import (
	"os"
	"path/filepath"
	"testing"
)

// Related to #1706. project_path is identity: tmux resolves a relative value
// against the tmux server's cwd, the Claude project slug derived from it points
// somewhere else again, and the #1731 hook-cwd ownership check (which resolves
// symlinks on both sides) can never match a relative string. Every local writer
// must therefore store an absolute path — `agent-deck add` always did, the
// later mutation paths did not.

func TestResolveProjectPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "absolute path is unchanged", input: "/repos/app", want: "/repos/app"},
		{name: "bare relative name anchors to cwd", input: "app", want: filepath.Join(cwd, "app")},
		{name: "nested relative path anchors to cwd", input: "src/app", want: filepath.Join(cwd, "src", "app")},
		{name: "dot-slash prefix is cleaned", input: "./app", want: filepath.Join(cwd, "app")},
		{name: "parent traversal is cleaned", input: "/repos/app/../other", want: "/repos/other"},
		{name: "trailing slash is cleaned", input: "/repos/app/", want: "/repos/app"},
		{name: "tilde is expanded", input: "~/repos/app", want: filepath.Join(home, "repos", "app")},
		{name: "empty stays empty", input: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveProjectPath(tt.input)
			if err != nil {
				t.Fatalf("ResolveProjectPath(%q) returned error: %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ResolveProjectPath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// `agent-deck session set <id> path <p>` and the TUI's edit-session dialog share
// SetField, so the resolution has to happen there rather than in each caller.
func TestSetField_PathIsStoredAbsolute(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}

	inst := &Instance{ProjectPath: "/repos/original"}

	oldValue, _, err := SetField(inst, FieldPath, "relative/target", nil)
	if err != nil {
		t.Fatalf("SetField(path) returned error: %v", err)
	}
	if oldValue != "/repos/original" {
		t.Fatalf("old value = %q, want %q", oldValue, "/repos/original")
	}

	want := filepath.Join(cwd, "relative", "target")
	if inst.ProjectPath != want {
		t.Fatalf("ProjectPath = %q, want %q (a relative project_path resolves differently for tmux, the Claude slug and the #1731 hook-cwd check)", inst.ProjectPath, want)
	}
}

func TestSetField_PathExpandsTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home dir: %v", err)
	}

	inst := &Instance{ProjectPath: "/repos/original"}
	if _, _, err := SetField(inst, FieldPath, "~/repos/app", nil); err != nil {
		t.Fatalf("SetField(path) returned error: %v", err)
	}

	want := filepath.Join(home, "repos", "app")
	if inst.ProjectPath != want {
		t.Fatalf("ProjectPath = %q, want %q (a quoted tilde from a non-expanding shell must still reach a real directory)", inst.ProjectPath, want)
	}
}

func TestSetField_PathAbsoluteValueIsPreserved(t *testing.T) {
	inst := &Instance{ProjectPath: "/repos/original"}
	if _, _, err := SetField(inst, FieldPath, "/repos/moved", nil); err != nil {
		t.Fatalf("SetField(path) returned error: %v", err)
	}
	if inst.ProjectPath != "/repos/moved" {
		t.Fatalf("ProjectPath = %q, want %q (an absolute value must pass through untouched)", inst.ProjectPath, "/repos/moved")
	}
}

// A directory name may legally begin or end with a space, and `add` does not
// trim its argument either, so neither may this. Text fields are trimmed by the
// caller that reads them.
func TestResolveProjectPath_PreservesSignificantWhitespace(t *testing.T) {
	got, err := ResolveProjectPath("/repos/app ")
	if err != nil {
		t.Fatalf("ResolveProjectPath returned error: %v", err)
	}
	if got != "/repos/app " {
		t.Fatalf("ResolveProjectPath(%q) = %q, want the trailing space preserved", "/repos/app ", got)
	}
}

// An SSH session's path names a directory on the remote host, so this machine's
// cwd is not a valid anchor for it.
func TestSetField_PathOnSSHInstanceIsNotResolvedLocally(t *testing.T) {
	inst := &Instance{ProjectPath: "srv/app", SSHHost: "build-box"}
	if _, _, err := SetField(inst, FieldPath, "srv/app-v2", nil); err != nil {
		t.Fatalf("SetField(path) returned error: %v", err)
	}
	if inst.ProjectPath != "srv/app-v2" {
		t.Fatalf("ProjectPath = %q, want %q (a remote path must not be anchored to the local cwd)", inst.ProjectPath, "srv/app-v2")
	}
}
