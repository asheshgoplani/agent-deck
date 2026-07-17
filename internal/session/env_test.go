package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get home directory")
	}

	// Set a known env var for testing
	t.Setenv("AGENTDECK_TEST_DIR", "/tmp/testdir")
	t.Setenv("AGENTDECK_RELATIVE_TEST_DIR", "tmp/relative/testdir")

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"absolute path", "/var/log/test.log", "/var/log/test.log"},
		{"relative path", ".env", ".env"},
		{"tilde prefix", "~/.secrets", filepath.Join(home, ".secrets")},
		{"just tilde", "~", home},
		{"tilde in middle", "/path/~/.env", "/path/~/.env"},
		{"$HOME expansion", "$HOME/.claude.env", filepath.Join(home, ".claude.env")},
		{"${HOME} expansion", "${HOME}/.claude.env", filepath.Join(home, ".claude.env")},
		{"custom env var", "$AGENTDECK_TEST_DIR/.env", "/tmp/testdir/.env"},
		{"env var in middle", "/opt/$AGENTDECK_RELATIVE_TEST_DIR/file", "/opt/tmp/relative/testdir/file"},
		{"tilde with env var after", "~/$AGENTDECK_TEST_DIR/.env", filepath.Join(home, "/tmp/testdir/.env")},
		{"tilde with ${VAR} after", "~/${AGENTDECK_TEST_DIR}/.env", filepath.Join(home, "/tmp/testdir/.env")},
		{"undefined env var", "$UNDEFINED_VAR/.env", "/.env"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExpandPath(tt.input)
			if result != tt.expected {
				t.Errorf("ExpandPath(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestResolvePath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("Cannot get home directory")
	}

	workDir := "/projects/myapp"

	tests := []struct {
		name     string
		path     string
		workDir  string
		expected string
	}{
		{"absolute path", "/etc/env", workDir, "/etc/env"},
		{"home path", "~/.secrets", workDir, filepath.Join(home, ".secrets")},
		{"relative path", ".env", workDir, "/projects/myapp/.env"},
		{"relative subdir", "config/.env", workDir, "/projects/myapp/config/.env"},
		{"$HOME env var", "$HOME/.claude.env", workDir, filepath.Join(home, ".claude.env")},
		{"${HOME} env var", "${HOME}/.secrets", workDir, filepath.Join(home, ".secrets")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := resolvePath(tt.path, tt.workDir)
			if result != tt.expected {
				t.Errorf("resolvePath(%q, %q) = %q, want %q", tt.path, tt.workDir, result, tt.expected)
			}
		})
	}
}

func TestIsFilePath(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"/etc/env", true},
		{"~/env", true},
		{"./env", true},
		{"../env", true},
		{"~", true},
		{"eval $(direnv hook bash)", false},
		{"source ~/.bashrc", false},
		{".env", false}, // Treated as inline command, not file path
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := isFilePath(tt.input)
			if result != tt.expected {
				t.Errorf("isFilePath(%q) = %v, want %v", tt.input, result, tt.expected)
			}
		})
	}
}

func TestBuildSourceCmd(t *testing.T) {
	tests := []struct {
		name          string
		path          string
		ignoreMissing bool
		wantContains  []string
	}{
		{
			name:          "ignore missing",
			path:          "/path/.env",
			ignoreMissing: true,
			wantContains:  []string{`[ -f "/path/.env" ]`, `source "/path/.env"`},
		},
		{
			name:          "strict mode",
			path:          "/path/.env",
			ignoreMissing: false,
			wantContains:  []string{`source "/path/.env"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildSourceCmd(tt.path, tt.ignoreMissing)
			for _, want := range tt.wantContains {
				if !strings.Contains(result, want) {
					t.Errorf("buildSourceCmd(%q, %v) = %q, want to contain %q", tt.path, tt.ignoreMissing, result, want)
				}
			}
		})
	}
}

func TestGetToolInlineEnv(t *testing.T) {
	// Save and restore the original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name     string
		tool     string
		env      map[string]string
		expected string
	}{
		{
			name:     "nil tool def returns empty",
			tool:     "nonexistent",
			env:      nil,
			expected: "",
		},
		{
			name:     "empty env map returns empty",
			tool:     "testtool",
			env:      map[string]string{},
			expected: "",
		},
		{
			name:     "single var",
			tool:     "testtool",
			env:      map[string]string{"API_KEY": "secret123"},
			expected: "export API_KEY='secret123'",
		},
		{
			name: "multiple vars sorted alphabetically",
			tool: "testtool",
			env: map[string]string{
				"ZEBRA":  "last",
				"ALPHA":  "first",
				"MIDDLE": "mid",
			},
			expected: "export ALPHA='first' && export MIDDLE='mid' && export ZEBRA='last'",
		},
		{
			name:     "value with single quotes escaped",
			tool:     "testtool",
			env:      map[string]string{"MSG": "it's a test"},
			expected: "export MSG='it'\\''s a test'",
		},
		{
			name:     "value with dollar sign not expanded",
			tool:     "testtool",
			env:      map[string]string{"VAR": "$HOME/path"},
			expected: "export VAR='$HOME/path'",
		},
		{
			name:     "value with backticks not expanded",
			tool:     "testtool",
			env:      map[string]string{"CMD": "`whoami`"},
			expected: "export CMD='`whoami`'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up config cache with test tool
			userConfigCacheMu.Lock()
			if tt.env != nil || tt.tool == "testtool" {
				userConfigCache = &UserConfig{
					Tools: map[string]ToolDef{
						"testtool": {Env: tt.env},
					},
					MCPs: make(map[string]MCPDef),
				}
			} else {
				userConfigCache = &UserConfig{
					Tools: make(map[string]ToolDef),
					MCPs:  make(map[string]MCPDef),
				}
			}
			userConfigCacheMu.Unlock()

			inst := &Instance{Tool: tt.tool}
			result := inst.getToolInlineEnv()
			if result != tt.expected {
				t.Errorf("getToolInlineEnv() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestThemeEnvExport(t *testing.T) {
	// Save and restore original config cache + COLORFGBG env var
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name         string
		theme        string
		envCOLORFGBG string // parent terminal value (empty = unset)
		wantContains string
	}{
		{
			name:         "dark theme without parent env",
			theme:        "dark",
			envCOLORFGBG: "",
			wantContains: "export COLORFGBG='15;0'",
		},
		{
			name:         "light theme without parent env",
			theme:        "light",
			envCOLORFGBG: "",
			wantContains: "export COLORFGBG='0;15'",
		},
		{
			name:         "dark theme with matching parent env",
			theme:        "dark",
			envCOLORFGBG: "7;0",
			wantContains: "export COLORFGBG='7;0'", // propagate parent's exact value
		},
		{
			name:         "light theme with matching parent env",
			theme:        "light",
			envCOLORFGBG: "0;15",
			wantContains: "export COLORFGBG='0;15'", // propagate parent's exact value
		},
		{
			name:         "dark theme with mismatched parent env (parent says light)",
			theme:        "dark",
			envCOLORFGBG: "0;15",
			wantContains: "export COLORFGBG='15;0'", // override with dark value
		},
		{
			name:         "light theme with mismatched parent env (parent says dark)",
			theme:        "light",
			envCOLORFGBG: "15;0",
			wantContains: "export COLORFGBG='0;15'", // override with light value
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up theme config
			userConfigCacheMu.Lock()
			userConfigCache = &UserConfig{
				Theme: tt.theme,
				MCPs:  make(map[string]MCPDef),
			}
			userConfigCacheMu.Unlock()

			// Set or unset COLORFGBG
			if tt.envCOLORFGBG != "" {
				t.Setenv("COLORFGBG", tt.envCOLORFGBG)
			} else {
				t.Setenv("COLORFGBG", "")
				os.Unsetenv("COLORFGBG")
			}

			result := themeEnvExport()
			if result != tt.wantContains {
				t.Errorf("themeEnvExport() = %q, want %q", result, tt.wantContains)
			}
		})
	}
}

func TestThemeColorFGBG(t *testing.T) {
	// Save and restore original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	tests := []struct {
		name     string
		theme    string
		envVal   string
		expected string
	}{
		{"dark theme", "dark", "", "15;0"},
		{"light theme", "light", "", "0;15"},
		{"dark with matching parent", "dark", "7;0", "7;0"},
		{"light with matching parent", "light", "0;8", "0;8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userConfigCacheMu.Lock()
			userConfigCache = &UserConfig{
				Theme: tt.theme,
				MCPs:  make(map[string]MCPDef),
			}
			userConfigCacheMu.Unlock()

			if tt.envVal != "" {
				t.Setenv("COLORFGBG", tt.envVal)
			} else {
				t.Setenv("COLORFGBG", "")
				os.Unsetenv("COLORFGBG")
			}

			result := ThemeColorFGBG()
			if result != tt.expected {
				t.Errorf("ThemeColorFGBG() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestBuildEnvSourceCommand_IncludesTheme(t *testing.T) {
	// Save and restore original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	// Ensure no parent COLORFGBG
	t.Setenv("COLORFGBG", "")
	os.Unsetenv("COLORFGBG")

	// Set up light theme config
	userConfigCacheMu.Lock()
	userConfigCache = &UserConfig{
		Theme: "light",
		MCPs:  make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()

	inst := &Instance{Tool: "codex", ProjectPath: "/tmp"}
	result := inst.buildEnvSourceCommand()

	if !strings.Contains(result, "COLORFGBG") {
		t.Errorf("buildEnvSourceCommand() = %q, should contain COLORFGBG", result)
	}
	if !strings.Contains(result, "0;15") {
		t.Errorf("buildEnvSourceCommand() = %q, should contain light theme COLORFGBG value '0;15'", result)
	}
}

func TestBuildEnvSourceCommand_SandboxSkipsThemeExport(t *testing.T) {
	// Save and restore original config cache
	userConfigCacheMu.Lock()
	origCache := userConfigCache
	userConfigCacheMu.Unlock()
	defer func() {
		userConfigCacheMu.Lock()
		userConfigCache = origCache
		userConfigCacheMu.Unlock()
	}()

	// Ensure no parent COLORFGBG
	t.Setenv("COLORFGBG", "")
	os.Unsetenv("COLORFGBG")

	// Set up light theme config
	userConfigCacheMu.Lock()
	userConfigCache = &UserConfig{
		Theme: "light",
		MCPs:  make(map[string]MCPDef),
	}
	userConfigCacheMu.Unlock()

	inst := &Instance{
		Tool:        "opencode",
		ProjectPath: "/tmp",
		Sandbox:     &SandboxConfig{Enabled: true, Image: "example/sandbox:latest"},
	}
	result := inst.buildEnvSourceCommand()

	if strings.Contains(result, "COLORFGBG") {
		t.Errorf("buildEnvSourceCommand() = %q, should not contain COLORFGBG for sandboxed sessions", result)
	}
}

func TestShellSettings_GetIgnoreMissingEnvFiles(t *testing.T) {
	trueBool := true
	falseBool := false

	tests := []struct {
		name     string
		settings ShellSettings
		expected bool
	}{
		{"nil pointer defaults to true", ShellSettings{}, true},
		{"explicit true", ShellSettings{IgnoreMissingEnvFiles: &trueBool}, true},
		{"explicit false", ShellSettings{IgnoreMissingEnvFiles: &falseBool}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.settings.GetIgnoreMissingEnvFiles()
			if result != tt.expected {
				t.Errorf("GetIgnoreMissingEnvFiles() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestGetConductorEnv(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)

	name := "env-order-test"
	env := map[string]string{
		"MY_API_KEY": "conductor-value",
		"DEBUG":      "true",
	}
	if err := SetupConductor(name, "default", true, true, "", "", "", "", env, ""); err != nil {
		t.Fatalf("setup failed: %v", err)
	}

	inst := &Instance{
		Title:       "conductor-" + name,
		Tool:        "claude",
		ProjectPath: tmpHome,
	}

	result := inst.getConductorEnv(true)
	if result == "" {
		t.Fatal("getConductorEnv returned empty string for conductor with env vars")
	}
	if !strings.Contains(result, "export DEBUG='true'") {
		t.Errorf("expected DEBUG export, got: %s", result)
	}
	if !strings.Contains(result, "export MY_API_KEY='conductor-value'") {
		t.Errorf("expected MY_API_KEY export, got: %s", result)
	}

	// Verify ordering: DEBUG before MY_API_KEY (sorted)
	debugIdx := strings.Index(result, "DEBUG")
	apiIdx := strings.Index(result, "MY_API_KEY")
	if debugIdx > apiIdx {
		t.Error("env vars should be sorted alphabetically")
	}
}

func TestGetConductorEnv_NonConductorSession(t *testing.T) {
	inst := &Instance{
		Title: "main",
		Tool:  "claude",
	}
	if result := inst.getConductorEnv(true); result != "" {
		t.Errorf("non-conductor session should return empty, got: %s", result)
	}
}

func TestValidateEnvFileForProbe(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("Cannot get home directory")
	}
	project := filepath.Join(home, "agent-deck-probe-test-project")

	// Paths under the home or project root are eligible for the existence probe.
	accepted := []string{
		filepath.Join(home, ".env"),
		filepath.Join(home, ".config", "agent-deck", "secrets.env"),
		filepath.Join(project, ".env"),
		filepath.Join(project, "nested", "tool.env"),
		home,    // the root itself
		project, // the root itself
	}
	for _, p := range accepted {
		got, ok := validateEnvFileForProbe(p, project)
		if !ok {
			t.Errorf("expected %q to be probe-eligible under home/project roots", p)
			continue
		}
		if got != filepath.Clean(p) {
			t.Errorf("validateEnvFileForProbe(%q) returned %q, want cleaned %q", p, got, filepath.Clean(p))
		}
	}

	// Fail closed: outside every known root, relative, or traversal-bearing.
	rejected := []string{
		"/etc/passwd",                          // outside home + project
		"/var/run/secrets/.env",                // outside home + project
		".env",                                 // relative — cannot prove containment
		"relative/path/.env",                   // relative
		"",                                     // empty
		filepath.Join(home, "..", "other.env"), // traversal escaping home
		// A sibling dir whose string prefix matches home but which is NOT
		// contained (boundary-aware: home + separator, not a raw prefix).
		home + "-sibling/.env",
	}
	for _, p := range rejected {
		if got, ok := validateEnvFileForProbe(p, project); ok {
			t.Errorf("expected %q to be rejected (fail-closed), got ok with %q", p, got)
		}
	}
}

// TestValidateEnvFileForProbe_SymlinkEscape exercises the filesystem-containment
// stage: a path that is LEXICALLY inside the project root but whose real target
// escapes via a symlink must be rejected before any os.Stat follows the link.
// This is the exact bypass the codex adversarial pass found against the
// lexical-only first-round fix (`~/link` → /etc/passwd). Without the
// resolveProbeTarget / symlink-resolved containment stage every case below
// returns ok=true, so the test is non-vacuous — it fails on the old code.
func TestValidateEnvFileForProbe_SymlinkEscape(t *testing.T) {
	project := t.TempDir()
	outside := t.TempDir()

	victim := filepath.Join(outside, "secret.env")
	if err := os.WriteFile(victim, []byte("X=1"), 0o600); err != nil {
		t.Fatalf("write victim: %v", err)
	}

	// 1. Symlinked final file inside the project pointing OUT of it.
	link := filepath.Join(project, "link.env")
	if err := os.Symlink(victim, link); err != nil {
		t.Fatalf("symlink final file: %v", err)
	}
	if got, ok := validateEnvFileForProbe(link, project); ok {
		t.Errorf("symlinked env_file escaping the project must be rejected; got ok with %q", got)
	}

	// 2. Symlinked PARENT directory; existing leaf under it resolves outside.
	evilDir := filepath.Join(project, "evil")
	if err := os.Symlink(outside, evilDir); err != nil {
		t.Fatalf("symlink parent dir: %v", err)
	}
	viaParent := filepath.Join(evilDir, "secret.env")
	if got, ok := validateEnvFileForProbe(viaParent, project); ok {
		t.Errorf("env_file under a symlinked parent escaping the project must be rejected; got ok with %q", got)
	}

	// 3. Symlinked parent with a MISSING leaf — resolveCanonical alone would
	//    leave this lexically contained; resolveProbeTarget must still reject.
	viaParentMissing := filepath.Join(evilDir, "nonexistent.env")
	if got, ok := validateEnvFileForProbe(viaParentMissing, project); ok {
		t.Errorf("missing leaf under a symlinked parent escaping the project must be rejected; got ok with %q", got)
	}

	// Control: a real file genuinely inside the project is still probe-eligible.
	real := filepath.Join(project, "real.env")
	if err := os.WriteFile(real, []byte("X=1"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}
	if _, ok := validateEnvFileForProbe(real, project); !ok {
		t.Errorf("a real in-project env_file must remain probe-eligible")
	}

	// Control: a missing-but-lexically-contained leaf with NO symlink in its
	// path must remain eligible (the diagnostic "configured-but-missing" warn
	// case the probe exists to serve).
	missingReal := filepath.Join(project, "missing.env")
	if _, ok := validateEnvFileForProbe(missingReal, project); !ok {
		t.Errorf("a missing in-project env_file (no symlink) must remain probe-eligible")
	}
}

// TestStatEnvFileProbe exercises the shared os.Stat sink wrapper: a real
// in-root file reports (probed, exists); a missing-but-eligible in-root file
// reports (probed, !exists); and an out-of-root / traversal path is never
// probed (probed==false, exists==false) so the diagnostic cannot treat an
// unvalidated path as "missing".
func TestStatEnvFileProbe(t *testing.T) {
	project := t.TempDir()

	real := filepath.Join(project, "real.env")
	if err := os.WriteFile(real, []byte("X=1"), 0o600); err != nil {
		t.Fatalf("write real: %v", err)
	}
	if got, exists, probed := statEnvFileProbe(real, project); !probed || !exists || got != real {
		t.Errorf("real in-project env_file: got (%q, exists=%v, probed=%v), want (%q, true, true)", got, exists, probed, real)
	}

	missing := filepath.Join(project, "missing.env")
	if got, exists, probed := statEnvFileProbe(missing, project); !probed || exists || got != missing {
		t.Errorf("missing in-project env_file: got (%q, exists=%v, probed=%v), want (%q, false, true)", got, exists, probed, missing)
	}

	// Out-of-root and traversal-bearing paths must never be probed.
	for _, p := range []string{"/etc/passwd", filepath.Join(project, "..", "escape.env"), ".env", ""} {
		if got, exists, probed := statEnvFileProbe(p, project); probed || exists {
			t.Errorf("unvalidated path %q must not be probed; got (%q, exists=%v, probed=%v)", p, got, exists, probed)
		}
	}
}

func TestIsValidEnvKey(t *testing.T) {
	valid := []string{"HOME", "MY_VAR", "_private", "A", "API_KEY_123"}
	for _, k := range valid {
		if !IsValidEnvKey(k) {
			t.Errorf("expected %q to be valid", k)
		}
	}
	invalid := []string{"", "123BAD", "HAS SPACE", "key=val", "semi;colon", "a'b"}
	for _, k := range invalid {
		if IsValidEnvKey(k) {
			t.Errorf("expected %q to be invalid", k)
		}
	}
}

func TestBuildEnvExports_SortsQuotesAndSkipsInvalidKeys(t *testing.T) {
	got := buildEnvExports(map[string]string{
		"Z_LAST":  "two words",
		"A_FIRST": "it's",
		"BAD-KEY": "ignored",
	})
	want := `export A_FIRST='it'\''s' && export Z_LAST='two words'`
	if got != want {
		t.Fatalf("buildEnvExports() = %q, want %q", got, want)
	}
}

func TestBuildRestartEnvPrefix(t *testing.T) {
	inst := &Instance{restartEnv: map[string]string{"RESTART_ONLY": "value"}}
	if got, want := inst.buildRestartEnvPrefix(), "export RESTART_ONLY='value' && "; got != want {
		t.Fatalf("buildRestartEnvPrefix() = %q, want %q", got, want)
	}
	inst.restartEnv = nil
	if got := inst.buildRestartEnvPrefix(); got != "" {
		t.Fatalf("cleared restart env produced prefix %q", got)
	}
}

func TestRestartWithEnvRejectsInvalidKeyBeforeRestart(t *testing.T) {
	inst := &Instance{ID: "invalid-env-test"}
	if err := inst.RestartWithEnv(map[string]string{"BAD-KEY": "value"}); err == nil {
		t.Fatal("RestartWithEnv accepted an invalid environment variable name")
	}
	if inst.restartEnv != nil {
		t.Fatalf("invalid restart populated transient environment: %#v", inst.restartEnv)
	}
}
