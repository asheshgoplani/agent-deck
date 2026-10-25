package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newInstallWithConfig makes a new install (the config is written by this
// release, so the install is recorded new) with the given settings.
func newInstallWithConfig(t *testing.T, cfg *UserConfig) string {
	t.Helper()
	home := installKindHome(t)
	if cfg == nil {
		cfg = &UserConfig{Theme: "dark"}
	}
	if err := SaveUserConfig(cfg); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	ClearUserConfigCache()
	return home
}

func projectDir(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestFolderTrustPolicy_LaunchTrust(t *testing.T) {
	yes := true
	cases := []struct {
		name  string
		setup func(t *testing.T) (home, dir string, inst *Instance)
		want  bool
	}{
		{"new install, plain folder: not pre-trusted", func(t *testing.T) (string, string, *Instance) {
			home := newInstallWithConfig(t, nil)
			dir := projectDir(t, home)
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, false},
		{"new install, folder under trusted_folders", func(t *testing.T) (string, string, *Instance) {
			home := installKindHome(t)
			dir := projectDir(t, home)
			if err := SaveUserConfig(&UserConfig{Security: SecuritySettings{TrustedFolders: []string{filepath.Join(home, "code")}}}); err != nil {
				t.Fatal(err)
			}
			ClearUserConfigCache()
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, true},
		{"new install, trusted_folders with ~", func(t *testing.T) (string, string, *Instance) {
			home := installKindHome(t)
			dir := projectDir(t, home)
			if err := SaveUserConfig(&UserConfig{Security: SecuritySettings{TrustedFolders: []string{"~/code/proj"}}}); err != nil {
				t.Fatal(err)
			}
			ClearUserConfigCache()
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, true},
		{"new install, sibling with a shared name prefix is not covered", func(t *testing.T) (string, string, *Instance) {
			home := installKindHome(t)
			dir := projectDir(t, home)
			if err := SaveUserConfig(&UserConfig{Security: SecuritySettings{TrustedFolders: []string{filepath.Join(home, "code", "pro")}}}); err != nil {
				t.Fatal(err)
			}
			ClearUserConfigCache()
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, false},
		{"new install, auto_trust_folders = true", func(t *testing.T) (string, string, *Instance) {
			home := newInstallWithConfig(t, &UserConfig{Security: SecuritySettings{AutoTrustFolders: &yes}})
			dir := projectDir(t, home)
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, true},
		{"new install, folder agent-deck owns (under its data dir)", func(t *testing.T) (string, string, *Instance) {
			home := newInstallWithConfig(t, nil)
			dir := filepath.Join(home, ".local", "share", "agent-deck", "watcher", "triage", "x")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, true},
		{"new install, worktree agent-deck made for the session", func(t *testing.T) (string, string, *Instance) {
			home := newInstallWithConfig(t, nil)
			dir := projectDir(t, home)
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir, WorktreePath: dir}
		}, true},
		{"existing install (config.toml without the key): pre-trusted as today", func(t *testing.T) (string, string, *Instance) {
			home := installKindHome(t)
			writeTestFile(t, xdgConfigTOML(home), "theme = \"dark\"\n")
			dir := projectDir(t, home)
			return home, dir, &Instance{Tool: "claude", ProjectPath: dir}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home, dir, inst := tc.setup(t)
			PreAcceptLaunchTrust(inst)
			if got := claudeTrusted(t, filepath.Join(home, ".claude.json"), dir); got != tc.want {
				t.Errorf("folder trusted = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFolderTrustPolicy_CodexStartTrust(t *testing.T) {
	for _, existing := range []bool{false, true} {
		home := installKindHome(t)
		if existing {
			writeTestFile(t, xdgConfigTOML(home), "theme = \"dark\"\n")
		} else if err := SaveUserConfig(&UserConfig{Theme: "dark"}); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
		dir := projectDir(t, home)
		inst := &Instance{Tool: "codex", ProjectPath: dir}
		inst.preAcceptCodexWorkspaceTrust()
		data, _ := os.ReadFile(GetCodexConfigPath(inst.getCodexHomeDir()))
		if got := strings.Contains(string(data), dir); got != existing {
			t.Errorf("existing=%v: codex trust written = %v, want %v\n%s", existing, got, existing, data)
		}
	}
}

func TestFolderTrustPolicy_CursorStartTrust(t *testing.T) {
	for _, existing := range []bool{false, true} {
		home := installKindHome(t)
		if existing {
			writeTestFile(t, xdgConfigTOML(home), "theme = \"dark\"\n")
		} else if err := SaveUserConfig(&UserConfig{Theme: "dark"}); err != nil {
			t.Fatal(err)
		}
		ClearUserConfigCache()
		dir := projectDir(t, home)
		inst := &Instance{Tool: "cursor", ProjectPath: dir}
		inst.preAcceptCursorWorkspaceTrust()
		trustPath, _, err := cursorWorkspaceTrustPath(GetCursorConfigDir(), dir)
		if err != nil {
			t.Fatal(err)
		}
		_, statErr := os.Stat(trustPath)
		if got := statErr == nil; got != existing {
			t.Errorf("existing=%v: cursor trust written = %v, want %v", existing, got, existing)
		}
	}
}

// C1: a relative env_file resolves into the project; it is sourced only when
// the project folder is trusted under the folder-trust rule.
func TestEnvFilePolicy_RelativeEnvFileNeedsTrustedFolder(t *testing.T) {
	cases := []struct {
		name       string
		existing   bool
		trusted    bool
		absolute   bool
		wantSource bool
	}{
		{name: "new install, untrusted project, relative env_file: skipped", wantSource: false},
		{name: "new install, trusted project, relative env_file: sourced", trusted: true, wantSource: true},
		{name: "new install, untrusted project, absolute env_file: sourced", absolute: true, wantSource: true},
		{name: "existing install, relative env_file: sourced as today", existing: true, wantSource: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := installKindHome(t)
			dir := projectDir(t, home)
			envFile := ".env"
			if tc.absolute {
				envFile = filepath.Join(home, "secrets.env")
			}
			if tc.existing {
				writeTestFile(t, xdgConfigTOML(home), "[claude]\nenv_file = \""+envFile+"\"\n")
			} else {
				cfg := &UserConfig{Claude: ClaudeSettings{EnvFile: envFile}}
				if tc.trusted {
					cfg.Security.TrustedFolders = []string{dir}
				}
				if err := SaveUserConfig(cfg); err != nil {
					t.Fatal(err)
				}
			}
			ClearUserConfigCache()
			inst := &Instance{Title: "envtest", Tool: "claude", ProjectPath: dir}
			got := inst.buildEnvSourceCommand()
			resolved := resolvePath(envFile, dir)
			sourced := strings.Contains(got, "source "+shellQuote(resolved))
			if sourced != tc.wantSource {
				t.Fatalf("env_file %s sourced = %v, want %v\n%s", resolved, sourced, tc.wantSource, got)
			}
			if !tc.wantSource && !strings.Contains(got, "not sourced") {
				t.Errorf("skipped env_file has no pane warning:\n%s", got)
			}
		})
	}
}
