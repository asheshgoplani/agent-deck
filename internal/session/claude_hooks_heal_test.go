package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Review round 2 (P1-A/P1-B). The first cut pinned the symlink-RESOLVED
// executable, which on a Homebrew install is the versioned Cellar keg that
// the next upgrade deletes: every hook entry then names a missing file and
// nothing repaired it. The install now pins the stable symlink the operator
// invokes, and HealClaudeHooks (daemon start, `hooks status`) rewrites an
// entry whose program is gone, whose binary reports another version, or
// whose Stop row lacks the sync marker.

// cellarFixture lays out <root>/Cellar/agent-deck/<version>/bin/agent-deck
// (a script reporting version) and the stable symlink <root>/bin/agent-deck
// pointing at it. It returns the keg path and the symlink path.
func cellarFixture(t *testing.T, root, version string) (keg, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script binaries and symlinks")
	}
	keg = filepath.Join(root, "Cellar", "agent-deck", version, "bin", "agent-deck")
	if err := os.MkdirAll(filepath.Dir(keg), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keg, []byte("#!/bin/sh\necho 'Agent Deck v"+version+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link = filepath.Join(root, "bin", "agent-deck")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(link)
	if err := os.Symlink(keg, link); err != nil {
		t.Fatal(err)
	}
	return keg, link
}

// realPath is filepath.EvalSymlinks with the test's failure handling.
func realPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// setHookExecutable points the install seam at path for the test.
func setHookExecutable(t *testing.T, path string) {
	t.Helper()
	prev := hookExecutablePath
	hookExecutablePath = func() (string, error) { return path, nil }
	t.Cleanup(func() { hookExecutablePath = prev })
}

func TestStableHookExecutablePath_NeverPinsCellarKeg(t *testing.T) {
	root := realPath(t, t.TempDir())
	keg, link := cellarFixture(t, root, "1.0.0")

	// macOS: os.Executable() is the invoked symlink. It is stable: keep it.
	if got, err := stableHookExecutablePath(link, ""); err != nil || got != link {
		t.Fatalf("invoked symlink must be pinned as-is: got %q err=%v", got, err)
	}
	// Linux: os.Executable() is the resolved keg. A PATH entry that resolves
	// to the same file is preferred over the keg.
	if got, err := stableHookExecutablePath(keg, filepath.Dir(link)); err != nil || got != link {
		t.Fatalf("PATH symlink to the running keg must be pinned instead of the keg: got %q err=%v", got, err)
	}
	// ~/.local/bin style: a second symlink elsewhere on PATH also qualifies.
	local := filepath.Join(root, "home", ".local", "bin", "agent-deck")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link, local); err != nil {
		t.Fatal(err)
	}
	if got, _ := stableHookExecutablePath(keg, filepath.Dir(local)); got != local {
		t.Fatalf("~/.local/bin symlink must be pinned: got %q", got)
	}
	// A PATH entry that resolves to a DIFFERENT file is never chosen.
	otherDir := filepath.Join(root, "other")
	if err := os.MkdirAll(otherDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(otherDir, "agent-deck"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, _ := stableHookExecutablePath(keg, otherDir); got != keg {
		t.Fatalf("with no stable path resolving to the binary the keg itself is the fallback: got %q", got)
	}
	if !isVersionedInstallPath(keg) || isVersionedInstallPath(link) {
		t.Fatalf("Cellar detection: keg=%v link=%v", isVersionedInstallPath(keg), isVersionedInstallPath(link))
	}
}

// The upgrade scenario end to end: hooks pinned to the 1.0.0 keg, the
// upgrade replaces it with 1.1.0 (old keg dir gone, symlink retargeted), the
// daemon starts on the new binary and heals the install.
func TestHealClaudeHooks_RewritesDanglingKegAfterUpgrade(t *testing.T) {
	root := realPath(t, t.TempDir())
	oldKeg, link := cellarFixture(t, root, "1.0.0")
	configDir := t.TempDir()

	// The previous release pinned the keg.
	setHookExecutable(t, oldKeg)
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	for event, cmds := range readHookCommands(t, configDir) {
		if !strings.Contains(cmds[0], oldKeg) {
			t.Fatalf("fixture: %s must be pinned to the keg, got %q", event, cmds)
		}
	}
	if !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("fixture: keg-pinned install must count as installed while the keg exists")
	}

	// brew upgrade: the old keg directory is removed, the symlink retargeted.
	newKeg, _ := cellarFixture(t, root, "1.1.0")
	if err := os.RemoveAll(filepath.Join(root, "Cellar", "agent-deck", "1.0.0")); err != nil {
		t.Fatal(err)
	}
	if realPath(t, link) != newKeg {
		t.Fatal("fixture: symlink must now resolve to the new keg")
	}
	if CheckClaudeHooksInstalled(configDir) {
		t.Fatal("a dangling absolute entry must NOT count as installed (the TUI accepted-reinstall path relies on this)")
	}

	// The new daemon pins the stable symlink and heals on start.
	setHookExecutable(t, link)
	res, err := HealClaudeHooks(configDir, "1.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healed || len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "no longer exists") {
		t.Fatalf("heal must rewrite a dangling install: %+v", res)
	}
	cmds := readHookCommands(t, configDir)
	for event, list := range cmds {
		for _, c := range list {
			if !strings.Contains(c, link+" hook-handler") || strings.Contains(c, "Cellar") {
				t.Errorf("%s: after heal the hook must run the stable symlink, got %q", event, c)
			}
		}
	}
	if want := StopHookSyncMarkerEnv + "=1 " + link + " hook-handler"; cmds["Stop"][0] != want {
		t.Fatalf("Stop after heal = %q, want %q", cmds["Stop"][0], want)
	}
	if !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("healed install must count as installed")
	}
	st := ClaudeHooksStatus(configDir, "1.1.0")
	if !st.Installed || len(st.Problems()) != 0 {
		t.Fatalf("status after heal must be clean: %+v problems=%v", st, st.Problems())
	}

	// Idempotent: nothing left to heal.
	again, err := HealClaudeHooks(configDir, "1.1.0")
	if err != nil || again.Healed || len(again.Reasons) != 0 {
		t.Fatalf("second heal must be a no-op: %+v err=%v", again, err)
	}
}

func TestHealClaudeHooks_RewritesVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script binaries")
	}
	// The hook entry runs a stale binary that still exists (a stale PATH
	// shadow or an old copy), reporting an older version than the daemon.
	staleDir := t.TempDir()
	stale := filepath.Join(staleDir, "agent-deck")
	if err := os.WriteFile(stale, []byte("#!/bin/sh\necho 'Agent Deck v1.0.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	stale = realPath(t, stale)
	exe := stubHookBinary(t, t.TempDir()) // reports v9.9.9
	configDir := t.TempDir()
	settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + StopHookSyncMarkerEnv + `=1 ` + stale + ` hook-handler"}]}]}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healed || len(res.Reasons) == 0 || !strings.Contains(res.Reasons[0], "v1.0.0") {
		t.Fatalf("a hook binary on another version must be healed: %+v", res)
	}
	if got := readHookCommands(t, configDir)["Stop"]; len(got) != 1 || got[0] != StopHookSyncMarkerEnv+"=1 "+exe+" hook-handler" {
		t.Fatalf("Stop after heal = %q", got)
	}

	// Same file, same version: no heal, even through a different path.
	if again, err := HealClaudeHooks(configDir, "9.9.9"); err != nil || again.Healed {
		t.Fatalf("a healthy install must not be rewritten: %+v err=%v", again, err)
	}
}

// P1-B: a Stop entry written by the release before the marker existed is
// sync but marker-less. It must count as drift for the unpinned check (so the
// TUI's accepted-reinstall path repairs it) and the heal must add the marker.
func TestHealClaudeHooks_AddsStopMarkerToPreMarkerInstall(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	// Strip the marker the way the previous release's settings.json looked.
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	stripped := strings.ReplaceAll(string(data), StopHookSyncMarkerEnv+"=1 ", "")
	if stripped == string(data) {
		t.Fatal("fixture: marker not found in the fresh install")
	}
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	if CheckClaudeHooksInstalled(configDir) {
		t.Fatal("a marker-less Stop entry must count as drift for the unpinned check")
	}
	if ClaudeHooksStatus(configDir, "9.9.9").Installed {
		t.Fatal("status must not report a marker-less install as cleanly installed")
	}

	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Healed || len(res.Reasons) != 1 || !strings.Contains(res.Reasons[0], "Stop sync marker") {
		t.Fatalf("heal must add the marker: %+v", res)
	}
	if got := readHookCommands(t, configDir)["Stop"]; len(got) != 1 || got[0] != StopHookSyncMarkerEnv+"=1 "+exe+" hook-handler" {
		t.Fatalf("Stop after heal = %q", got)
	}
	if !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("healed install must count as installed")
	}
}

func TestHealClaudeHooks_NeverInstallsFresh(t *testing.T) {
	stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil || res.Healed || len(res.Reasons) != 0 {
		t.Fatalf("heal must not install where nothing is installed: %+v err=%v", res, err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "settings.json")); !os.IsNotExist(err) {
		t.Fatal("heal must not create settings.json")
	}
	// A user's own hooks with no agent-deck entry are left alone too.
	user := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo mine"}]}]}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(user), 0o644); err != nil {
		t.Fatal(err)
	}
	if res, err := HealClaudeHooks(configDir, "9.9.9"); err != nil || res.Healed {
		t.Fatalf("heal must not touch a settings.json without agent-deck hooks: %+v err=%v", res, err)
	}
	if data, _ := os.ReadFile(filepath.Join(configDir, "settings.json")); string(data) != user {
		t.Fatalf("settings.json changed: %s", data)
	}
}

func TestHookCommandHasEnv(t *testing.T) {
	cases := []struct {
		command, env string
		want         bool
	}{
		{"agent-deck hook-handler", "", true},
		{"agent-deck hook-handler", "X=1", false},
		{"X=1 agent-deck hook-handler", "X=1", true},
		{"Y=2 X=1 /opt/bin/agent-deck hook-handler", "X=1", true},
		{"X=0 agent-deck hook-handler", "X=1", false},
		{"/opt/x=1/agent-deck hook-handler", "X=1", false},
	}
	for _, c := range cases {
		if got := hookCommandHasEnv(c.command, c.env); got != c.want {
			t.Errorf("hookCommandHasEnv(%q, %q) = %v, want %v", c.command, c.env, got, c.want)
		}
	}
}
