package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// polluteProcessState puts the state a previous run, or the developer's own
// shell, can leave in a test process: a persisted HOME with the restart
// hand-off and first-run files in it, and the hand-off variables the TUI
// reads on its first frame.
func polluteProcessState(t *testing.T) string {
	t.Helper()
	stale := t.TempDir()
	dataDir := filepath.Join(stale, ".local", "share", "agent-deck")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "install-kind"), []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", stale)
	t.Setenv("XDG_DATA_HOME", filepath.Join(stale, ".local", "share"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(stale, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(stale, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(stale, ".cache"))
	t.Setenv("AGENTDECK_RESTARTED_FROM", "1.0.0")
	t.Setenv("AGENTDECK_RESTART_SELECT", "stale-session-id")
	t.Setenv("AGENTDECK_PROFILE", "stale")
	t.Setenv("AGENT_DECK_ALLOW_NO_TTY", "0")
	return stale
}

// TestPinVisualCheckEnvDropsAmbientState: the visual check process itself
// must start from a private HOME/XDG and no agent-deck hand-off variables,
// whatever the previous run or the caller's shell left behind.
func TestPinVisualCheckEnvDropsAmbientState(t *testing.T) {
	stale := polluteProcessState(t)
	t.Setenv("GOCACHE", "")
	if err := os.Unsetenv("GOCACHE"); err != nil {
		t.Fatal(err)
	}

	home := pinVisualCheckEnv(t)

	if home == "" || home == stale {
		t.Fatalf("pinned HOME = %q, want a fresh dir distinct from the ambient %q", home, stale)
	}
	for _, k := range []string{"HOME"} {
		if got := os.Getenv(k); got != home {
			t.Errorf("%s = %q, want the pinned %q", k, got, home)
		}
	}
	for _, k := range []string{"XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if got := os.Getenv(k); !strings.HasPrefix(got, home+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a path under the pinned HOME %q", k, got, home)
		}
	}
	for _, k := range []string{"AGENTDECK_RESTARTED_FROM", "AGENTDECK_RESTART_SELECT", "AGENTDECK_PROFILE", "AGENT_DECK_ALLOW_NO_TTY"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s still set to %q in the pinned process", k, v)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".local", "share", "agent-deck", "install-kind")); err == nil {
		t.Errorf("pinned HOME carries a persisted install-kind marker")
	}
	// Moving HOME must not throw away the Go build cache: the test builds
	// cmd/agent-deck twice and a cold cache would make the run time out.
	if os.Getenv("GOCACHE") == "" {
		t.Errorf("GOCACHE not resolved before HOME moved; the build would run cold")
	}
}

// TestSandboxEnvIgnoresAmbientState: nothing the process holds may reach the
// sandboxed binary; the sandbox is built from an allowlist rooted at its own
// directory.
func TestSandboxEnvIgnoresAmbientState(t *testing.T) {
	stale := polluteProcessState(t)
	root := t.TempDir()
	bin := filepath.Join(root, "agent-deck")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	s := &suite{bin: bin}
	if err := s.setup(); err != nil {
		t.Skipf("sandbox setup unavailable here: %v", err)
	}
	defer s.cleanup()
	for _, kv := range s.env {
		if strings.Contains(kv, stale) || strings.HasPrefix(kv, "AGENTDECK_RESTART") || strings.HasPrefix(kv, "AGENTDECK_PROFILE=") {
			t.Errorf("sandbox env inherited ambient state: %s", kv)
		}
		if strings.HasPrefix(kv, "HOME=") && kv != "HOME="+s.root {
			t.Errorf("sandbox HOME = %q, want %q", kv, "HOME="+s.root)
		}
	}
}

// pinVisualCheckEnv gives the calling test a private process state: HOME and
// the XDG dirs point at fresh temp dirs, and every AGENTDECK_*/AGENT_DECK_*
// variable (restart hand-off, profile, TTY overrides) is removed, so nothing
// persisted by an earlier run or the caller's shell can reach the harness.
// The Go build cache locations are resolved first, because they default to a
// path under HOME and moving HOME would otherwise force a cold build.
// It returns the pinned HOME.
func pinVisualCheckEnv(t *testing.T) string {
	t.Helper()
	for _, k := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		if os.Getenv(k) != "" {
			continue
		}
		if out, err := exec.Command("go", "env", k).Output(); err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				t.Setenv(k, v)
			}
		}
	}
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "AGENTDECK_") || strings.HasPrefix(k, "AGENT_DECK_") {
			t.Setenv(k, "") // registers the restore
			if err := os.Unsetenv(k); err != nil {
				t.Fatal(err)
			}
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	return home
}
