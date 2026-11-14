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

// Review M2: on a new install, a launch into a folder agent-deck may not
// trust reports a machine-readable warning; an existing install reports none.
func TestPreAcceptLaunchTrust_ReturnsFolderNotTrustedWarning(t *testing.T) {
	home := newInstallWithConfig(t, nil)
	dir := projectDir(t, home)
	w := PreAcceptLaunchTrust(&Instance{Tool: "claude", ProjectPath: dir})
	if w == nil {
		t.Fatal("new install, untrusted folder: no warning")
	}
	real, _ := filepath.EvalSymlinks(dir)
	if w.Code != WarningFolderNotTrusted || w.Folder != real || !strings.Contains(w.Hint, "--trust-folder") || !strings.Contains(w.Hint, "trusted_folders") {
		t.Errorf("warning = %+v", w)
	}

	home2 := installKindHome(t)
	writeTestFile(t, xdgConfigTOML(home2), "theme = \"dark\"\n")
	if w := PreAcceptLaunchTrust(&Instance{Tool: "claude", ProjectPath: projectDir(t, home2)}); w != nil {
		t.Errorf("existing install: unexpected warning %+v", w)
	}
}

// Review M2: the queued drain (a daemon, its stderr is lost) logs the same
// warning code and returns it.
func TestQueuedStart_NewInstallUntrustedFolderWarns(t *testing.T) {
	home := newInstallWithConfig(t, nil)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	dir := projectDir(t, home)
	inst := NewInstanceWithGroup("worker", dir, "workers")
	inst.Tool = "claude"
	inst.MarkQueued("")
	storage := newTestStorage(t)
	if err := storage.SaveWithGroups([]*Instance{inst}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(SetQueuedSpawnForTest(func(i *Instance, _ string) error { return nil }))
	buf := captureSessionLogJSON(t)
	out, err := storage.StartQueued(inst)
	if err != nil {
		t.Fatal(err)
	}
	if out.TrustWarning == nil || out.TrustWarning.Code != WarningFolderNotTrusted {
		t.Fatalf("queued start TrustWarning = %+v, want %s", out.TrustWarning, WarningFolderNotTrusted)
	}
	if !strings.Contains(buf.String(), `"msg":"queued_start_folder_not_trusted"`) || !strings.Contains(buf.String(), `"code":"FOLDER_NOT_TRUSTED"`) {
		t.Errorf("queued drain did not log the warning code:\n%s", buf.String())
	}
}

// Review LOW: --trust-folder edits config.toml in place, keeping comments.
func TestAddTrustedFolder_KeepsComments(t *testing.T) {
	for name, content := range map[string]string{
		"no security table": "# keep me\ntheme = \"dark\" # and me\n",
		"empty list":        "# keep me\n[security]\n# list\ntrusted_folders = []\n",
		"one entry":         "# keep me\n[security]\ntrusted_folders = [\"/srv/a\"] # mine\n",
	} {
		t.Run(name, func(t *testing.T) {
			home := installKindHome(t)
			path := xdgConfigTOML(home)
			writeTestFile(t, path, content)
			dir := projectDir(t, home)
			if err := AddTrustedFolder(dir); err != nil {
				t.Fatal(err)
			}
			after := readTestFile(t, path)
			for _, comment := range []string{"# keep me", "# and me", "# list", "# mine"} {
				if strings.Contains(content, comment) && !strings.Contains(after, comment) {
					t.Errorf("comment %q lost:\n%s", comment, after)
				}
			}
			ClearUserConfigCache()
			cfg, err := LoadUserConfig()
			if err != nil {
				t.Fatal(err)
			}
			real, _ := filepath.EvalSymlinks(dir)
			found := false
			for _, f := range cfg.Security.TrustedFolders {
				found = found || f == real
			}
			if !found {
				t.Errorf("trusted_folders = %v, want %s added", cfg.Security.TrustedFolders, real)
			}
			if name == "one entry" && (len(cfg.Security.TrustedFolders) != 2 || !strings.Contains(after, "# mine")) {
				t.Errorf("existing entry or its comment lost:\n%s", after)
			}
			// Idempotent.
			if err := AddTrustedFolder(dir); err != nil {
				t.Fatal(err)
			}
			if again := readTestFile(t, path); again != after {
				t.Errorf("second add changed the file:\n%s", again)
			}
		})
	}
}
