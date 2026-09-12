package session

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
	"github.com/BurntSushi/toml"
)

// identityTestEnv isolates HOME/XDG so the identity file lands in a temp data
// dir and no real config.toml is read. Returns the temp home.
func identityTestEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("GEMINI_CLI_HOME", "")
	t.Setenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH", "")
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)
	return home
}

func identityTestInstance(tool string) *Instance {
	return &Instance{
		ID:              "abc12345-1700000000",
		Title:           "worker one",
		Tool:            tool,
		GroupPath:       "conductor/workers",
		ProjectPath:     "/tmp/proj",
		ParentSessionID: "parent01-1600000000",
		Account:         "work",
	}
}

func TestBuildIdentityPrompt_ContainsRecordAndCLI(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("codex")
	got := inst.BuildIdentityPrompt()

	for _, want := range []string{
		"You are running inside agent-deck",
		"- session id: abc12345-1700000000",
		"- title: worker one",
		"- tool: codex",
		"- group: conductor/workers",
		"- account: work",
		"- parent session id: parent01-1600000000",
		"- project path: /tmp/proj",
		"agent-deck session current --json",
		"agent-deck session send <id-or-title>",
		"agent-deck launch <path>",
		"agent-deck session children --json",
		"agent-deck inbox drain --json abc12345-1700000000",
		"===AGENTDECK_DONE=== status=<ok|fail> summary=<one line>",
		"$" + IdentityFileEnv,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("identity prompt missing %q:\n%s", want, got)
		}
	}
	// LOCKED DECISION 3: short. Count non-empty lines.
	lines := 0
	for _, l := range strings.Split(got, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines > 40 {
		t.Errorf("identity prompt is %d non-empty lines, want <= 40", lines)
	}
}

func TestBuildIdentityPrompt_RootSessionPlaceholders(t *testing.T) {
	identityTestEnv(t)
	inst := &Instance{ID: "root0001-1700000000", Title: "root", Tool: "claude", ProjectPath: "/tmp/p"}
	got := inst.BuildIdentityPrompt()
	if !strings.Contains(got, "- parent session id: (none: this is a root session)") {
		t.Errorf("root session must say so:\n%s", got)
	}
	if !strings.Contains(got, "- account: (none)") || !strings.Contains(got, "- group: (none)") {
		t.Errorf("empty fields must render as (none):\n%s", got)
	}
}

func TestEnsureIdentityFile_WritesBothNamesUnderDataDir(t *testing.T) {
	home := identityTestEnv(t)
	inst := identityTestInstance("claude")
	dir, file, ok := inst.ensureIdentityFile()
	if !ok {
		t.Fatal("ensureIdentityFile returned !ok with default config")
	}
	wantRoot := filepath.Join(home, ".local", "share", "agent-deck", "runtime", "identity")
	if !strings.HasPrefix(dir, wantRoot) {
		t.Errorf("identity dir %q not under %q", dir, wantRoot)
	}
	if filepath.Base(file) != identityFileName {
		t.Errorf("canonical file = %q, want %s", file, identityFileName)
	}
	for _, name := range []string{identityFileName, geminiIdentityFileName} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if string(data) != inst.BuildIdentityPrompt() {
			t.Errorf("%s content differs from BuildIdentityPrompt", name)
		}
	}
	if _, err := os.Stat(file + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("tmp file left behind after atomic write")
	}
}

func TestEnsureIdentityFile_RegeneratesFromRecord(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("pi")
	_, file, ok := inst.ensureIdentityFile()
	if !ok {
		t.Fatal("first write failed")
	}
	inst.Title = "renamed worker"
	inst.ParentSessionID = ""
	if _, _, ok := inst.ensureIdentityFile(); !ok {
		t.Fatal("second write failed")
	}
	data, _ := os.ReadFile(file)
	if !strings.Contains(string(data), "- title: renamed worker") {
		t.Errorf("rename not reflected in regenerated file:\n%s", data)
	}
	if strings.Contains(string(data), "parent01") {
		t.Errorf("stale parent survived regeneration:\n%s", data)
	}
}

func TestIdentityInjection_OptOuts(t *testing.T) {
	t.Run("per-session flag", func(t *testing.T) {
		identityTestEnv(t)
		inst := identityTestInstance("claude")
		inst.IdentityInjectionDisabled = true
		if inst.identityInjectionEnabled() {
			t.Fatal("IdentityInjectionDisabled must disable injection")
		}
		if got := inst.claudeIdentityFlag(); got != "" {
			t.Errorf("claude flag = %q, want empty", got)
		}
		if got := inst.identityEnvExport(); got != "" {
			t.Errorf("env export = %q, want empty", got)
		}
	})
	t.Run("global config", func(t *testing.T) {
		identityTestEnv(t)
		off := false
		restore := resetUserConfigCache(t, &UserConfig{Launch: LaunchSettings{InjectIdentity: &off}})
		defer restore()
		inst := identityTestInstance("codex")
		if inst.identityInjectionEnabled() {
			t.Fatal("[launch] inject_identity=false must disable injection")
		}
		if got := inst.codexIdentityFlag("", ""); got != "" {
			t.Errorf("codex flag = %q, want empty", got)
		}
	})
	t.Run("ssh and sandbox skipped", func(t *testing.T) {
		identityTestEnv(t)
		ssh := identityTestInstance("claude")
		ssh.SSHHost = "box"
		if ssh.identityInjectionEnabled() {
			t.Error("ssh session must not inject a controller-local file path")
		}
		sb := identityTestInstance("claude")
		sb.Sandbox = &SandboxConfig{Enabled: true}
		if sb.identityInjectionEnabled() {
			t.Error("sandboxed session must not inject a host-local file path")
		}
	})
	t.Run("default on", func(t *testing.T) {
		identityTestEnv(t)
		if !identityTestInstance("claude").identityInjectionEnabled() {
			t.Error("injection must default to on")
		}
	})
}

func TestLaunchSettings_GetInjectIdentityDefaultsTrue(t *testing.T) {
	var nilSettings *LaunchSettings
	if !nilSettings.GetInjectIdentity() {
		t.Error("nil settings must default to true")
	}
	if !(&LaunchSettings{}).GetInjectIdentity() {
		t.Error("unset must default to true")
	}
	off := false
	if (&LaunchSettings{InjectIdentity: &off}).GetInjectIdentity() {
		t.Error("explicit false must be honoured")
	}
}

func TestIdentityInjection_PerHarnessCommands(t *testing.T) {
	identityTestEnv(t)

	t.Run("claude fresh command carries --append-system-prompt-file", func(t *testing.T) {
		inst := identityTestInstance("claude")
		inst.Command = "claude"
		cmd := inst.buildClaudeCommand("claude")
		file, _ := inst.IdentityFilePath()
		if !strings.Contains(cmd, "--append-system-prompt-file "+file) {
			t.Errorf("claude command missing identity flag:\n%s", cmd)
		}
		if !strings.Contains(cmd, "export "+IdentityFileEnv+"="+file) {
			t.Errorf("claude command missing env export:\n%s", cmd)
		}
		// The flag must precede any positional startup query.
		inst.StartupQuery = "hello there"
		cmd = inst.buildClaudeCommand("claude")
		if strings.Index(cmd, "--append-system-prompt-file") > strings.Index(cmd, "'hello there'") {
			t.Errorf("identity flag must come before the startup query:\n%s", cmd)
		}
	})

	t.Run("codex command carries developer_instructions override", func(t *testing.T) {
		inst := identityTestInstance("codex")
		cmd := inst.buildCodexCommand("codex")
		if !strings.Contains(cmd, ` -c 'developer_instructions="`) || !strings.Contains(cmd, `- session id: abc12345-1700000000`) {
			t.Errorf("codex command missing inline identity override:\n%s", cmd)
		}
		if strings.Contains(cmd, "$(") || strings.Contains(cmd, "sed ") {
			t.Errorf("codex override must be inlined, never assembled by a shell pipeline:\n%s", cmd)
		}
		if strings.Contains(cmd, "model_instructions_file") || strings.Contains(cmd, "experimental_instructions_file") {
			t.Errorf("codex must append, never replace the base instructions:\n%s", cmd)
		}
		// Resume keeps the override (a -c override never persists).
		inst.CodexSessionID = "" // fresh path already covered; resume gated on rollout files
		withPrompt, embedded := inst.buildCodexCommandWithPrompt("codex", "do the thing")
		if !embedded || !strings.HasSuffix(withPrompt, " 'do the thing'") {
			t.Fatalf("prompt embedding changed: %v %q", embedded, withPrompt)
		}
		if strings.Index(withPrompt, "developer_instructions") > strings.Index(withPrompt, "'do the thing'") {
			t.Errorf("identity override must precede the positional prompt:\n%s", withPrompt)
		}
	})

	t.Run("codex custom command is passthrough but keeps env export", func(t *testing.T) {
		inst := identityTestInstance("codex")
		cmd := inst.buildCodexCommand("codex --model o3")
		if strings.Contains(cmd, "developer_instructions") {
			t.Errorf("custom codex command must not be rewritten:\n%s", cmd)
		}
		if !strings.Contains(cmd, "export "+IdentityFileEnv+"=") {
			t.Errorf("custom codex command must still export the identity file:\n%s", cmd)
		}
	})

	t.Run("pi command carries --append-system-prompt file", func(t *testing.T) {
		inst := identityTestInstance("pi")
		cmd := inst.buildPiCommand("pi")
		file, _ := inst.IdentityFilePath()
		if !strings.HasSuffix(cmd, "--append-system-prompt "+file) {
			t.Errorf("pi command missing identity flag:\n%s", cmd)
		}
	})

	t.Run("gemini command withholds include-directories until the root is trusted", func(t *testing.T) {
		inst := identityTestInstance("gemini")
		cmd := inst.buildGeminiCommand("gemini")
		if strings.Contains(cmd, "--include-directories") {
			t.Errorf("untrusted identity root must not add the dir (dialog eats the initial message):\n%s", cmd)
		}
		if !strings.Contains(cmd, "agent-deck: warning: gemini: agent-deck identity context skipped") {
			t.Errorf("untrusted root must print the pane hint:\n%s", cmd)
		}
		if strings.Contains(cmd, `"`) && strings.Contains(cmd, "TRUST_FOLDER rule") {
			// paneWarning output is wrapped in bash -c "..." by prepareCommand;
			// double quotes in the hint would render as backslashes.
			if strings.Contains(cmd[strings.Index(cmd, "agent-deck: warning"):strings.Index(cmd, "TRUST_FOLDER rule")], `"`) {
				t.Errorf("hint must not contain double quotes:\n%s", cmd)
			}
		}

		// Trust the identity root the way a user would (trustedFolders.json).
		root, _ := IdentityDir()
		gdir := filepath.Join(os.Getenv("HOME"), ".gemini")
		if err := os.MkdirAll(gdir, 0o755); err != nil {
			t.Fatal(err)
		}
		rules, _ := json.Marshal(map[string]string{root: "TRUST_FOLDER"})
		if err := os.WriteFile(filepath.Join(gdir, "trustedFolders.json"), rules, 0o600); err != nil {
			t.Fatal(err)
		}
		cmd = inst.buildGeminiCommand("gemini")
		dir, _ := inst.identitySessionDir()
		if !strings.HasSuffix(cmd, "--include-directories "+dir) {
			t.Errorf("trusted root must add the identity dir:\n%s", cmd)
		}
		if strings.Contains(cmd, "identity context skipped") {
			t.Errorf("no hint once trusted:\n%s", cmd)
		}
	})

	t.Run("gemini folder trust disabled needs no rule", func(t *testing.T) {
		identityTestEnv(t)
		inst := identityTestInstance("gemini")
		gdir := filepath.Join(os.Getenv("HOME"), ".gemini")
		if err := os.MkdirAll(gdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gdir, "settings.json"), []byte(`{"security":{"folderTrust":{"enabled":false}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(inst.buildGeminiCommand("gemini"), "--include-directories") {
			t.Error("folder trust off must inject without a trust rule")
		}
	})

	t.Run("generic custom tool gets env export only", func(t *testing.T) {
		inst := identityTestInstance("shell")
		inst.Command = "/opt/mytool"
		cmd := inst.buildShellPassthroughCommand("/opt/mytool")
		_ = cmd // passthrough disabled: falls through unchanged
		env := inst.buildEnvSourceCommand()
		if !strings.Contains(env, "export "+IdentityFileEnv+"=") {
			t.Errorf("env prefix missing identity export:\n%s", env)
		}
	})
}

func TestGeminiIdentityDirTrusted_Rules(t *testing.T) {
	identityTestEnv(t)
	root := filepath.Join(t.TempDir(), "identity")
	dir := filepath.Join(root, "sess")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	trustFile := filepath.Join(t.TempDir(), "trusted.json")
	t.Setenv("GEMINI_CLI_TRUSTED_FOLDERS_PATH", trustFile)
	write := func(rules map[string]string) {
		data, _ := json.Marshal(rules)
		if err := os.WriteFile(trustFile, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if geminiIdentityDirTrusted(dir) {
		t.Error("missing rules file must read as untrusted (would prompt)")
	}
	write(map[string]string{})
	if geminiIdentityDirTrusted(dir) {
		t.Error("no matching rule must read as untrusted")
	}
	write(map[string]string{root: "TRUST_FOLDER"})
	if !geminiIdentityDirTrusted(dir) {
		t.Error("TRUST_FOLDER on the root must cover the session dir")
	}
	write(map[string]string{filepath.Join(root, "other"): "TRUST_PARENT"})
	if !geminiIdentityDirTrusted(dir) {
		t.Error("TRUST_PARENT on a sibling must cover the session dir via the parent")
	}
	write(map[string]string{root: "TRUST_FOLDER", dir: "DO_NOT_TRUST"})
	if geminiIdentityDirTrusted(dir) {
		t.Error("longest rule wins: DO_NOT_TRUST on the dir must deny")
	}
	write(map[string]string{filepath.Dir(root) + "x": "TRUST_FOLDER"})
	if geminiIdentityDirTrusted(dir) {
		t.Error("a sibling-prefix path must not match (no string-prefix trust)")
	}
}

func TestIdentityInjectionDisabled_ToolDataRoundTrip(t *testing.T) {
	td := WriteIdentityInjectionDisabledToToolData(json.RawMessage(`{"idle_timeout_secs":5}`), true)
	if !ReadIdentityInjectionDisabledFromToolData(td) {
		t.Fatalf("flag lost: %s", td)
	}
	if !strings.Contains(string(td), `"idle_timeout_secs":5`) {
		t.Errorf("sibling key lost: %s", td)
	}
	td = WriteIdentityInjectionDisabledToToolData(td, false)
	if ReadIdentityInjectionDisabledFromToolData(td) {
		t.Errorf("false must clear the flag: %s", td)
	}
	if strings.Contains(string(td), toolDataIdentityInjectionDisabledKey) {
		t.Errorf("false must delete the key: %s", td)
	}
	if ReadIdentityInjectionDisabledFromToolData(nil) || ReadIdentityInjectionDisabledFromToolData(json.RawMessage("nope")) {
		t.Error("missing/malformed blobs must read as inject (false)")
	}
}

// SQLite round-trip: `--no-identity` must survive save/load so a restart from
// another process still honours it.
func TestIdentityInjectionDisabled_SQLiteRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	storage := newTestStorage(t)

	inst := NewInstance("identity-optout", "/tmp")
	inst.Tool = "pi"
	inst.IdentityInjectionDisabled = true
	other := NewInstance("identity-default", "/tmp")
	other.Tool = "pi"

	groupTree := NewGroupTreeWithGroups([]*Instance{inst, other}, nil)
	if err := storage.SaveWithGroups([]*Instance{inst, other}, groupTree); err != nil {
		t.Fatalf("SaveWithGroups: %v", err)
	}
	loaded, _, err := storage.LoadWithGroups()
	if err != nil {
		t.Fatalf("LoadWithGroups: %v", err)
	}
	byTitle := map[string]*Instance{}
	for _, l := range loaded {
		byTitle[l.Title] = l
	}
	if !byTitle["identity-optout"].IdentityInjectionDisabled {
		t.Error("IdentityInjectionDisabled not preserved across SQLite round-trip")
	}
	if byTitle["identity-default"].IdentityInjectionDisabled {
		t.Error("default session must load with injection enabled")
	}
}

// tomlRoundTrip parses a `key=<toml>` override the way codex's -c does and
// returns the string value.
func tomlRoundTrip(t *testing.T, override string) string {
	t.Helper()
	var m map[string]string
	if err := toml.Unmarshal([]byte(override), &m); err != nil {
		t.Fatalf("override is not valid TOML: %v\n%s", err, override)
	}
	return m["developer_instructions"]
}

// adversarialTitles are record values that broke or would break a
// quote-based encoding: spaces, single and double quotes, backticks, triple
// quotes (which end a TOML literal), backslashes, newlines, tabs, $() and
// non-ASCII.
var adversarialTitles = []string{
	"worker one",
	"worker 'quoted' one",
	`worker "dq" one`,
	"worker `tick` one",
	"worker '''' quoted",
	"a'''b'''c",
	`back\slash \n literal`,
	"line1\nline2\ttabbed",
	"a'b;$(touch MUST_NOT_EXIST)`id`",
	"日本語 🚀",
}

func TestTomlBasicString_RoundTripsAdversarialInput(t *testing.T) {
	for _, in := range append(adversarialTitles, "", "\x00\x01\x7f", "\"\"\"", `\"`) {
		got := tomlRoundTrip(t, "developer_instructions="+tomlBasicString(in))
		if got != in {
			t.Errorf("tomlBasicString(%q) round-tripped to %q", in, got)
		}
	}
}

func TestCodexIdentityOverride_LosslessForAdversarialTitlesAndPaths(t *testing.T) {
	identityTestEnv(t)
	for _, title := range adversarialTitles {
		inst := identityTestInstance("codex")
		inst.Title = title
		inst.ProjectPath = "/tmp/pro ject/'odd' \"dir\"/`x`"
		value, ok := inst.codexIdentityOverride("", "")
		if !ok {
			t.Fatal("override not built")
		}
		got := tomlRoundTrip(t, value)
		wantTitle := "- title: " + strings.TrimSpace(identityOneLine(title))
		if !strings.Contains(got, wantTitle+"\n") {
			t.Errorf("title %q not preserved on one line; got block:\n%s", title, got)
		}
		if !strings.Contains(got, "- project path: "+inst.ProjectPath+"\n") {
			t.Errorf("project path with quotes not preserved:\n%s", got)
		}
		if !strings.HasSuffix(strings.TrimRight(got, "\n"), "regenerated on every start/restart.") {
			t.Errorf("block truncated for title %q:\n%s", title, got)
		}
		if strings.Contains(got, "MUST_NOT_EXIST") {
			if _, err := os.Stat("MUST_NOT_EXIST"); err == nil {
				t.Fatal("command substitution in a title was executed")
			}
		}
		// The shell-level flag is one quoted argv element: split it the way
		// bash would and check the value survives byte for byte.
		flag := inst.codexIdentityFlag("", "")
		args := startupArgs(t, flag)
		if len(args) != 2 || args[0] != "-c" || args[1] != value {
			t.Errorf("codex flag did not survive shell parsing for title %q: %q", title, args)
		}
	}
}

func TestCodexIdentityOverride_PreservesConfiguredDeveloperInstructions(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	existing := "Operator rule: always run make lint.\nSecond \"quoted\" line with 'quotes' and ```fences```."
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("model = \"o3\"\ndeveloper_instructions = "+tomlBasicString(existing)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := identityTestInstance("codex")
	value, ok := inst.codexIdentityOverride(codexHome, "")
	if !ok {
		t.Fatal("override not built")
	}
	got := tomlRoundTrip(t, value)
	if !strings.HasPrefix(got, existing+"\n\n# agent-deck session context") {
		t.Errorf("configured developer_instructions must come first, intact:\n%s", got)
	}
	if !strings.Contains(got, "- session id: abc12345-1700000000") {
		t.Errorf("identity block missing after the configured text:\n%s", got)
	}
	// Whole-command path resolves the same CODEX_HOME the launch exports.
	t.Setenv("CODEX_HOME", codexHome)
	cmd := inst.buildCodexCommand("codex")
	if !strings.Contains(cmd, "Operator rule: always run make lint.") {
		t.Errorf("buildCodexCommand dropped the configured developer_instructions:\n%s", cmd)
	}
	// No configured value: block alone, no leading separator.
	plain, _ := inst.codexIdentityOverride(t.TempDir(), "")
	if !strings.HasPrefix(tomlRoundTrip(t, plain), "# agent-deck session context") {
		t.Errorf("unconfigured home must yield the bare block")
	}
}

func TestIdentityInjection_NeverReplacesExistingInstructions(t *testing.T) {
	identityTestEnv(t)

	t.Run("claude keeps operator extra args and never uses --system-prompt", func(t *testing.T) {
		inst := identityTestInstance("claude")
		inst.ExtraArgs = []string{"--append-system-prompt", "operator rule: keep me"}
		cmd := inst.buildClaudeCommand("claude")
		if !strings.Contains(cmd, "--append-system-prompt 'operator rule: keep me'") {
			t.Errorf("operator --append-system-prompt dropped:\n%s", cmd)
		}
		if strings.Contains(cmd, " --system-prompt ") || strings.Contains(cmd, "--system-prompt-file") {
			t.Errorf("identity must append, never replace claude's system prompt:\n%s", cmd)
		}
		if !strings.Contains(cmd, "--append-system-prompt-file ") {
			t.Errorf("identity flag missing alongside operator args:\n%s", cmd)
		}
	})

	t.Run("conductor keeps its CLAUDE.md and the block defers to it", func(t *testing.T) {
		inst := identityTestInstance("claude")
		inst.IsConductor = true
		block := inst.BuildIdentityPrompt()
		if !strings.Contains(block, "CLAUDE.md, AGENTS.md, GEMINI.md) and from the task you were given take precedence") {
			t.Errorf("block lacks the deference clause:\n%s", block)
		}
		cmd := inst.buildClaudeCommand("claude")
		if strings.Contains(cmd, "CLAUDE.md") || strings.Contains(cmd, "--setting-sources") {
			t.Errorf("injection must not touch memory/settings sources:\n%s", cmd)
		}
	})

	t.Run("gemini never writes into the project and keeps project memory", func(t *testing.T) {
		project := t.TempDir()
		if err := os.WriteFile(filepath.Join(project, "GEMINI.md"), []byte("project rule"), 0o600); err != nil {
			t.Fatal(err)
		}
		inst := identityTestInstance("gemini")
		inst.ProjectPath = project
		gdir := filepath.Join(os.Getenv("HOME"), ".gemini")
		if err := os.MkdirAll(gdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gdir, "settings.json"), []byte(`{"security":{"folderTrust":{"enabled":false}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := inst.buildGeminiCommand("gemini")
		dir, _ := inst.identitySessionDir()
		if !strings.Contains(cmd, "--include-directories "+dir) {
			t.Fatalf("identity dir not added:\n%s", cmd)
		}
		if strings.HasPrefix(dir, project) {
			t.Errorf("identity dir must not live inside the project: %s", dir)
		}
		if got, _ := os.ReadFile(filepath.Join(project, "GEMINI.md")); string(got) != "project rule" {
			t.Errorf("project GEMINI.md modified: %q", got)
		}
		entries, _ := os.ReadDir(project)
		if len(entries) != 1 {
			t.Errorf("project dir gained files: %v", entries)
		}
		if strings.Contains(cmd, "GEMINI_SYSTEM_MD") {
			t.Errorf("must not replace gemini's system prompt:\n%s", cmd)
		}
	})

	t.Run("pi uses the append flag only", func(t *testing.T) {
		inst := identityTestInstance("pi")
		cmd := inst.buildPiCommand("pi")
		if strings.Contains(cmd, " --system-prompt ") {
			t.Errorf("pi must append, never replace:\n%s", cmd)
		}
	})
}

func TestBuildIdentityPrompt_CollapsesControlCharactersInFields(t *testing.T) {
	identityTestEnv(t)
	inst := identityTestInstance("claude")
	inst.Title = "multi\nline\ttitle\x1b[31m"
	inst.GroupPath = "grp\r\nx"
	got := inst.BuildIdentityPrompt()
	if !strings.Contains(got, "- title: multi line title [31m\n") {
		t.Errorf("title not collapsed to one line:\n%s", got)
	}
	if !strings.Contains(got, "- group: grp  x\n") {
		t.Errorf("group not collapsed:\n%s", got)
	}
	lines := 0
	for _, l := range strings.Split(got, "\n") {
		if strings.TrimSpace(l) != "" {
			lines++
		}
	}
	if lines > 40 {
		t.Errorf("block grew to %d lines with hostile fields", lines)
	}
}

// #2237: a session switched to another harness is a NEW instance in a NEW
// harness and must carry ITS identity (not the source's) through the target
// harness's native flag, while the immutable plan stays untouched.
func TestCrossHarnessPlanCommand_CarriesTargetIdentity(t *testing.T) {
	for _, tc := range []struct{ source, target string }{
		{"codex", "claude"}, {"pi", "claude"}, {"claude", "codex"}, {"pi", "codex"}, {"claude", "pi"}, {"codex", "pi"},
	} {
		t.Run(tc.source+"-to-"+tc.target, func(t *testing.T) {
			home := identityTestEnv(t)
			_ = home
			export := syntheticTransferExport(tc.source)
			export.Manifest.Source.SessionID = "source-session"
			plan, err := BuildFreshTargetLaunchPlan(export, FreshTargetLaunchOptions{TargetHarness: tc.target, TargetTitle: "switched 'worker' \"one\""})
			if err != nil {
				t.Fatal(err)
			}
			plan.Prompt = []byte("carried context")
			if err := stageCrossHarnessPayload(plan); err != nil {
				t.Fatal(err)
			}
			nativeArgsBefore := append([]string(nil), plan.NativeArgs...)
			target := &Instance{ID: plan.Target.InstanceID, Tool: plan.Target.Tool, Account: plan.Target.Account, ProjectPath: plan.Target.ProjectPath, Title: plan.Target.Title, GroupPath: plan.Target.GroupPath}
			command, _, err := crossHarnessPlanCommand(target, plan, "")
			if err != nil {
				t.Fatal(err)
			}
			args := target.identityNativeArgs(plan.Target.Tool, plan.Environment["CODEX_HOME"])
			if len(args) != 2 {
				t.Fatalf("no identity argv for target %s", tc.target)
			}
			for _, a := range args {
				if !strings.Contains(command, shellescape.Quote(a)) {
					t.Errorf("command lacks identity argv %q:\n%s", a, command)
				}
			}
			// Identity flags are positional args of the handoff script (after
			// the native args, before the script appends "$payload").
			if strings.LastIndex(command, shellescape.Quote(args[1])) < strings.Index(command, " agent-deck-handoff ") {
				t.Errorf("identity argv must be among the handoff positional args:\n%s", command)
			}
			if !reflect.DeepEqual(plan.NativeArgs, nativeArgsBefore) {
				t.Errorf("immutable plan mutated: %v -> %v", nativeArgsBefore, plan.NativeArgs)
			}
			// The block describes the TARGET instance, never the source.
			file, _ := target.IdentityFilePath()
			block, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"- session id: " + plan.Target.InstanceID, "- tool: " + plan.Target.Tool, "- title: switched 'worker' \"one\""} {
				if !strings.Contains(string(block), want) {
					t.Errorf("target block missing %q:\n%s", want, block)
				}
			}
			if strings.Contains(string(block), export.Manifest.Source.InstanceID) || strings.Contains(string(block), "- tool: "+tc.source) {
				t.Errorf("target block carries the SOURCE identity:\n%s", block)
			}
			if tc.target == "codex" {
				got := tomlRoundTrip(t, args[1])
				if !strings.Contains(got, "- session id: "+plan.Target.InstanceID) {
					t.Errorf("codex override does not decode to the target block:\n%s", got)
				}
			}
			if len(command) > 8192 {
				t.Errorf("command unexpectedly large: %d", len(command))
			}
		})
	}
}

// Full-launch quoting: execute the entire spawn command string under bash
// with a fake `codex` on PATH that records its argv and environment. This is
// the whole command tmux would run (env exports, AGENTDECK_* prefix, the
// identity override), not just the identity flag.
func TestBuildCodexCommand_FullLaunchQuotingIsLosslessUnderBash(t *testing.T) {
	identityTestEnv(t)
	bin := t.TempDir()
	record := filepath.Join(t.TempDir(), "argv.txt")
	fake := "#!/bin/bash\n{ printf 'TITLE=%s\\n' \"$AGENTDECK_TITLE\"; for a in \"$@\"; do printf 'ARG=%s\\n' \"$a\"; done; } > \"$RECORD\"\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	work := t.TempDir()
	for _, title := range adversarialTitles {
		inst := identityTestInstance("codex")
		inst.Title = title
		cmd := inst.buildCodexCommand("codex")
		run := exec.Command("bash", "-c", cmd)
		run.Dir = work
		run.Env = append(os.Environ(), "RECORD="+record)
		if out, err := run.CombinedOutput(); err != nil {
			t.Fatalf("title %q: launch command failed: %v\n%s\n%s", title, err, out, cmd)
		} else if strings.Contains(string(out), "command not found") {
			t.Errorf("title %q: shell evaluated part of the title: %s", title, out)
		}
		got, err := os.ReadFile(record)
		if err != nil {
			t.Fatal(err)
		}
		wantTitle := "TITLE=" + title + "\n"
		if !strings.Contains(string(got), wantTitle) {
			t.Errorf("title %q: AGENTDECK_TITLE not delivered byte for byte:\n%s", title, got)
		}
		if !strings.Contains(string(got), "ARG=developer_instructions=\"") {
			t.Errorf("title %q: identity override argv missing:\n%s", title, got)
		}
		if _, err := os.Stat(filepath.Join(work, "MUST_NOT_EXIST")); err == nil {
			t.Fatalf("title %q: command substitution executed", title)
		}
	}
}

// Codex precedence: a trusted project's .codex/config.toml developer
// instructions replace the user-level ones in codex itself (verified with
// `codex debug prompt-input`), so the override must carry the project layer,
// verbatim and first, and never the shadowed user value.
func TestCodexIdentityOverride_PreservesTrustedProjectLayer(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	root := t.TempDir()
	project := filepath.Join(root, "repo", "sub")
	if err := os.MkdirAll(filepath.Join(root, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "repo", ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	projectDI := "Project rule: \"quoted\" `ticked` never shadow me\nsecond line"
	if err := os.WriteFile(filepath.Join(root, "repo", ".codex", "config.toml"), []byte("developer_instructions = "+tomlBasicString(projectDI)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userCfg := "developer_instructions = \"USER-LAYER\"\n[projects." + tomlBasicString(filepath.Join(root, "repo")) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := identityTestInstance("codex")
	inst.ProjectPath = project
	value, ok := inst.codexIdentityOverride(codexHome, project)
	if !ok {
		t.Fatal("override not built")
	}
	got := tomlRoundTrip(t, value)
	if !strings.HasPrefix(got, projectDI+"\n\n# agent-deck session context") {
		t.Errorf("trusted project developer_instructions must lead, verbatim:\n%s", got)
	}
	if strings.Contains(got, "USER-LAYER") {
		t.Errorf("user layer must not appear when the project layer wins (codex would not apply it):\n%s", got)
	}

	// Untrusted project: codex ignores the project layer, so must we.
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte("developer_instructions = \"USER-LAYER\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, _ = inst.codexIdentityOverride(codexHome, project)
	got = tomlRoundTrip(t, value)
	if strings.Contains(got, "never shadow me") || !strings.HasPrefix(got, "USER-LAYER\n\n") {
		t.Errorf("untrusted project layer must be ignored in favour of the user layer:\n%s", got)
	}

	// Whole command resolves the project from the instance's working dir.
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	if cmd := inst.buildCodexCommand("codex"); !strings.Contains(cmd, "never shadow me") {
		t.Errorf("buildCodexCommand dropped the trusted project developer_instructions:\n%s", cmd)
	}
}

// Acceptance against the real codex binary (skipped where codex is not
// installed, e.g. CI): `codex debug prompt-input` renders the model-visible
// input without a model call. With the agent-deck override in place, the
// project's AGENTS.md must still be delivered verbatim in its own
// user_instructions message, the configured developer_instructions must
// still open the developer message, and the identity block must follow it.
func TestCodexAcceptance_ProjectInstructionsDeliveredWithIdentity(t *testing.T) {
	codexBin, err := exec.LookPath("codex")
	if err != nil {
		t.Skip("codex not installed")
	}
	identityTestEnv(t)
	codexHome := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	agents := "# Project rules\nAGENTS-MD-VERBATIM: when asked for the codeword answer MANGO-11 and nothing else.\n"
	if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(agents), 0o600); err != nil {
		t.Fatal(err)
	}
	userCfg := "developer_instructions = \"USER-LAYER-DI\"\n[projects." + tomlBasicString(project) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := identityTestInstance("codex")
	inst.ProjectPath = project
	value, ok := inst.codexIdentityOverride(codexHome, project)
	if !ok {
		t.Fatal("override not built")
	}
	run := exec.Command(codexBin, "debug", "prompt-input", "-c", value, "ping")
	run.Dir = project
	run.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	out, err := run.Output()
	if err != nil {
		t.Skipf("codex debug prompt-input unavailable: %v", err)
	}
	var items []struct {
		Type    string `json:"type"`
		Role    string `json:"role"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(out, &items); err != nil {
		t.Fatalf("prompt-input is not JSON: %v\n%s", err, out)
	}
	texts := map[string]string{}
	for _, it := range items {
		if it.Type != "message" {
			continue
		}
		for _, c := range it.Content {
			texts[it.Role] += c.Text
		}
	}
	dev := texts["developer"]
	if !strings.HasPrefix(dev, "USER-LAYER-DI\n\n# agent-deck session context") {
		t.Errorf("developer message must open with the configured instructions then the identity block:\n%.300s", dev)
	}
	if !strings.Contains(texts["user"], strings.TrimSpace(agents)) {
		t.Errorf("project AGENTS.md not delivered verbatim in user_instructions:\n%.500s", texts["user"])
	}
	if strings.Contains(dev, "AGENTS-MD-VERBATIM") {
		t.Errorf("AGENTS.md must stay in its own channel, not be folded into the override")
	}
}

// codexConfigReadWithinRoot must fail closed on a real config file that
// escapes the containment root via a ".." segment, and on one reached through
// a symlink-free traversal — using files that EXIST, so a missing-file false
// pass cannot mask a removed barrier. It must still read a legitimately named
// sibling (e.g. "release..backup") inside the root, which a substring ".."
// check would wrongly drop.
func TestCodexConfigReadWithinRoot_ContainmentBarrier(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	legit := filepath.Join(root, "release..backup")
	for _, d := range []string{root, outside, legit} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, di string) string {
		p := filepath.Join(dir, "config.toml")
		if err := os.WriteFile(p, []byte("developer_instructions = "+tomlBasicString(di)+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	inside := write(root, "INSIDE-DI")
	write(outside, "OUTSIDE-DI")
	legitCfg := write(legit, "LEGIT-DI")

	// Contained read works.
	if layer, ok := codexConfigReadWithinRoot(inside, root); !ok || layer.DeveloperInstructions == nil || *layer.DeveloperInstructions != "INSIDE-DI" {
		t.Fatalf("contained config must read: ok=%v layer=%+v", ok, layer)
	}
	// A directory whose NAME merely contains ".." (e.g. "release..backup") is a
	// legitimate path and MUST still be read — os.Root confines the open to
	// root without treating dotted names as traversal. A substring ".." check
	// would wrongly drop it and then inject a replacement developer_instructions
	// over the project's own config; this asserts that does not happen.
	if layer, ok := codexConfigReadWithinRoot(legitCfg, root); !ok || layer.DeveloperInstructions == nil || *layer.DeveloperInstructions != "LEGIT-DI" {
		t.Errorf("legitimate release..backup config must be read (project instructions preserved): ok=%v", ok)
	}
	// An existing config reached by a ".." segment that escapes root is refused.
	escape := filepath.Join(root, "..", "outside", "config.toml")
	if _, err := os.Stat(filepath.Clean(escape)); err != nil {
		t.Fatalf("fixture escape target must exist: %v", err)
	}
	if _, ok := codexConfigReadWithinRoot(escape, root); ok {
		t.Error("a ..-segment path escaping root must be refused even though the file exists")
	}
	// An existing config simply OUTSIDE root (no ".." in the string) is refused
	// by containment alone — proving the HasPrefix(root) barrier, not just the
	// traversal check, is load-bearing.
	if _, ok := codexConfigReadWithinRoot(filepath.Join(outside, "config.toml"), root); ok {
		t.Error("a path outside root must be refused by containment")
	}
}

// Codex empty-override precedence: an explicit developer_instructions = "" in
// a trusted project's .codex/config.toml clears it (codex applies nothing) and
// must NOT resurrect an ancestor/user value; the identity block still rides
// alone. An absent key falls back to the user value, as codex does.
func TestCodexIdentityOverride_EmptyProjectOverrideDoesNotResurrectUserValue(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	userCfg := "developer_instructions = \"USER-LAYER-VALUE\"\n[projects." + tomlBasicString(project) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}

	// (a) explicit empty project override -> block alone, user value gone.
	if err := os.WriteFile(filepath.Join(project, ".codex", "config.toml"), []byte("developer_instructions = \"\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	val, found := codexEffectiveDeveloperInstructions(codexHome, project)
	if !found || val != "" {
		t.Errorf("explicit empty project override must be found and empty, got found=%v val=%q", found, val)
	}
	inst := identityTestInstance("codex")
	inst.ProjectPath = project
	ov, _ := inst.codexIdentityOverride(codexHome, project)
	got := tomlRoundTrip(t, ov)
	if strings.Contains(got, "USER-LAYER-VALUE") {
		t.Errorf("empty project override must not resurrect the user value:\n%s", got)
	}
	if !strings.HasPrefix(got, "# agent-deck session context") {
		t.Errorf("empty project override should leave the block alone at the top:\n%.80s", got)
	}

	// (b) whitespace-only project override is a present value too: preserved
	// verbatim, still no user fallback.
	if err := os.WriteFile(filepath.Join(project, ".codex", "config.toml"), []byte("developer_instructions = \"   \"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	val, found = codexEffectiveDeveloperInstructions(codexHome, project)
	if !found || val != "   " {
		t.Errorf("whitespace override must be preserved verbatim, got found=%v val=%q", found, val)
	}

	// (c) absent key -> user value applies (codex fallback).
	if err := os.WriteFile(filepath.Join(project, ".codex", "config.toml"), []byte("model = \"o3\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	val, found = codexEffectiveDeveloperInstructions(codexHome, project)
	if !found || val != "USER-LAYER-VALUE" {
		t.Errorf("absent project key must fall back to the user value, got found=%v val=%q", found, val)
	}
}

// An empty or whitespace-only project AGENTS.md is codex's own
// user_instructions channel and is never touched by the identity override:
// prove the developer override is byte-identical whether AGENTS.md is empty,
// whitespace, or absent, so injection cannot "replace" the project's file.
func TestCodexIdentityOverride_DoesNotDependOnAgentsMd(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	project := t.TempDir() // one fixed project so the block's path line is constant
	overrideFor := func() string {
		inst := identityTestInstance("codex")
		inst.ID = "fixed0001-1700000000"
		inst.Title = "fixed"
		inst.ProjectPath = project
		ov, ok := inst.codexIdentityOverride(codexHome, project)
		if !ok {
			t.Fatal("override not built")
		}
		return ov
	}
	base := overrideFor() // no AGENTS.md
	for _, content := range []string{"", "   \n\t", "# real rules\ndo the thing"} {
		if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := overrideFor(); got != base {
			t.Errorf("override changed with AGENTS.md content %q — the file must be untouched by injection", content)
		}
	}
}

// Per-harness precedence, at the command level: the identity block never
// suppresses the harness's own project-instruction channel, and it carries a
// deference clause. This is the unit-level counterpart to the live
// conflicting-codeword acceptance recorded in the results bundle.
func TestIdentityInjection_ProjectInstructionChannelsCoexist(t *testing.T) {
	identityTestEnv(t)
	block := identityTestInstance("claude").BuildIdentityPrompt()
	if !strings.Contains(block, "CLAUDE.md, AGENTS.md, GEMINI.md) and from the task you were given take precedence") {
		t.Fatalf("deference clause missing:\n%s", block)
	}

	t.Run("claude append-only, keeps operator + never --system-prompt", func(t *testing.T) {
		inst := identityTestInstance("claude")
		inst.ExtraArgs = []string{"--append-system-prompt", "operator says X"}
		cmd := inst.buildClaudeCommand("claude")
		if !strings.Contains(cmd, "--append-system-prompt 'operator says X'") || !strings.Contains(cmd, "--append-system-prompt-file ") {
			t.Errorf("operator instruction or identity flag missing:\n%s", cmd)
		}
		if strings.Contains(cmd, " --system-prompt ") || strings.Contains(cmd, "--system-prompt-file") || strings.Contains(cmd, "--setting-sources") {
			t.Errorf("claude injection must not replace prompt/settings:\n%s", cmd)
		}
	})

	t.Run("codex keeps project AGENTS.md channel (never folded into the override)", func(t *testing.T) {
		project := t.TempDir()
		if err := os.WriteFile(filepath.Join(project, "AGENTS.md"), []byte("CODEWORD=MANGO-11"), 0o600); err != nil {
			t.Fatal(err)
		}
		inst := identityTestInstance("codex")
		inst.ProjectPath = project
		ov, _ := inst.codexIdentityOverride("", project)
		if strings.Contains(tomlRoundTrip(t, ov), "CODEWORD=MANGO-11") {
			t.Errorf("AGENTS.md must not be folded into developer_instructions:\n%s", tomlRoundTrip(t, ov))
		}
	})

	t.Run("gemini keeps the project's own GEMINI.md and writes nothing to the project", func(t *testing.T) {
		project := t.TempDir()
		if err := os.WriteFile(filepath.Join(project, "GEMINI.md"), []byte("project rule"), 0o600); err != nil {
			t.Fatal(err)
		}
		gdir := filepath.Join(os.Getenv("HOME"), ".gemini")
		_ = os.MkdirAll(gdir, 0o755)
		_ = os.WriteFile(filepath.Join(gdir, "settings.json"), []byte(`{"security":{"folderTrust":{"enabled":false}}}`), 0o600)
		inst := identityTestInstance("gemini")
		inst.ProjectPath = project
		cmd := inst.buildGeminiCommand("gemini")
		dir, _ := inst.identitySessionDir()
		if !strings.Contains(cmd, "--include-directories "+dir) || strings.HasPrefix(dir, project) {
			t.Errorf("identity dir must be outside the project:\n%s", cmd)
		}
		if got, _ := os.ReadFile(filepath.Join(project, "GEMINI.md")); string(got) != "project rule" {
			t.Errorf("project GEMINI.md changed: %q", got)
		}
		if ents, _ := os.ReadDir(project); len(ents) != 1 {
			t.Errorf("project dir gained files: %v", ents)
		}
	})

	t.Run("pi append-only", func(t *testing.T) {
		if strings.Contains(identityTestInstance("pi").buildPiCommand("pi"), " --system-prompt ") {
			t.Error("pi must append, not replace")
		}
	})
}

// Reviewer item (round 4): a trusted project whose directory NAME contains
// ".." (e.g. "release..backup") must keep its configured developer_instructions
// — injection must not skip the project config and then override with a
// block-only replacement. os.Root reads the legit path, so the project value
// leads the override.
func TestCodexIdentityOverride_PreservesLegitDottedProjectPath(t *testing.T) {
	identityTestEnv(t)
	codexHome := t.TempDir()
	base := t.TempDir()
	repo := filepath.Join(base, "release..backup")
	project := filepath.Join(repo, "sub")
	for _, d := range []string{filepath.Join(repo, ".git"), filepath.Join(repo, ".codex"), project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projDI := "Project rule from a release..backup checkout: codeword is APRICOT-9"
	if err := os.WriteFile(filepath.Join(repo, ".codex", "config.toml"), []byte("developer_instructions = "+tomlBasicString(projDI)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userCfg := "developer_instructions = \"USER-LAYER\"\n[projects." + tomlBasicString(repo) + "]\ntrust_level = \"trusted\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(userCfg), 0o600); err != nil {
		t.Fatal(err)
	}
	inst := identityTestInstance("codex")
	inst.ProjectPath = project
	value, ok := inst.codexIdentityOverride(codexHome, project)
	if !ok {
		t.Fatal("override not built")
	}
	got := tomlRoundTrip(t, value)
	if !strings.HasPrefix(got, projDI+"\n\n# agent-deck session context") {
		t.Errorf("release..backup project developer_instructions must lead, not be dropped for a block-only replacement:\n%s", got)
	}
	if strings.Contains(got, "USER-LAYER") {
		t.Errorf("must not resurrect the user layer when the project layer is present:\n%s", got)
	}
}
