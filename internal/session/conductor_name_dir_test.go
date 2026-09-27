package session

import (
	"path/filepath"
	"testing"
)

func TestConductorNameDir_RejectsNonLocalNames(t *testing.T) {
	for _, name := range []string{"", ".", "..", "../x", "a/b", `a\b`, "/abs", "x/../../y"} {
		if dir, err := ConductorNameDir(name); err == nil {
			t.Errorf("ConductorNameDir(%q) = %q, want error", name, dir)
		}
	}
}

func TestConductorNameDir_AcceptsSingleElementNames(t *testing.T) {
	_, xdgConfigHome, _ := setupSessionXDGPathEnv(t)
	base := t.TempDir()
	writeConductorDirConfig(t, xdgConfigHome, base)

	for _, name := range []string{"main", "ops-1", "a..b", "My_Conductor.v2"} {
		dir, err := ConductorNameDir(name)
		if err != nil {
			t.Fatalf("ConductorNameDir(%q): %v", name, err)
		}
		if want := filepath.Join(base, name); dir != want {
			t.Fatalf("ConductorNameDir(%q) = %q, want %q", name, dir, want)
		}
	}
}
