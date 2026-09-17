package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Messaging audit P1-1: settings.json used to carry the bare command
// `agent-deck hook-handler`, so every Claude session ran whatever `agent-deck`
// came first on ITS PATH. On the maintainer's machine that was a stale
// /usr/local/bin symlink eight releases behind the daemon, which silently
// disabled the Stop-hook drain, the sentinel scan and the events history.
// The install now writes the absolute, symlink-resolved path of the binary
// that performed the install, and `hooks status` reports a PATH shadow and a
// version mismatch instead of a bare "INSTALLED".

// stubHookBinary points the install/status seams at a fake agent-deck binary
// for the duration of the test and returns the fake's (symlink-resolved) path.
func stubHookBinary(t *testing.T, dir string) string {
	t.Helper()
	exe := filepath.Join(dir, "agent-deck")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\necho 'Agent Deck v9.9.9'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		t.Fatal(err)
	}
	prev := hookExecutablePath
	hookExecutablePath = func() (string, error) { return resolved, nil }
	t.Cleanup(func() { hookExecutablePath = prev })
	return resolved
}

// isBareHookForm reports whether c is the legacy PATH-resolved command, with
// or without a leading VAR=value marker.
func isBareHookForm(c string) bool {
	return c == agentDeckHookCommand || strings.HasSuffix(c, " "+agentDeckHookCommand)
}

func readHookCommands(t *testing.T, configDir string) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]claudeHookMatcher `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	out := map[string][]string{}
	for event, matchers := range settings.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				out[event] = append(out[event], h.Command)
			}
		}
	}
	return out
}

func TestInjectClaudeHooks_WritesAbsoluteBinaryPath(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	want := exe + " hook-handler"
	for event, cmds := range readHookCommands(t, configDir) {
		found := false
		for _, c := range cmds {
			if strings.HasSuffix(c, want) {
				found = true
			}
			if isBareHookForm(c) {
				t.Errorf("%s: bare command %q installed; want absolute %q", event, c, want)
			}
		}
		if !found {
			t.Errorf("%s: absolute hook command %q not installed (got %q)", event, want, cmds)
		}
	}
}

// Messaging audit P2-1: only the Stop entry carries the sync marker, and it
// is still recognised as ours (so uninstall / drift detection keep working).
func TestInjectClaudeHooks_StopCarriesSyncMarker(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	cmds := readHookCommands(t, configDir)
	if want := StopHookSyncMarkerEnv + "=1 " + exe + " hook-handler"; len(cmds["Stop"]) != 1 || cmds["Stop"][0] != want {
		t.Fatalf("Stop command = %q, want %q", cmds["Stop"], want)
	}
	for event, list := range cmds {
		if event == "Stop" {
			continue
		}
		for _, c := range list {
			if strings.Contains(c, StopHookSyncMarkerEnv) {
				t.Errorf("%s must not carry the Stop sync marker: %q", event, c)
			}
		}
	}
	if !isAgentDeckHookCommand(cmds["Stop"][0]) {
		t.Fatal("marker-prefixed Stop command must be recognised as an agent-deck hook")
	}
	// A legacy Stop entry without the marker is drift: install rewrites it.
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"` + exe + ` hook-handler","async":true}]}]}}`
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if installed, err := InjectClaudeHooks(other); err != nil || !installed {
		t.Fatalf("install must rewrite a marker-less Stop entry: installed=%v err=%v", installed, err)
	}
	if got := readHookCommands(t, other)["Stop"]; len(got) != 1 || !strings.HasPrefix(got[0], StopHookSyncMarkerEnv+"=1 ") {
		t.Fatalf("Stop entry after reinstall = %q", got)
	}
}

func TestInjectClaudeHooks_RewritesLegacyBareEntries(t *testing.T) {
	exe := stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"agent-deck hook-handler"}]}],` +
		`"SessionStart":[{"hooks":[{"type":"command","command":"agent-deck hook-handler","async":true},` +
		`{"type":"command","command":"echo user-hook"}]}]},"theme":"dark"}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	installed, err := InjectClaudeHooks(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if !installed {
		t.Fatal("install must rewrite a legacy bare entry, but reported already-installed")
	}
	cmds := readHookCommands(t, configDir)
	if got := strings.Join(cmds["SessionStart"], "|"); got != exe+" hook-handler|echo user-hook" {
		t.Fatalf("SessionStart commands = %q: bare entry must be rewritten in place, user hook kept", got)
	}
	if len(cmds["Stop"]) != 1 || cmds["Stop"][0] != StopHookSyncMarkerEnv+"=1 "+exe+" hook-handler" {
		t.Fatalf("Stop commands = %q: want exactly one absolute entry with the sync marker", cmds["Stop"])
	}

	// Idempotent: a second install on the same binary is a no-op.
	again, err := InjectClaudeHooks(configDir)
	if err != nil {
		t.Fatal(err)
	}
	if again {
		t.Fatal("second install must be a no-op")
	}
	if !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("CheckClaudeHooksInstalled must be true after install")
	}
}

func TestCheckClaudeHooksInstalled_AcceptsLegacyBareForm(t *testing.T) {
	// The TUI's startup check must keep treating a bare install as installed:
	// it silently re-installs on "missing", and a developer build must never
	// hijack the operator's hooks just by starting a TUI.
	stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	prev := hookExecutablePath
	hookExecutablePath = func() (string, error) { return "", os.ErrNotExist }
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	hookExecutablePath = prev
	for _, cmds := range readHookCommands(t, configDir) {
		for _, c := range cmds {
			if !isBareHookForm(c) {
				t.Fatalf("fallback install must write the bare form, got %q", c)
			}
		}
	}
	if !CheckClaudeHooksInstalled(configDir) {
		t.Fatal("bare form must still count as installed for the TUI check")
	}
}

func TestClaudeHooksStatus_DetectsPathShadowAndVersionMismatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH shadow test uses a shell script")
	}
	// The binary that "is" the daemon.
	exe := stubHookBinary(t, t.TempDir())
	// A different, older binary that wins on PATH.
	shadowDir := t.TempDir()
	shadow := filepath.Join(shadowDir, "agent-deck")
	if err := os.WriteFile(shadow, []byte("#!/bin/sh\necho 'Agent Deck v1.16.3 (update available: v1.16.10)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shadowDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	configDir := t.TempDir()
	legacy := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"agent-deck hook-handler"}]}]}}`
	if err := os.WriteFile(filepath.Join(configDir, "settings.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	st := ClaudeHooksStatus(configDir, "9.9.9")
	if st.Executable != exe {
		t.Fatalf("Executable = %q, want %q", st.Executable, exe)
	}
	if len(st.Binaries) != 1 {
		t.Fatalf("want one distinct hook command, got %+v", st.Binaries)
	}
	b := st.Binaries[0]
	if !b.Bare {
		t.Fatalf("bare command must be reported as bare: %+v", b)
	}
	wantShadow, _ := filepath.EvalSymlinks(shadow)
	if b.ResolvedPath != wantShadow {
		t.Fatalf("ResolvedPath = %q, want PATH winner %q", b.ResolvedPath, wantShadow)
	}
	if !b.Shadowed {
		t.Fatalf("bare command resolving to a different file than this binary must be flagged: %+v", b)
	}
	if b.Version != "1.16.3" || !b.VersionMismatch {
		t.Fatalf("hook binary version must be read and compared: %+v", b)
	}
	if st.Installed {
		t.Fatalf("a shadowed bare install must not be reported as cleanly installed: %+v", st)
	}

	// After install the command is absolute, resolves to this binary and the
	// version matches.
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	st = ClaudeHooksStatus(configDir, "9.9.9")
	if !st.Installed || len(st.Binaries) == 0 || len(st.Problems()) != 0 {
		t.Fatalf("after install: %+v problems=%v", st, st.Problems())
	}
	for _, b := range st.Binaries {
		if b.Bare || b.Shadowed || b.VersionMismatch || b.ResolvedPath != exe || b.Version != "9.9.9" {
			t.Fatalf("after install the hook must point at this binary: %+v", b)
		}
	}
}

func TestRemoveClaudeHooks_RemovesAbsoluteEntries(t *testing.T) {
	stubHookBinary(t, t.TempDir())
	configDir := t.TempDir()
	if _, err := InjectClaudeHooks(configDir); err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveClaudeHooks(configDir)
	if err != nil || !removed {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
	if CheckClaudeHooksInstalled(configDir) {
		t.Fatal("hooks must be gone after uninstall")
	}
}
