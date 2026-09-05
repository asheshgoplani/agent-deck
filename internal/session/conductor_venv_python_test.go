package session

import (
	"os"
	"path/filepath"
	"testing"
)

// findPython3 prefers the conductor-owned venv because the bridge's Python
// dependencies (toml, aiogram) live there on any host whose system interpreter
// is externally managed (PEP 668 — Homebrew, Debian/Ubuntu). ConductorVenvDir
// is what the CLI prints in that remediation, so the two must name the same
// directory: when they drifted, users built a venv at ".venv" that nothing
// ever looked at.
func TestConductorVenvDir_IsWhatFindPython3Prefers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	venvDir := ConductorVenvDir()
	if venvDir == "" {
		t.Fatal("ConductorVenvDir returned empty")
	}
	if filepath.Base(venvDir) != "venv" {
		t.Fatalf("ConductorVenvDir = %q, want a directory named \"venv\" (a dot-prefixed name is invisible to findPython3)", venvDir)
	}

	venvPython := filepath.Join(venvDir, "bin", "python3")
	if err := os.MkdirAll(filepath.Dir(venvPython), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(venvPython, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write fake interpreter: %v", err)
	}

	if got := FindPython3(); got != venvPython {
		t.Fatalf("FindPython3() = %q, want the conductor venv interpreter %q", got, venvPython)
	}
}

// Without a venv, resolution falls through to PATH exactly as before — the
// fallback is what every host that has no PEP 668 problem relies on.
func TestFindPython3_FallsThroughToPathWithoutVenv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	got := FindPython3()
	if got == "" {
		t.Skip("no python3 resolvable in this environment")
	}
	if got == filepath.Join(ConductorVenvDir(), "bin", "python3") {
		t.Fatalf("FindPython3() returned the venv interpreter %q, but no venv exists", got)
	}
}
