package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests for the LoadUserConfig sticky-error fix and the env_file /
// config.toml loudness fixes in buildEnvSourceCommand.
// Reuses withIsolatedHomeAndConfig from pergroupconfig_nested_test.go.

func TestLoadUserConfig_ParseErrorIsSticky(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude
env_file = "broken
`)
	if _, err := LoadUserConfig(); err == nil {
		t.Fatal("first load of a broken config.toml must return the parse error")
	}
	// Second call is a cache hit — the error must survive it. Before the
	// sticky-error fix only the FIRST load after an mtime change saw the
	// error; long-running processes then spawned sessions on silent
	// defaults with zero diagnostics.
	if _, err := LoadUserConfig(); err == nil {
		t.Fatal("cache hit must keep returning the parse error while the file is broken")
	}

	// A spawn during the broken window must say so in the pane.
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "claude")
	cmd := inst.buildEnvSourceCommand()
	if !strings.Contains(cmd, "config.toml error") {
		t.Errorf("spawn during a broken-config window must warn in the pane:\n%s", cmd)
	}

	// Fixing the file clears the error on the next load.
	configPath := filepath.Join(tmpHome, ".agent-deck", "config.toml")
	if err := os.WriteFile(configPath, []byte("[claude]\ncommand = \"claude\"\n"), 0o600); err != nil {
		t.Fatalf("fix config: %v", err)
	}
	bumpMtime(t, configPath)
	if _, err := LoadUserConfig(); err != nil {
		t.Fatalf("fixed config must load clean, got: %v", err)
	}
}

func TestGroupClaude_MissingEnvFileWarnsInPane(t *testing.T) {
	tmpHome := withIsolatedHomeAndConfig(t, `
[groups."work".claude]
env_file = "~/.agent-deck/groups/missing.env"
`)
	inst := NewInstanceWithGroupAndTool("s1", "/tmp/p", "work", "claude")

	cmd := inst.buildEnvSourceCommand()
	if !strings.Contains(cmd, "agent-deck: warning: env_file not found:") {
		t.Errorf("missing configured env_file must warn in the pane instead of silently skipping:\n%s", cmd)
	}

	// Create the file → warning disappears, source remains.
	envPath := filepath.Join(tmpHome, ".agent-deck", "groups", "missing.env")
	if err := os.MkdirAll(filepath.Dir(envPath), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(envPath, []byte("export X=1\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	cmd = inst.buildEnvSourceCommand()
	if strings.Contains(cmd, "agent-deck: warning: env_file not found:") {
		t.Errorf("warning must clear once the env_file exists:\n%s", cmd)
	}
	if !strings.Contains(cmd, `source "`+envPath+`"`) {
		t.Errorf("env_file must still be sourced:\n%s", cmd)
	}
}

// bumpMtime advances a file's mtime explicitly — mtime-based cache
// invalidation needs the new timestamp to differ even on coarse-granularity
// filesystems.
func bumpMtime(t *testing.T, path string) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	newTime := st.ModTime().Add(2 * time.Second)
	if err := os.Chtimes(path, newTime, newTime); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
}
