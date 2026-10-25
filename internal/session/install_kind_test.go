package session

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/statedb"
)

// installKindHome points HOME at a fresh temp dir with the XDG base dirs
// cleared, so every agent-deck path resolves under it, and returns it.
func installKindHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, k := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		t.Setenv(k, "")
	}
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return home
}

func readTestFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func xdgConfigTOML(home string) string {
	return filepath.Join(home, ".config", "agent-deck", "config.toml")
}

func installMarker(t *testing.T, home string) string {
	t.Helper()
	for _, p := range []string{
		filepath.Join(home, ".local", "share", "agent-deck", InstallKindMarkerName),
		filepath.Join(home, ".agent-deck", InstallKindMarkerName),
	} {
		if data, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(data))
		}
	}
	return ""
}

func listFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	return out
}

// A home with nothing of agent-deck in it is a new install: safe values.
func TestInstallKind_FreshHomeIsNew(t *testing.T) {
	installKindHome(t)
	if got := InstallKind(); got != InstallKindNew {
		t.Fatalf("InstallKind() = %q, want %q", got, InstallKindNew)
	}
	var c ClaudeSettings
	if c.GetDangerousMode() {
		t.Error("fresh install: unset dangerous_mode resolved to true, want false")
	}
	var s SecuritySettings
	if s.GetAutoTrustFolders() {
		t.Error("fresh install: unset auto_trust_folders resolved to true, want false")
	}
}

// The first config.toml this release writes (setup wizard, Settings, config
// set) records the install as new before the file exists, so the file it
// leaves behind does not turn the install into an existing one.
func TestInstallKind_FirstConfigSaveRecordsNew(t *testing.T) {
	home := installKindHome(t)
	if err := SaveUserConfig(&UserConfig{Theme: "dark"}); err != nil {
		t.Fatalf("SaveUserConfig: %v", err)
	}
	if got := installMarker(t, home); got != InstallKindNew {
		t.Fatalf("marker after first save = %q, want %q", got, InstallKindNew)
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Claude.GetDangerousMode() {
		t.Error("new install with a config.toml written by this release: dangerous_mode resolved to true")
	}
}

// Same for CreateExampleConfig.
func TestInstallKind_ExampleConfigRecordsNew(t *testing.T) {
	home := installKindHome(t)
	if err := CreateExampleConfig(); err != nil {
		t.Fatalf("CreateExampleConfig: %v", err)
	}
	if got := installMarker(t, home); got != InstallKindNew {
		t.Fatalf("marker after example config = %q, want %q", got, InstallKindNew)
	}
	if got := InstallKind(); got != InstallKindNew {
		t.Fatalf("InstallKind() = %q, want new", got)
	}
}

// A CLI-first new user's first command creates a state.db; that records new.
func TestInstallKind_FirstStateDBRecordsNew(t *testing.T) {
	home := installKindHome(t)
	dbPath := filepath.Join(home, ".local", "share", "agent-deck", "profiles", "default", "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("statedb.Open: %v", err)
	}
	_ = db.Close()
	if got := installMarker(t, home); got != InstallKindNew {
		t.Fatalf("marker after first state.db = %q, want %q", got, InstallKindNew)
	}
	if got := InstallKind(); got != InstallKindNew {
		t.Fatalf("InstallKind() = %q, want new", got)
	}
}

// A config dir, a data dir, logs and config.json made by some other command
// are not evidence of an older install.
func TestInstallKind_DirsAndOtherFilesAreNotEvidence(t *testing.T) {
	home := installKindHome(t)
	for _, d := range []string{
		filepath.Join(home, ".config", "agent-deck"),
		filepath.Join(home, ".local", "share", "agent-deck", "logs"),
		filepath.Join(home, ".agent-deck", "hooks"),
	} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(home, ".config", "agent-deck", "config.json"), `{"default_profile":"default"}`)
	if got := InstallKind(); got != InstallKindNew {
		t.Fatalf("InstallKind() = %q, want new", got)
	}
}

// An existing config.toml without the keys (written by an older release or by
// hand) is an existing install: today's values.
func TestInstallKind_ConfigWithoutKeysIsExisting(t *testing.T) {
	for _, path := range []func(string) string{
		xdgConfigTOML,
		func(h string) string { return filepath.Join(h, ".agent-deck", "config.toml") },
	} {
		home := installKindHome(t)
		writeTestFile(t, path(home), "theme = \"dark\"\n")
		if got := InstallKind(); got != InstallKindExisting {
			t.Fatalf("%s: InstallKind() = %q, want existing", path(home), got)
		}
		cfg, err := LoadUserConfig()
		if err != nil {
			t.Fatal(err)
		}
		if !cfg.Claude.GetDangerousMode() {
			t.Errorf("%s: existing install lost dangerous_mode default true", path(home))
		}
		if !cfg.Security.GetAutoTrustFolders() {
			t.Errorf("%s: existing install lost auto_trust_folders default true", path(home))
		}
	}
}

// Sessions in any profile, under either data root, make an existing install,
// with or without config.toml.
func TestInstallKind_ProfileStoresInAnyProfileAreExisting(t *testing.T) {
	cases := map[string]func(string) string{
		"xdg non-default profile state.db": func(h string) string {
			return filepath.Join(h, ".local", "share", "agent-deck", "profiles", "work", "state.db")
		},
		"legacy default profile state.db": func(h string) string {
			return filepath.Join(h, ".agent-deck", "profiles", "default", "state.db")
		},
		"legacy profile sessions.json": func(h string) string {
			return filepath.Join(h, ".agent-deck", "profiles", "work", "sessions.json")
		},
		"pre-profile sessions.json": func(h string) string {
			return filepath.Join(h, ".agent-deck", "sessions.json")
		},
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			home := installKindHome(t)
			writeTestFile(t, path(home), "x")
			if got := InstallKind(); got != InstallKindExisting {
				t.Fatalf("InstallKind() = %q, want existing", got)
			}
			var c ClaudeSettings
			if !c.GetDangerousMode() {
				t.Error("existing CLI-first install (no config.toml) lost dangerous_mode default true")
			}
		})
	}
}

// The update path: an existing install whose new binary creates a store for
// a new profile (a daemon, say) stays existing; the create hook never
// records "new" over evidence.
func TestInstallKind_UpdateCreatingNewProfileStoreStaysExisting(t *testing.T) {
	home := installKindHome(t)
	writeTestFile(t, filepath.Join(home, ".agent-deck", "profiles", "default", "state.db"), "x")
	dbPath := filepath.Join(home, ".agent-deck", "profiles", "work", "state.db")
	db, err := statedb.Open(dbPath)
	if err != nil {
		t.Fatalf("statedb.Open: %v", err)
	}
	_ = db.Close()
	if got := installMarker(t, home); got == InstallKindNew {
		t.Fatal("create hook recorded new on an existing install")
	}
	if got := InstallKind(); got != InstallKindExisting {
		t.Fatalf("InstallKind() = %q, want existing", got)
	}
}

// Once recorded new, a config.toml copied in later (dotfiles) does not flip
// the install back to the old defaults.
func TestInstallKind_RecordedNewIsFinal(t *testing.T) {
	home := installKindHome(t)
	if err := SaveUserConfig(&UserConfig{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(home, ".agent-deck", "profiles", "default", "state.db"), "x")
	if got := InstallKind(); got != InstallKindNew {
		t.Fatalf("InstallKind() = %q, want new", got)
	}
}

const existingConfigNoKeys = `# my agent-deck config, hand edited
theme = "light"  # keep

[claude]
# account dir
config_dir = "~/.claude-work"

[logs]
max_lines = 5000
`

// Existing config.toml without the keys: today's values are written into it
// once, the rest of the file (comments too) is kept byte for byte, a .bak is
// taken, and the notice is returned once.
func TestApplyInstallDefaults_ExistingConfigWithoutKeysWrittenOnce(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	writeTestFile(t, path, existingConfigNoKeys)

	notice, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatalf("ApplyInstallDefaults: %v", err)
	}
	if notice == "" {
		t.Fatal("first run on an existing install returned no notice")
	}
	for _, want := range []string{"dangerous_mode = true", "auto_trust_folders = true", "shown once"} {
		if !strings.Contains(notice, want) {
			t.Errorf("notice missing %q:\n%s", want, notice)
		}
	}
	after := readTestFile(t, path)
	for _, line := range strings.Split(existingConfigNoKeys, "\n") {
		if !strings.Contains(after, line) {
			t.Errorf("line %q of the user's config was lost:\n%s", line, after)
		}
	}
	if !strings.Contains(after, "[claude]\ndangerous_mode = true\n") {
		t.Errorf("dangerous_mode = true not written under [claude]:\n%s", after)
	}
	if !strings.Contains(after, "[security]\nauto_trust_folders = true\n") {
		t.Errorf("[security] auto_trust_folders = true not written:\n%s", after)
	}
	if bak := readTestFile(t, path+".bak"); bak != existingConfigNoKeys {
		t.Errorf("config.toml.bak = %q, want the original file", bak)
	}
	if got := installMarker(t, home); got != InstallKindExisting {
		t.Errorf("marker = %q, want existing", got)
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatalf("written config does not load: %v", err)
	}
	if cfg.Claude.DangerousMode == nil || !*cfg.Claude.DangerousMode || cfg.Claude.ConfigDir != "~/.claude-work" || cfg.Logs.MaxLines != 5000 || cfg.Theme != "light" {
		t.Errorf("written config decodes wrong: %+v", cfg.Claude)
	}

	notice2, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if notice2 != "" {
		t.Errorf("second run returned a notice again:\n%s", notice2)
	}
	if again := readTestFile(t, path); again != after {
		t.Errorf("second run changed config.toml:\n%s", again)
	}
}

// Explicit values are never touched, and nothing is announced for them.
func TestApplyInstallDefaults_ExplicitValuesUntouched(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	const content = "[claude]\ndangerous_mode = false\n\n[security]\nauto_trust_folders = false\n"
	writeTestFile(t, path, content)
	notice, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if notice != "" {
		t.Errorf("explicit values: unexpected notice:\n%s", notice)
	}
	if got := readTestFile(t, path); got != content {
		t.Errorf("explicit values: config.toml changed:\n%s", got)
	}
	if _, err := os.Stat(path + ".bak"); err == nil {
		t.Error("explicit values: a .bak was written although nothing changed")
	}
	cfg, _ := LoadUserConfig()
	if cfg.Claude.GetDangerousMode() || cfg.Security.GetAutoTrustFolders() {
		t.Error("explicit false values resolved to true")
	}
	if got := installMarker(t, home); got != InstallKindExisting {
		t.Errorf("marker = %q, want existing", got)
	}
}

// Only the missing key is written when one is set.
func TestApplyInstallDefaults_WritesOnlyMissingKeys(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	writeTestFile(t, path, "[claude]\ndangerous_mode = false\n")
	notice, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatal(err)
	}
	after := readTestFile(t, path)
	if strings.Count(after, "dangerous_mode") != 1 || !strings.Contains(after, "dangerous_mode = false") {
		t.Errorf("explicit dangerous_mode changed:\n%s", after)
	}
	if !strings.Contains(after, "auto_trust_folders = true") {
		t.Errorf("missing auto_trust_folders not written:\n%s", after)
	}
	if strings.Contains(notice, "dangerous_mode") || !strings.Contains(notice, "auto_trust_folders") {
		t.Errorf("notice should list only the written key:\n%s", notice)
	}
}

// A fresh install: nothing is written, nothing is announced.
func TestApplyInstallDefaults_FreshInstallWritesNothing(t *testing.T) {
	home := installKindHome(t)
	notice, err := ApplyInstallDefaults()
	if err != nil || notice != "" {
		t.Fatalf("fresh install: notice=%q err=%v, want none", notice, err)
	}
	if files := listFiles(t, home); len(files) != 0 {
		t.Errorf("fresh install: ApplyInstallDefaults wrote %v", files)
	}
}

// An existing install without config.toml: no config.toml is created (the
// TUI's first-run wizard keys off its absence); the marker keeps today's
// values and the notice says how to change them.
func TestApplyInstallDefaults_ExistingWithoutConfigCreatesNoConfig(t *testing.T) {
	home := installKindHome(t)
	writeTestFile(t, filepath.Join(home, ".agent-deck", "profiles", "default", "state.db"), "x")
	notice, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if notice == "" || !strings.Contains(notice, "dangerous_mode = false") {
		t.Errorf("notice should say how to turn the kept values off:\n%s", notice)
	}
	for _, p := range []string{xdgConfigTOML(home), filepath.Join(home, ".agent-deck", "config.toml")} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("config.toml created at %s", p)
		}
	}
	if got := installMarker(t, home); got != InstallKindExisting {
		t.Errorf("marker = %q, want existing", got)
	}
	var c ClaudeSettings
	if !c.GetDangerousMode() {
		t.Error("existing install without config.toml lost dangerous_mode default true")
	}
	if n, _ := ApplyInstallDefaults(); n != "" {
		t.Errorf("notice repeated:\n%s", n)
	}
}

// A broken config.toml is never rewritten and the decision is retried later.
func TestApplyInstallDefaults_BrokenConfigUntouched(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	const broken = "[claude\ndangerous_mode = \n"
	writeTestFile(t, path, broken)
	if _, err := ApplyInstallDefaults(); err == nil {
		t.Error("broken config: want an error")
	}
	if got := readTestFile(t, path); got != broken {
		t.Errorf("broken config rewritten:\n%s", got)
	}
	if got := installMarker(t, home); got != "" {
		t.Errorf("broken config: marker %q written, want none", got)
	}
}

// A layout the line insert cannot handle safely (an inline claude table) is
// left exactly as it is; the install still keeps today's values.
func TestApplyInstallDefaults_UnsafeLayoutLeftAlone(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	const inline = "claude = { config_dir = \"~/.claude-x\" }\nsecurity = { trusted_folders = [] }\n"
	writeTestFile(t, path, inline)
	notice, err := ApplyInstallDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if got := readTestFile(t, path); got != inline {
		t.Errorf("inline-table config changed:\n%s", got)
	}
	if notice == "" {
		t.Error("want a notice that explains the kept values")
	}
	ClearUserConfigCache()
	cfg, err := LoadUserConfig()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Claude.GetDangerousMode() || !cfg.Security.GetAutoTrustFolders() {
		t.Error("existing install lost today's values when the config could not be edited")
	}
}

// A dotfiles-managed config.toml symlink stays a symlink; the target gets
// the lines.
func TestApplyInstallDefaults_SymlinkedConfigWrittenThrough(t *testing.T) {
	home := installKindHome(t)
	target := filepath.Join(home, "dotfiles", "agent-deck.toml")
	writeTestFile(t, target, "theme = \"dark\"\n")
	path := xdgConfigTOML(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyInstallDefaults(); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("config.toml is no longer a symlink (err=%v)", err)
	}
	if got := readTestFile(t, target); !strings.Contains(got, "dangerous_mode = true") {
		t.Errorf("symlink target not updated:\n%s", got)
	}
}

// Deleting the written line later keeps the existing install's value (the
// marker); setting it to false is how it is turned off.
func TestApplyInstallDefaults_MarkerKeepsValueAfterLineRemoved(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	writeTestFile(t, path, "theme = \"dark\"\n")
	if _, err := ApplyInstallDefaults(); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, path, "theme = \"dark\"\n")
	ClearUserConfigCache()
	cfg, _ := LoadUserConfig()
	if !cfg.Claude.GetDangerousMode() {
		t.Error("existing install: removing the written line flipped dangerous_mode")
	}
}

// Review M1: a profile directory that is a symlink (to another disk or a
// dotfiles location) still holds the install's history.
func TestInstallKind_SymlinkedProfileDirIsExisting(t *testing.T) {
	home := installKindHome(t)
	real := filepath.Join(home, "data", "agent-deck-default")
	writeTestFile(t, filepath.Join(real, "state.db"), "x")
	profiles := filepath.Join(home, ".local", "share", "agent-deck", "profiles")
	if err := os.MkdirAll(profiles, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(profiles, "default")); err != nil {
		t.Fatal(err)
	}
	if got := InstallKind(); got != InstallKindExisting {
		t.Fatalf("InstallKind() = %q, want existing (symlinked profile dir with a store)", got)
	}
	// The first-run wizard's save must not record the install as new.
	if err := SaveUserConfig(&UserConfig{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if got := installMarker(t, home); got == InstallKindNew {
		t.Fatal("first config save recorded an existing install as new")
	}
	ClearUserConfigCache()
	cfg, _ := LoadUserConfig()
	if !cfg.Security.GetAutoTrustFolders() {
		t.Error("existing install with a symlinked profile dir lost auto_trust_folders = true")
	}
}

// Review LOW: the one-time edit keeps the file's permission bits.
func TestApplyInstallDefaults_KeepsFileMode(t *testing.T) {
	home := installKindHome(t)
	path := xdgConfigTOML(home)
	writeTestFile(t, path, "theme = \"dark\"\n")
	if err := os.Chmod(path, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyInstallDefaults(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Errorf("config.toml mode after the one-time edit = %#o, want 0640", got)
	}
	if !strings.Contains(readTestFile(t, path), "dangerous_mode = true") {
		t.Error("one-time edit did not run")
	}
}
