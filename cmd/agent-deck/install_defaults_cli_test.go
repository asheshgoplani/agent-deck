package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// installDefaultsCLIHome is a throwaway HOME for the real binary, with the
// XDG dirs under it in this process too.
func installDefaultsCLIHome(t *testing.T) (string, []string) {
	t.Helper()
	home := t.TempDir()
	env := []string{
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_STATE_HOME=" + filepath.Join(home, ".local", "state"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"AGENTDECK_PROFILE=",
		"AGENTDECK_EVENTS_BUS=off",
	}
	t.Setenv("HOME", home)
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, v)
	}
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)
	return home, env
}

// The update path: an existing install (config.toml without the new keys)
// driven by non-interactive commands, which is what daemons, hooks, scripts
// and the auto-update restart are, keeps today's values and its config.toml
// is not touched.
func TestInstallDefaultsCLI_ExistingInstallNonInteractiveUnchanged(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, env := installDefaultsCLIHome(t)
	cfgPath := filepath.Join(home, ".config", "agent-deck", "config.toml")
	const original = "# hand written\ntheme = \"dark\"\n"
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "-t", "s1", "-c", "shell", "--no-parent", "--json", project},
		{"list", "--json"},
	} {
		if _, stderr, code := runAgentDeckEnv(t, home, "", env, args...); code != 0 {
			t.Fatalf("agent-deck %v: exit %d: %s", args, code, stderr)
		}
	}
	for _, key := range []string{"claude.dangerous_mode"} {
		out, stderr, code := runAgentDeckEnv(t, home, "", env, "config", "get", key)
		if code != 0 || strings.TrimSpace(out) != "true" {
			t.Errorf("config get %s = %q (exit %d, %s), want true (today's value)", key, out, code, stderr)
		}
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil || string(got) != original {
		t.Errorf("non-interactive commands changed config.toml (err=%v):\n%s", err, got)
	}
	if session.InstallKind() != session.InstallKindExisting {
		t.Errorf("InstallKind() = %q, want existing", session.InstallKind())
	}
}

// A fresh install driven from the CLI first (no config.toml) gets the safe
// values, and stays new once its first state.db exists.
func TestInstallDefaultsCLI_FreshInstallGetsSafeValues(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, env := installDefaultsCLIHome(t)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeckEnv(t, home, "", env, "add", "-t", "s1", "-c", "shell", "--no-parent", "--json", project); code != 0 {
		t.Fatalf("add: exit %d: %s", code, stderr)
	}
	out, stderr, code := runAgentDeckEnv(t, home, "", env, "config", "get", "claude.dangerous_mode")
	if code != 0 || strings.TrimSpace(out) != "false" {
		t.Errorf("config get claude.dangerous_mode = %q (exit %d, %s), want false", out, code, stderr)
	}
	if session.InstallKind() != session.InstallKindNew {
		t.Errorf("InstallKind() = %q, want new", session.InstallKind())
	}
}

// add --trust-folder puts the folder into [security] trusted_folders.
func TestInstallDefaultsCLI_AddTrustFolder(t *testing.T) {
	if testing.Short() {
		t.Skip("subprocess CLI test skipped in short mode")
	}
	home, env := installDefaultsCLIHome(t)
	project := filepath.Join(home, "proj")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, stderr, code := runAgentDeckEnv(t, home, "", env, "add", "--trust-folder", "-t", "s1", "-c", "shell", "--no-parent", "--json", project); code != 0 {
		t.Fatalf("add --trust-folder: exit %d: %s", code, stderr)
	}
	session.ClearUserConfigCache()
	cfg, err := session.LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(project)
	found := false
	for _, f := range cfg.Security.TrustedFolders {
		if f == real || f == project {
			found = true
		}
	}
	if !found {
		t.Errorf("trusted_folders = %v, want %s", cfg.Security.TrustedFolders, project)
	}
}
