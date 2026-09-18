package session

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Review round 3 (finding 2): the heal pinned whatever binary ran it, dev
// builds included, so a `hooks status` or TUI start from a build under /tmp
// rewrote the operator's hooks to a path that vanished with the build. An
// unpinnable binary now never writes from the heal, and the explicit install
// keeps whatever program the entries already name.

// setUnpinnableHookExecutable makes the running binary count as a dev build.
func setUnpinnableHookExecutable(t *testing.T) {
	t.Helper()
	setHookExecutable(t, "")
}

func settingsMTime(t *testing.T, configDir string) int64 {
	t.Helper()
	info, err := os.Stat(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	return info.ModTime().UnixNano()
}

func TestHealClaudeHooks_UnpinnableDevBuildNeverWrites(t *testing.T) {
	// A healthy pinned install made by a stable binary...
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	// ...whose Stop marker is then stripped (drift the heal would repair)
	// and whose program is removed (dangling), the worst case.
	data, _ := os.ReadFile(filepath.Join(configDir, "settings.json"))
	stripped := strings.ReplaceAll(string(data), StopHookSyncMarkerEnv+"=1 ", "")
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	before := settingsMTime(t, configDir)

	setUnpinnableHookExecutable(t)
	res, err := HealClaudeHooks(configDir, "9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	if res.Healed || len(res.Reasons) != 0 || res.Skipped != unpinnableHookExecutableReason {
		t.Fatalf("a dev build must report, not heal: %+v", res)
	}
	after, _ := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if string(after) != stripped || settingsMTime(t, configDir) != before {
		t.Fatalf("a dev build wrote settings.json:\n%s", after)
	}
	if backups, _ := filepath.Glob(filepath.Join(configDir, healBackupPrefix+"*")); len(backups) != 0 {
		t.Fatalf("no backup without a heal, got %v", backups)
	}
	st := ClaudeHooksStatus(configDir, "9.9.9")
	if st.Executable != "" || !strings.HasPrefix(st.Unpinnable, unpinnableHookExecutableReason) {
		t.Fatalf("status must report the unpinnable build: %+v", st)
	}
}

// The explicit `hooks install` from a dev build repairs config drift but
// keeps the program the entries already pin; a fresh install writes the bare
// command; a dangling program falls back to the bare command.
func TestInjectClaudeHooks_UnpinnableKeepsExistingProgram(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(configDir, "settings.json"))
	stripped := strings.ReplaceAll(string(data), StopHookSyncMarkerEnv+"=1 ", "")
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}

	setUnpinnableHookExecutable(t)
	if CheckClaudeHooksInstalled(configDir) {
		t.Fatal("fixture: the marker-less Stop entry is drift")
	}
	installed, err := InjectClaudeHooks(configDir)
	if err != nil || !installed {
		t.Fatalf("install must repair the marker: %v %v", installed, err)
	}
	cmds := readHookCommands(t, configDir)
	if want := StopHookSyncMarkerEnv + "=1 " + exe + " hook-handler"; cmds["Stop"][0] != want {
		t.Fatalf("Stop = %q, want the marker with the EXISTING program %q", cmds["Stop"][0], want)
	}
	for event, list := range cmds {
		for _, c := range list {
			if !strings.Contains(c, exe+" hook-handler") {
				t.Errorf("%s: dev build must keep the pinned program, got %q", event, c)
			}
		}
	}
	if !CheckClaudeHooksInstalled(configDir) || !ClaudeHooksStatus(configDir, "9.9.9").Installed {
		t.Fatal("the repaired install must count as installed for the dev build too")
	}
	if again, err := InjectClaudeHooks(configDir); err != nil || again {
		t.Fatalf("idempotent: %v %v", again, err)
	}

	// Fresh install from a dev build: bare command.
	fresh := t.TempDir()
	if _, err := InjectClaudeHooks(fresh); err != nil {
		t.Fatal(err)
	}
	for event, list := range readHookCommands(t, fresh) {
		if !isBareHookForm(list[0]) {
			t.Errorf("%s: fresh install from a dev build must be bare, got %q", event, list[0])
		}
	}

	// Dangling program: an explicit install from a dev build falls back to
	// the bare command rather than keeping a path that no longer exists.
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if installed, err := InjectClaudeHooks(configDir); err != nil || !installed {
		t.Fatalf("dangling program must be rewritten: %v %v", installed, err)
	}
	for event, list := range readHookCommands(t, configDir) {
		if !isBareHookForm(list[0]) {
			t.Errorf("%s: dangling pin must fall back to bare, got %q", event, list[0])
		}
	}
}

// An OLDER stable binary must never "heal" an install pinned to a newer one
// back to itself (the reverse ping-pong).
func TestHealClaudeHooks_OlderBinaryNeverRewritesNewerPin(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script binaries")
	}
	newerDir := t.TempDir()
	newer := filepath.Join(newerDir, "agent-deck")
	if err := os.WriteFile(newer, []byte("#!/bin/sh\necho 'Agent Deck v2.0.0'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	newer = realPath(t, newer)
	configDir := t.TempDir()
	settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + StopHookSyncMarkerEnv + `=1 ` + newer + ` hook-handler"}]}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	stubHookBinary(t, t.TempDir()) // this process: a different, older binary
	res, err := HealClaudeHooks(configDir, "1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if res.Healed || len(res.Reasons) != 0 || !strings.Contains(res.Skipped, "newer than this binary") {
		t.Fatalf("older binary must skip: %+v", res)
	}
	if after, _ := os.ReadFile(filepath.Join(configDir, "settings.json")); string(after) != settings {
		t.Fatalf("older binary rewrote settings.json:\n%s", after)
	}
}

// Review round 3 (finding 4): a CLI-installed entry counts as present in
// every recognised form (bare, pinned, marker-less, dangling), so the TUI
// never re-prompts for it; only an absent or partial install is not present.
func TestCheckClaudeHooksPresent_RecognisesEveryInstalledForm(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	if !CheckClaudeHooksPresent(configDir) || !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("fresh install must be present and installed")
	}
	data, _ := os.ReadFile(filepath.Join(configDir, "settings.json"))
	// Marker-less (previous release), still present.
	stripped := strings.ReplaceAll(string(data), StopHookSyncMarkerEnv+"=1 ", "")
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CheckClaudeHooksPresent(configDir) || CheckClaudeHooksInstalled(configDir) {
		t.Fatal("marker-less install must be present but not installed (drift)")
	}
	// Bare legacy form, present.
	bare := strings.ReplaceAll(stripped, exe+" hook-handler", agentDeckHookCommand)
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(bare), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CheckClaudeHooksPresent(configDir) {
		t.Fatal("bare install must be present")
	}
	// Dangling pin, present (the heal repairs it, nobody is asked).
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(stripped), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(exe); err != nil {
		t.Fatal(err)
	}
	if !CheckClaudeHooksPresent(configDir) || CheckClaudeHooksInstalled(configDir) {
		t.Fatal("dangling install must be present but not installed")
	}
	// One event missing: not present (a partial install is prompted for).
	if _, err := RemoveClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	if CheckClaudeHooksPresent(configDir) {
		t.Fatal("removed install must not be present")
	}
	if CheckClaudeHooksPresent(t.TempDir()) {
		t.Fatal("no settings.json must not be present")
	}
}
