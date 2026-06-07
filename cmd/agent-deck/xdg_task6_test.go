package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

func setupTask6XDGEnv(t *testing.T) (home, xdgConfigHome, xdgDataHome, xdgCacheHome string) {
	t.Helper()

	root := t.TempDir()
	home = filepath.Join(root, "home")
	xdgConfigHome = filepath.Join(root, "xdg-config")
	xdgDataHome = filepath.Join(root, "xdg-data")
	xdgCacheHome = filepath.Join(root, "xdg-cache")

	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	pathDir := filepath.Join(root, "bin")
	if err := os.MkdirAll(pathDir, 0o755); err != nil {
		t.Fatalf("mkdir path dir: %v", err)
	}

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)
	t.Setenv("XDG_DATA_HOME", xdgDataHome)
	t.Setenv("XDG_CACHE_HOME", xdgCacheHome)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("PATH", pathDir)
	session.ClearUserConfigCache()
	t.Cleanup(session.ClearUserConfigCache)

	return home, xdgConfigHome, xdgDataHome, xdgCacheHome
}

func captureStdoutForTask6(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	t.Cleanup(func() { os.Stdout = origStdout })

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout writer: %v", err)
	}
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		t.Fatalf("read stdout: %v", err)
	}
	return buf.String()
}

func TestXDGTask6_HookStatusUsesXDGDataAndLegacyFallback(t *testing.T) {
	home, _, xdgDataHome, _ := setupTask6XDGEnv(t)

	wantXDG := filepath.Join(xdgDataHome, "agent-deck", "hooks")
	if got := getHooksDir(); got != wantXDG {
		t.Fatalf("getHooksDir() = %q, want %q", got, wantXDG)
	}

	writeHookStatus("xdg-hook", "waiting", "sess-xdg", "SessionStart")
	if _, err := os.Stat(filepath.Join(wantXDG, "xdg-hook.json")); err != nil {
		t.Fatalf("status file should be written under XDG data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".agent-deck", "hooks", "xdg-hook.json")); !os.IsNotExist(err) {
		t.Fatalf("status file should not be written under legacy hooks for new XDG user, stat err=%v", err)
	}

	legacyHooks := filepath.Join(home, ".agent-deck", "hooks")
	if err := os.MkdirAll(legacyHooks, 0o700); err != nil {
		t.Fatalf("mkdir legacy hooks: %v", err)
	}
	if got := getHooksDir(); got != wantXDG {
		t.Fatalf("XDG hooks should continue to win after XDG marker exists: got %q want %q", got, wantXDG)
	}
}

func TestXDGTask6_HookStatusUsesLegacyHooksWhenOnlyLegacyExists(t *testing.T) {
	home, _, _, _ := setupTask6XDGEnv(t)
	legacyHooks := filepath.Join(home, ".agent-deck", "hooks")
	if err := os.MkdirAll(legacyHooks, 0o700); err != nil {
		t.Fatalf("mkdir legacy hooks: %v", err)
	}

	if got := getHooksDir(); got != legacyHooks {
		t.Fatalf("getHooksDir() = %q, want legacy %q", got, legacyHooks)
	}
}

func TestXDGTask6_CostEventsUseXDGDataAndLegacyFallback(t *testing.T) {
	home, _, _, _ := setupTask6XDGEnv(t)
	legacyCostEvents := filepath.Join(home, ".agent-deck", "cost-events")
	if err := os.MkdirAll(legacyCostEvents, 0o700); err != nil {
		t.Fatalf("mkdir legacy cost-events: %v", err)
	}

	if got := getCostEventsDir(); got != legacyCostEvents {
		t.Fatalf("getCostEventsDir() = %q, want legacy %q", got, legacyCostEvents)
	}

	_, _, xdgDataHome, _ := setupTask6XDGEnv(t)
	wantXDG := filepath.Join(xdgDataHome, "agent-deck", "cost-events")
	if got := getCostEventsDir(); got != wantXDG {
		t.Fatalf("getCostEventsDir() = %q, want XDG %q", got, wantXDG)
	}
}

func TestXDGTask6_CostDebugUsesXDGCache(t *testing.T) {
	home, _, _, xdgCacheHome := setupTask6XDGEnv(t)
	t.Setenv("AGENTDECK_DEBUG", "1")
	legacyPath := filepath.Join(home, ".agent-deck", "cost-debug.log")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy cache debug dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy\n"), 0o600); err != nil {
		t.Fatalf("write legacy cost debug log: %v", err)
	}

	logCostDebug("cache path probe")

	want := filepath.Join(xdgCacheHome, "agent-deck", "cost-debug.log")
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("cost debug log should be written under XDG cache: %v", err)
	}
	if !strings.Contains(string(data), "cache path probe") {
		t.Fatalf("cost debug log missing message, got %q", string(data))
	}
	legacyData, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy cost debug log: %v", err)
	}
	if strings.Contains(string(legacyData), "cache path probe") {
		t.Fatalf("cost debug should not append to legacy cache log, got %q", string(legacyData))
	}
}

func TestXDGTask6_PricingCacheUsesXDGCacheDespiteLegacyFile(t *testing.T) {
	home, _, _, xdgCacheHome := setupTask6XDGEnv(t)
	legacyPath := filepath.Join(home, ".agent-deck", "pricing.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy pricing cache dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("write legacy pricing cache: %v", err)
	}

	got, err := effectiveCacheDir()
	if err != nil {
		t.Fatalf("effectiveCacheDir: %v", err)
	}
	want := filepath.Join(xdgCacheHome, "agent-deck")
	if got != want {
		t.Fatalf("pricing cache dir = %q, want XDG cache dir %q", got, want)
	}
	if filepath.Join(got, "pricing.json") == legacyPath {
		t.Fatalf("pricing cache should not use legacy file %q", legacyPath)
	}
}

func TestXDGTask6_DebugDumpUsesXDGCacheDespiteLegacyLog(t *testing.T) {
	home, _, _, xdgCacheHome := setupTask6XDGEnv(t)
	legacyPath := filepath.Join(home, ".agent-deck", "debug.log")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o700); err != nil {
		t.Fatalf("mkdir legacy debug dir: %v", err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy\n"), 0o600); err != nil {
		t.Fatalf("write legacy debug log: %v", err)
	}

	got, err := effectiveCacheDir()
	if err != nil {
		t.Fatalf("effectiveCacheDir: %v", err)
	}
	want := filepath.Join(xdgCacheHome, "agent-deck")
	if got != want {
		t.Fatalf("debug cache dir = %q, want %q", got, want)
	}
	if filepath.Join(got, "debug.log") == legacyPath {
		t.Fatalf("debug log should not use legacy path %q", legacyPath)
	}
	if filepath.Dir(filepath.Join(got, "debug-dump-123.jsonl")) != want {
		t.Fatalf("debug dump should be written under %q", want)
	}
	if filepath.Dir(filepath.Join(got, "crash-dump-123.jsonl")) != want {
		t.Fatalf("crash dump should be written under %q", want)
	}
}

func TestXDGTask6_DebugDumpCommandWritesToXDGCache(t *testing.T) {
	_, _, _, xdgCacheHome := setupTask6XDGEnv(t)

	out := captureStdoutForTask6(t, handleDebugDump)
	wantDir := filepath.Join(xdgCacheHome, "agent-deck")
	if !strings.Contains(out, wantDir) {
		t.Fatalf("debug-dump output should mention XDG cache dir %q, got:\n%s", wantDir, out)
	}

	matches, err := filepath.Glob(filepath.Join(wantDir, "debug-dump-*.jsonl"))
	if err != nil {
		t.Fatalf("glob debug dumps: %v", err)
	}
	if len(matches) == 0 {
		t.Fatalf("expected debug dump under %s", wantDir)
	}
}

func TestXDGTask6_WatcherInstallSkillUsesConfiguredPoolPath(t *testing.T) {
	_, xdgConfigHome, _, _ := setupTask6XDGEnv(t)

	if err := handleWatcherInstallSkill("default", []string{"watcher-creator"}); err != nil {
		t.Fatalf("handleWatcherInstallSkill: %v", err)
	}

	want := filepath.Join(xdgConfigHome, "agent-deck", "skills", "pool", "watcher-creator", "SKILL.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("watcher skill should be installed under configured pool path: %v", err)
	}
}

func TestXDGTask6_PluginHelpAndErrorsUseEffectiveConfigPath(t *testing.T) {
	_, xdgConfigHome, _, _ := setupTask6XDGEnv(t)
	wantConfig := filepath.Join(xdgConfigHome, "agent-deck", "config.toml")

	helpOut := captureStdoutForTask6(t, printPluginHelp)
	if !strings.Contains(helpOut, wantConfig) {
		t.Fatalf("plugin help should mention effective config path %q, got:\n%s", wantConfig, helpOut)
	}
	if strings.Contains(helpOut, "~/.agent-deck/config.toml") {
		t.Fatalf("plugin help should not hard-code legacy config path, got:\n%s", helpOut)
	}

	err := validatePluginFlags([]string{"octopus"})
	if err == nil {
		t.Fatal("expected empty catalog error")
	}
	if !strings.Contains(err.Error(), wantConfig) {
		t.Fatalf("plugin empty catalog error should mention %q, got %v", wantConfig, err)
	}
}

func TestXDGTask6_TryAndSessionHelpUseEffectiveConfigPath(t *testing.T) {
	_, xdgConfigHome, _, _ := setupTask6XDGEnv(t)
	wantConfig := filepath.Join(xdgConfigHome, "agent-deck", "config.toml")

	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "try", args: []string{"try-help"}},
		{name: "session set", args: []string{"session-set-help"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := runTask6HelperProcess(t, tc.args...)
			if !strings.Contains(out, wantConfig) {
				t.Fatalf("%s help should mention effective config path %q, got:\n%s", tc.name, wantConfig, out)
			}
			if strings.Contains(out, "~/.agent-deck/config.toml") {
				t.Fatalf("%s help should not hard-code legacy config path, got:\n%s", tc.name, out)
			}
		})
	}
}

func runTask6HelperProcess(t *testing.T, args ...string) string {
	t.Helper()

	cmd := exec.Command(os.Args[0], append([]string{"-test.run=TestXDGTask6HelperProcess", "--"}, args...)...)
	cmd.Env = append(os.Environ(), "AGENT_DECK_TASK6_HELPER_PROCESS=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper process failed: %v\n%s", err, string(out))
	}
	return string(out)
}

func TestXDGTask6HelperProcess(t *testing.T) {
	if os.Getenv("AGENT_DECK_TASK6_HELPER_PROCESS") != "1" {
		return
	}
	args := os.Args
	for len(args) > 0 && args[0] != "--" {
		args = args[1:]
	}
	if len(args) > 0 {
		args = args[1:]
	}
	if len(args) != 1 {
		os.Exit(2)
	}
	switch args[0] {
	case "try-help":
		handleTry("default", []string{"--help"})
	case "session-set-help":
		handleSessionSet("default", []string{"--help"})
	case "uninstall-no-home":
		// handleUninstall must abort with os.Exit(1) before touching any
		// path when the home directory cannot be resolved. Caller clears
		// HOME so os.UserHomeDir() fails on Linux.
		handleUninstall([]string{"-y", "--keep-tmux-config"})
	default:
		os.Exit(2)
	}
	os.Exit(0)
}

// TestXDGTask6_UninstallAbortsWhenHomeUnresolvable is the regression test for
// the remaining P2 (R3 #1294): handleUninstall previously did
// `homeDir, _ := os.UserHomeDir()`, swallowing the error. With HOME unset and
// resolution failing, homeDir became "" and every collected path degraded to
// cwd-relative junk (".tmux.conf", binary/data paths under cwd), so backups and
// removals targeted the wrong files. The fix aborts early with a clear error
// and a non-zero exit before collecting or removing anything.
func TestXDGTask6_UninstallAbortsWhenHomeUnresolvable(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestXDGTask6HelperProcess", "--", "uninstall-no-home")
	// Drop HOME (and XDG/USERPROFILE) so os.UserHomeDir() fails on Linux.
	env := []string{"AGENT_DECK_TASK6_HELPER_PROCESS=1"}
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "HOME=") ||
			strings.HasPrefix(e, "USERPROFILE=") ||
			strings.HasPrefix(e, "XDG_CONFIG_HOME=") ||
			strings.HasPrefix(e, "XDG_DATA_HOME=") ||
			strings.HasPrefix(e, "XDG_CACHE_HOME=") {
			continue
		}
		env = append(env, e)
	}
	cmd.Env = env

	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected uninstall to abort with non-zero exit when home is unresolvable; got success:\n%s", out)
	}
	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected *exec.ExitError, got %T: %v\n%s", err, err, out)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d:\n%s", exitErr.ExitCode(), out)
	}
	if !strings.Contains(string(out), "cannot resolve home directory") {
		t.Fatalf("expected clear home-resolution error message; got:\n%s", out)
	}
	// The abort must happen before the uninstaller does any work, so the
	// "Found:" / "will be removed" collection output must be absent.
	if strings.Contains(string(out), "The following will be removed") {
		t.Fatalf("uninstall collected/removed items despite unresolvable home:\n%s", out)
	}
}

func TestXDGTask6_UninstallDryRunListsExistingXDGAndLegacyLocations(t *testing.T) {
	home, xdgConfigHome, xdgDataHome, xdgCacheHome := setupTask6XDGEnv(t)
	for _, dir := range []string{
		filepath.Join(xdgConfigHome, "agent-deck"),
		filepath.Join(xdgDataHome, "agent-deck"),
		filepath.Join(xdgCacheHome, "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	out := captureStdoutForTask6(t, func() {
		handleUninstall([]string{"--dry-run", "--keep-tmux-config"})
	})

	for _, want := range []string{
		"Config directory",
		filepath.Join(xdgConfigHome, "agent-deck"),
		"Data directory",
		filepath.Join(xdgDataHome, "agent-deck"),
		"Cache directory",
		filepath.Join(xdgCacheHome, "agent-deck"),
		"Legacy directory",
		filepath.Join(home, ".agent-deck"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("dry-run output missing %q:\n%s", want, out)
		}
	}
}

func TestXDGTask6_UninstallKeepDataPreservesAllLocations(t *testing.T) {
	home, xdgConfigHome, xdgDataHome, xdgCacheHome := setupTask6XDGEnv(t)
	locations := []string{
		filepath.Join(xdgConfigHome, "agent-deck"),
		filepath.Join(xdgDataHome, "agent-deck"),
		filepath.Join(xdgCacheHome, "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	}
	for _, dir := range locations {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	_ = captureStdoutForTask6(t, func() {
		handleUninstall([]string{"-y", "--keep-data", "--keep-tmux-config"})
	})

	for _, dir := range locations {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("--keep-data should preserve %s: %v", dir, err)
		}
	}
}

func TestXDGTask6_UninstallRemovesXDGAndLegacyLocations(t *testing.T) {
	home, xdgConfigHome, xdgDataHome, xdgCacheHome := setupTask6XDGEnv(t)
	locations := []string{
		filepath.Join(xdgConfigHome, "agent-deck"),
		filepath.Join(xdgDataHome, "agent-deck"),
		filepath.Join(xdgCacheHome, "agent-deck"),
		filepath.Join(home, ".agent-deck"),
	}
	for _, dir := range locations {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	_ = captureStdoutForTask6(t, func() {
		handleUninstall([]string{"-y", "--keep-tmux-config"})
	})

	for _, dir := range locations {
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("uninstall should remove %s, stat err=%v", dir, err)
		}
	}
}

func TestXDGTask6_UninstallRemovesXDGCacheSymlinkWithoutFollowingEscape(t *testing.T) {
	home, _, _, xdgCacheHome := setupTask6XDGEnv(t)

	if err := os.MkdirAll(filepath.Join(home, ".agent-deck"), 0o700); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside-cache")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	marker := filepath.Join(outside, "marker.txt")
	if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}
	cacheLink := filepath.Join(xdgCacheHome, "agent-deck")
	if err := os.MkdirAll(filepath.Dir(cacheLink), 0o700); err != nil {
		t.Fatalf("mkdir xdg cache home: %v", err)
	}
	if err := os.Symlink(outside, cacheLink); err != nil {
		t.Fatalf("symlink cache: %v", err)
	}

	_ = captureStdoutForTask6(t, func() {
		handleUninstall([]string{"-y", "--keep-tmux-config"})
	})

	if _, err := os.Lstat(cacheLink); !os.IsNotExist(err) {
		t.Fatalf("cache symlink should be removed without following target, lstat err=%v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("symlink target marker should remain: %v", err)
	}
}
