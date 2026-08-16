package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
)

// DeepSeek Harness (`dsh`) integration tests.
//
// Every command string asserted here was checked against the real
// @deepseek-ai/dsh 0.1.0-rc.6 binary in a sandboxed HOME (see
// docs/tools/deepseek.md for the captures). The invariants that matter:
//
//   - the binary is `dsh`, not `deepseek` — the tool name would not resolve;
//   - launcher flags (--profile, --patch) precede app arguments, because dsh's
//     parser stops owning tokens at the first one it does not recognize;
//   - only the headless profile takes a task positionally;
//   - a resume flag is emitted ONLY when configured, since neither shipped
//     profile accepts one and dsh would exit with a usage error.

// withConfig installs a UserConfig for the duration of a test and restores the
// previous cache afterwards. Mirrors the crush_test idiom.
func withConfig(t *testing.T, cfg *UserConfig) {
	t.Helper()
	oldCache := userConfigCache
	oldRegistry, oldRegistryCfg := registryCache, registryCacheCfg
	t.Cleanup(func() {
		userConfigCache = oldCache
		registryMu.Lock()
		registryCache, registryCacheCfg = oldRegistry, oldRegistryCfg
		registryMu.Unlock()
	})
	userConfigCache = cfg
	registryMu.Lock()
	registryCache, registryCacheCfg = nil, nil
	registryMu.Unlock()
}

func TestDeepSeekProfileMode(t *testing.T) {
	tests := []struct {
		profile string
		want    DeepSeekMode
	}{
		{"web", deepSeekModeWeb},
		{"WEB", deepSeekModeWeb},
		{"", deepSeekModeWeb}, // unset falls back to the default profile, which is web
		{"headless", deepSeekModeHeadless},
		{"  headless  ", deepSeekModeHeadless},
		{"tui", deepSeekModeCustom},
		{"my-profile", deepSeekModeCustom},
	}
	for _, tt := range tests {
		if got := DeepSeekProfileMode(tt.profile); got != tt.want {
			t.Errorf("DeepSeekProfileMode(%q) = %q, want %q", tt.profile, got, tt.want)
		}
	}
}

func TestDeepSeekOptions_ToolName(t *testing.T) {
	opts := &DeepSeekOptions{}
	if got := opts.ToolName(); got != "deepseek" {
		t.Errorf("ToolName() = %q, want %q", got, "deepseek")
	}
}

func TestDeepSeekOptions_ToArgs(t *testing.T) {
	tests := []struct {
		name string
		opts DeepSeekOptions
		want []string
	}{
		{
			name: "empty",
			opts: DeepSeekOptions{},
			want: nil,
		},
		{
			name: "profile only",
			opts: DeepSeekOptions{Profile: "headless"},
			want: []string{"--profile", "headless"},
		},
		{
			name: "patches are repeatable and keep argv order",
			opts: DeepSeekOptions{Profile: "web", Patches: []string{"a.yml", "b.yml"}},
			want: []string{"--profile", "web", "--patch", "a.yml", "--patch", "b.yml"},
		},
		{
			name: "web app flags follow the launcher flags",
			opts: DeepSeekOptions{Profile: "web", Host: "127.0.0.1", Port: intPtr(8080)},
			want: []string{"--profile", "web", "--host", "127.0.0.1", "--port", "8080"},
		},
		{
			name: "trusted hosts are repeatable",
			opts: DeepSeekOptions{Profile: "web", TrustedHosts: []string{"a.example", "b.example:8080"}},
			want: []string{"--profile", "web", "--trusted-host", "a.example", "--trusted-host", "b.example:8080"},
		},
		{
			name: "web flags are dropped for a non-web profile that cannot parse them",
			opts: DeepSeekOptions{Profile: "headless", Host: "127.0.0.1", Port: intPtr(8080)},
			want: []string{"--profile", "headless"},
		},
		{
			// 0 is meaningful upstream ("let the OS pick a free port"), so it
			// must survive as a real request rather than reading as unset.
			name: "explicit port 0 is emitted",
			opts: DeepSeekOptions{Profile: "web", Port: intPtr(0)},
			want: []string{"--profile", "web", "--port", "0"},
		},
		{
			name: "unset port emits nothing",
			opts: DeepSeekOptions{Profile: "web"},
			want: []string{"--profile", "web"},
		},
		{
			name: "extra args come last",
			opts: DeepSeekOptions{Profile: "web", Port: intPtr(3080), ExtraArgs: []string{"--verbose"}},
			want: []string{"--profile", "web", "--port", "3080", "--verbose"},
		},
		{
			name: "blank entries are skipped",
			opts: DeepSeekOptions{Profile: "web", Patches: []string{"", "  "}, TrustedHosts: []string{""}, ExtraArgs: []string{" "}},
			want: []string{"--profile", "web"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.opts.ToArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ToArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnmarshalDeepSeekOptions(t *testing.T) {
	opts := &DeepSeekOptions{Profile: "headless", Patches: []string{"x.yml"}}
	raw, err := MarshalToolOptions(opts)
	if err != nil {
		t.Fatalf("MarshalToolOptions: %v", err)
	}

	got, err := UnmarshalDeepSeekOptions(raw)
	if err != nil {
		t.Fatalf("UnmarshalDeepSeekOptions: %v", err)
	}
	if got == nil || got.Profile != "headless" || !reflect.DeepEqual(got.Patches, []string{"x.yml"}) {
		t.Fatalf("round-trip lost data: %+v", got)
	}

	// A wrapper for a different tool must yield (nil, nil), not another tool's
	// options reinterpreted as ours.
	other, err := MarshalToolOptions(&CrushOptions{})
	if err != nil {
		t.Fatalf("MarshalToolOptions(crush): %v", err)
	}
	mismatched, err := UnmarshalDeepSeekOptions(other)
	if err != nil {
		t.Fatalf("UnmarshalDeepSeekOptions(crush wrapper): %v", err)
	}
	if mismatched != nil {
		t.Errorf("UnmarshalDeepSeekOptions on a crush wrapper = %+v, want nil", mismatched)
	}

	// Empty input is not an error.
	if got, err := UnmarshalDeepSeekOptions(nil); err != nil || got != nil {
		t.Errorf("UnmarshalDeepSeekOptions(nil) = (%+v, %v), want (nil, nil)", got, err)
	}
}

func TestGetToolCommand_DeepSeekResolvesToDsh(t *testing.T) {
	withConfig(t, &UserConfig{})
	if got := GetToolCommand("deepseek"); got != "dsh" {
		t.Errorf("GetToolCommand(\"deepseek\") = %q, want %q — the tool is named for the "+
			"vendor but the binary is dsh; returning the tool name spawns nothing", got, "dsh")
	}

	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Command: "/opt/dsh/bin/dsh"}})
	if got := GetToolCommand("deepseek"); got != "/opt/dsh/bin/dsh" {
		t.Errorf("configured command override = %q, want %q", got, "/opt/dsh/bin/dsh")
	}
}

func TestGetToolIcon_DeepSeekIsDistinct(t *testing.T) {
	withConfig(t, &UserConfig{})
	icon := GetToolIcon("deepseek")
	if icon == "" {
		t.Fatal("GetToolIcon(\"deepseek\") is empty")
	}
	if icon == GetToolIcon("shell") {
		t.Errorf("GetToolIcon(\"deepseek\") = %q equals the shell fallback", icon)
	}
}

func TestRegistry_DeepSeekIsBuiltinAndMatches(t *testing.T) {
	r := Init(nil)
	if !r.IsBuiltin("deepseek") {
		t.Fatal("deepseek is not registered as a built-in")
	}
	tests := []struct {
		cmd  string
		want string
	}{
		{"dsh", "deepseek"},
		{"dsh --profile web", "deepseek"},
		{"npx @deepseek-ai/dsh web", "deepseek"},
		// "dsh" is token-matched, not substring-matched: these must NOT be
		// claimed by deepseek.
		{"dshell", "shell"},
		{"/usr/bin/fdshx", "shell"},
	}
	for _, tt := range tests {
		if got := r.Match(tt.cmd); got != tt.want {
			t.Errorf("Match(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}

func TestRegistry_DeepSeekInstalledProbeUsesDshNotToolName(t *testing.T) {
	withConfig(t, &UserConfig{})

	oldLookPath := lookPathFn
	t.Cleanup(func() { lookPathFn = oldLookPath })

	var probed []string
	lookPathFn = func(name string) (string, error) {
		probed = append(probed, name)
		if name == "dsh" {
			return "/usr/local/bin/dsh", nil
		}
		return "", os.ErrNotExist
	}

	r := InitFiltered(nil, true, nil)
	if !r.IsVisible("deepseek") {
		t.Error("deepseek is hidden by show_only_installed_tools even though dsh resolves on PATH")
	}
	found := false
	for _, name := range probed {
		if name == "dsh" {
			found = true
		}
		if name == "deepseek" {
			t.Errorf("probe looked up %q; the binary is dsh", name)
		}
	}
	if !found {
		t.Error("probe never looked up dsh")
	}
}

func TestBuildDeepSeekCommand_DefaultProfileIsWeb(t *testing.T) {
	withConfig(t, &UserConfig{})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	cmd := inst.buildDeepSeekCommand("deepseek")

	if !strings.Contains(cmd, "dsh --profile web") {
		t.Errorf("command %q does not launch `dsh --profile web`", cmd)
	}
	if strings.Contains(cmd, "--resume") {
		t.Errorf("command %q emits --resume with resume_flag unset; no shipped dsh profile accepts it", cmd)
	}
	if !strings.Contains(cmd, "AGENTDECK_INSTANCE_ID=abc") {
		t.Errorf("command %q is missing the AGENTDECK_* identity env", cmd)
	}
}

func TestBuildDeepSeekCommand_LauncherFlagsPrecedeAppArgs(t *testing.T) {
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Profile:      "web",
		Patches:      []string{"/tmp/extra.yml"},
		Host:         "127.0.0.1",
		Port:         intPtr(8080),
		TrustedHosts: []string{"deck.local"},
	}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	cmd := inst.buildDeepSeekCommand("deepseek")

	profileIdx := strings.Index(cmd, "--profile")
	patchIdx := strings.Index(cmd, "--patch")
	hostIdx := strings.Index(cmd, "--host")
	portIdx := strings.Index(cmd, "--port")
	trustedIdx := strings.Index(cmd, "--trusted-host")
	for name, idx := range map[string]int{"--profile": profileIdx, "--patch": patchIdx, "--host": hostIdx, "--port": portIdx, "--trusted-host": trustedIdx} {
		if idx < 0 {
			t.Fatalf("command %q is missing %s", cmd, name)
		}
	}
	// dsh's parser hands EVERYTHING after the first unrecognized token to the
	// booted app, so a --patch emitted after --host would be parsed by the web
	// app (which rejects it) instead of the launcher.
	if !(profileIdx < patchIdx && patchIdx < hostIdx && hostIdx < portIdx && portIdx < trustedIdx) {
		t.Errorf("launcher flags do not precede app args in %q", cmd)
	}
}

func TestBuildDeepSeekCommand_CustomCommandPassthrough(t *testing.T) {
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Profile: "headless"}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	cmd := inst.buildDeepSeekCommand("my-dsh-wrapper --thing")

	if !strings.HasSuffix(cmd, "my-dsh-wrapper --thing") {
		t.Errorf("command %q did not pass the custom command through verbatim", cmd)
	}
	if strings.Contains(cmd, "--profile") {
		t.Errorf("command %q injected flags into a custom passthrough command", cmd)
	}
	// The identity env must survive the passthrough — a wrapper still needs to
	// find its session (the #951 review finding on the codex path).
	if !strings.Contains(cmd, "AGENTDECK_INSTANCE_ID=abc") {
		t.Errorf("passthrough command %q dropped the AGENTDECK_* identity env", cmd)
	}
}

func TestBuildDeepSeekCommandWithPrompt(t *testing.T) {
	tests := []struct {
		name         string
		profile      string
		base         string
		prompt       string
		wantEmbedded bool
		wantContains string
	}{
		{
			name:         "headless takes the task positionally",
			profile:      "headless",
			base:         "deepseek",
			prompt:       "run the tests",
			wantEmbedded: true,
			wantContains: "--profile headless 'run the tests'",
		},
		{
			name:         "web cannot carry a prompt on the command line",
			profile:      "web",
			base:         "deepseek",
			prompt:       "run the tests",
			wantEmbedded: false,
		},
		{
			name:         "a custom profile owns its own grammar",
			profile:      "tui",
			base:         "deepseek",
			prompt:       "run the tests",
			wantEmbedded: false,
		},
		{
			name:         "empty prompt embeds nothing",
			profile:      "headless",
			base:         "deepseek",
			prompt:       "   ",
			wantEmbedded: false,
		},
		{
			name:         "custom command passthrough never embeds",
			profile:      "headless",
			base:         "my-wrapper",
			prompt:       "run the tests",
			wantEmbedded: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Profile: tt.profile}})
			inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
			cmd, embedded := inst.buildDeepSeekCommandWithPrompt(tt.base, tt.prompt)
			if embedded != tt.wantEmbedded {
				t.Fatalf("embedded = %v, want %v (cmd: %q)", embedded, tt.wantEmbedded, cmd)
			}
			if tt.wantContains != "" && !strings.Contains(cmd, tt.wantContains) {
				t.Errorf("command %q does not contain %q", cmd, tt.wantContains)
			}
			if !embedded && strings.Contains(cmd, tt.prompt) && strings.TrimSpace(tt.prompt) != "" {
				t.Errorf("command %q carries the prompt but reported it as not embedded", cmd)
			}
		})
	}
}

func TestBuildDeepSeekCommand_ShellMetacharactersAreQuoted(t *testing.T) {
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{
		Profile: "headless",
		Patches: []string{"/tmp/a b; touch /tmp/pwned.yml"},
	}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	cmd, _ := inst.buildDeepSeekCommandWithPrompt("deepseek", "hi; rm -rf /tmp/nope")

	// The command runs through `bash -c`, so an unquoted `;` in either the patch
	// path or the prompt would terminate the dsh invocation and run the rest.
	// Both must appear as single-quoted words, i.e. exactly what
	// shellescape.Quote produces for them.
	wantPatch := shellescape.Quote("/tmp/a b; touch /tmp/pwned.yml")
	wantPrompt := shellescape.Quote("hi; rm -rf /tmp/nope")
	if !strings.Contains(cmd, wantPatch) {
		t.Errorf("patch path is not shell-quoted in %q (want the token %s)", cmd, wantPatch)
	}
	if !strings.Contains(cmd, wantPrompt) {
		t.Errorf("prompt is not shell-quoted in %q (want the token %s)", cmd, wantPrompt)
	}
	// And nothing may appear outside those quoted words: strip them and the
	// remainder must carry no shell metacharacter that could start a command.
	remainder := strings.ReplaceAll(strings.ReplaceAll(cmd, wantPatch, ""), wantPrompt, "")
	remainder = strings.ReplaceAll(remainder, "COLORFGBG='15;0'", "") // an unrelated, already-quoted export
	if strings.Contains(remainder, "; touch") || strings.Contains(remainder, "; rm") {
		t.Errorf("a shell metacharacter escaped quoting in %q", cmd)
	}
}

func TestDeepSeekResume_OffByDefault_OnWhenConfigured(t *testing.T) {
	withConfig(t, &UserConfig{})
	if DeepSeekSupportsResume() {
		t.Error("resume is on by default; no shipped dsh profile accepts a resume flag")
	}

	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Profile: "tui", ResumeFlag: "--resume"}})
	if !DeepSeekSupportsResume() {
		t.Fatal("resume is off with resume_flag configured")
	}

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds", DeepSeekSessionID: "session-42"}
	cmd := inst.buildDeepSeekResumeCommand()
	if !strings.Contains(cmd, "--resume session-42") {
		t.Errorf("resume command %q does not carry the discovered session", cmd)
	}
	// Still after the launcher flags.
	if strings.Index(cmd, "--profile") > strings.Index(cmd, "--resume") {
		t.Errorf("--resume precedes --profile in %q; dsh would hand --profile to the app", cmd)
	}
}

func TestBuildDeepSeekResumeCommand_NoResumeFlagMeansFreshBoot(t *testing.T) {
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{Profile: "web"}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds", DeepSeekSessionID: "session-42"}
	resume := inst.buildDeepSeekResumeCommand()
	fresh := inst.buildDeepSeekCommand("deepseek")

	if resume != fresh {
		t.Errorf("with resume_flag unset the restart command must equal a fresh launch\n resume: %q\n fresh:  %q", resume, fresh)
	}
	if strings.Contains(resume, "session-42") {
		t.Errorf("restart command %q leaks a session id dsh cannot accept", resume)
	}
}

func TestDeepSeekHome_ExportedOnlyWhenExplicit(t *testing.T) {
	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	if got := inst.deepSeekHomeToExport(); got != "" {
		t.Errorf("deepSeekHomeToExport() = %q with nothing configured; want \"\" so dsh resolves its own default", got)
	}
	if !strings.Contains(inst.buildDeepSeekCommand("deepseek"), "dsh --profile") {
		t.Error("command lost its invocation")
	}
	if strings.Contains(inst.buildDeepSeekCommand("deepseek"), "DSH_HOME=") {
		t.Error("DSH_HOME exported when nothing is explicit")
	}
}

func TestDeepSeekHome_PriorityChain(t *testing.T) {
	dir := t.TempDir()
	account := filepath.Join(dir, "account")
	conductor := filepath.Join(dir, "conductor")
	group := filepath.Join(dir, "group")
	global := filepath.Join(dir, "global")
	env := filepath.Join(dir, "env")

	cfg := &UserConfig{
		DeepSeek: DeepSeekSettings{ConfigDir: global},
		Profiles: map[string]ProfileSettings{
			"work": {DeepSeek: ProfileDeepSeekSettings{ConfigDir: account}},
		},
		Conductors: map[string]ConductorOverrides{
			"boss": {DeepSeek: ConductorDeepSeekSettings{ConfigDir: conductor}},
		},
		Groups: map[string]GroupSettings{
			"team": {DeepSeek: GroupDeepSeekSettings{ConfigDir: group}},
		},
	}

	t.Setenv("DSH_HOME", env)
	withConfig(t, cfg)

	// Account beats everything.
	inst := &Instance{Tool: "deepseek", Account: "work", GroupPath: "team", Title: "conductor-boss"}
	if got := inst.deepSeekHomeToExport(); got != account {
		t.Errorf("account level: got %q, want %q", got, account)
	}

	// Without an account, the conductor level wins.
	inst = &Instance{Tool: "deepseek", GroupPath: "team", Title: "conductor-boss"}
	if got := inst.deepSeekHomeToExport(); got != conductor {
		t.Errorf("conductor level: got %q, want %q", got, conductor)
	}

	// Without a conductor, the group level wins.
	inst = &Instance{Tool: "deepseek", GroupPath: "team", Title: "plain"}
	if got := inst.deepSeekHomeToExport(); got != group {
		t.Errorf("group level: got %q, want %q", got, group)
	}

	// With no scoped override, $DSH_HOME from the environment wins over global.
	inst = &Instance{Tool: "deepseek", Title: "plain"}
	if got := inst.deepSeekHomeToExport(); got != env {
		t.Errorf("env level: got %q, want %q", got, env)
	}

	// And with no env, the global config value.
	t.Setenv("DSH_HOME", "")
	if got := inst.deepSeekHomeToExport(); got != global {
		t.Errorf("global level: got %q, want %q", got, global)
	}
}

func TestBuildDeepSeekCommand_ExportsDshHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "dsh-home")
	t.Setenv("DSH_HOME", "")
	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{ConfigDir: home}})

	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}
	cmd := inst.buildDeepSeekCommand("deepseek")

	if !strings.Contains(cmd, "DSH_HOME="+home) {
		t.Errorf("command %q does not export the resolved DSH_HOME", cmd)
	}
	// The export must precede the binary, or it is an argument rather than an
	// environment assignment.
	if strings.Index(cmd, "DSH_HOME=") > strings.Index(cmd, "dsh --profile") {
		t.Errorf("DSH_HOME is emitted after the binary in %q", cmd)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Errorf("resolved DSH_HOME %q was not created: %v", home, err)
	}
}

// --- on-disk session discovery ----------------------------------------------

// writeDshHome builds a $DSH_HOME with the workspace index and session bodies
// dsh 0.1.0-rc.6 actually writes:
//
//	<home>/storages/workspace.json
//	<home>/sessions/<slug>/<session-id>/session.jsonl.zstd
func writeDshHome(t *testing.T, workspace string, sessionIDs []string, bodies []string) string {
	t.Helper()
	home := t.TempDir()

	doc := map[string]any{
		"unit": map[string]any{"name": "workspace", "version": 2},
		"tables": map[string]any{
			"workspaces": map[string]any{
				"06ce661c-81ca-4f4c-a9cd-2c57a941c800": map[string]any{
					"path":       workspace,
					"title":      filepath.Base(workspace),
					"sessionIds": sessionIDs,
				},
			},
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal workspace doc: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "storages"), 0o755); err != nil {
		t.Fatalf("mkdir storages: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "storages", "workspace.json"), data, 0o644); err != nil {
		t.Fatalf("write workspace doc: %v", err)
	}

	slug := "--slugified-workspace--"
	for _, id := range bodies {
		dir := filepath.Join(home, "sessions", slug, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir session body: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "session.jsonl.zstd"), []byte("x"), 0o644); err != nil {
			t.Fatalf("write session body: %v", err)
		}
	}
	return home
}

func TestDiscoverDeepSeekSessionID(t *testing.T) {
	workspace := t.TempDir()
	ids := []string{"session-old", "session-new"}

	t.Run("newest session with a body on disk", func(t *testing.T) {
		home := writeDshHome(t, workspace, ids, ids)
		if got := DiscoverDeepSeekSessionID(home, workspace); got != "session-new" {
			t.Errorf("got %q, want %q (sessionIds is append-ordered, newest last)", got, "session-new")
		}
	})

	t.Run("falls back past a pruned body", func(t *testing.T) {
		// The newest ID is still in the index but its directory was pruned.
		// Resuming it is the #756 stale-sid failure, so discovery must skip it.
		home := writeDshHome(t, workspace, ids, []string{"session-old"})
		if got := DiscoverDeepSeekSessionID(home, workspace); got != "session-old" {
			t.Errorf("got %q, want %q", got, "session-old")
		}
	})

	t.Run("no bodies at all means no resume", func(t *testing.T) {
		home := writeDshHome(t, workspace, ids, nil)
		if got := DiscoverDeepSeekSessionID(home, workspace); got != "" {
			t.Errorf("got %q, want \"\" so restart boots fresh", got)
		}
	})

	t.Run("a different workspace does not match", func(t *testing.T) {
		home := writeDshHome(t, workspace, ids, ids)
		if got := DiscoverDeepSeekSessionID(home, t.TempDir()); got != "" {
			t.Errorf("got %q for an unrelated workspace, want \"\"", got)
		}
	})

	t.Run("missing index fails closed", func(t *testing.T) {
		if got := DiscoverDeepSeekSessionID(t.TempDir(), workspace); got != "" {
			t.Errorf("got %q with no index, want \"\"", got)
		}
	})

	t.Run("corrupt index fails closed", func(t *testing.T) {
		home := t.TempDir()
		if err := os.MkdirAll(filepath.Join(home, "storages"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "storages", "workspace.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := DiscoverDeepSeekSessionID(home, workspace); got != "" {
			t.Errorf("got %q from a corrupt index, want \"\"", got)
		}
	})

	t.Run("empty inputs fail closed", func(t *testing.T) {
		if got := DiscoverDeepSeekSessionID("", workspace); got != "" {
			t.Errorf("got %q for an empty home, want \"\"", got)
		}
		if got := DiscoverDeepSeekSessionID(t.TempDir(), ""); got != "" {
			t.Errorf("got %q for an empty workspace, want \"\"", got)
		}
	})

	t.Run("a symlinked workspace resolves to the same session", func(t *testing.T) {
		// Multi-repo sessions reach their working dir through a symlink, so the
		// path agent-deck holds and the path dsh recorded can differ textually.
		home := writeDshHome(t, workspace, ids, ids)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(workspace, link); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if got := DiscoverDeepSeekSessionID(home, link); got != "session-new" {
			t.Errorf("got %q through a symlinked workspace, want %q", got, "session-new")
		}
	})
}

func TestDeepSeekSessionIDs(t *testing.T) {
	workspace := t.TempDir()
	ids := []string{"session-a", "session-b"}
	home := writeDshHome(t, workspace, ids, ids)

	got := DeepSeekSessionIDs(home, workspace)
	if !reflect.DeepEqual(got, ids) {
		t.Errorf("DeepSeekSessionIDs = %v, want %v (index order preserved)", got, ids)
	}
	if got := DeepSeekSessionIDs(home, t.TempDir()); len(got) != 0 {
		t.Errorf("DeepSeekSessionIDs for an unrelated workspace = %v, want empty", got)
	}
	if got := DeepSeekSessionIDs("", workspace); got != nil {
		t.Errorf("DeepSeekSessionIDs with no home = %v, want nil", got)
	}
}

func TestRefreshDeepSeekSessionID_NoOpWithoutResumeFlag(t *testing.T) {
	workspace := t.TempDir()
	home := writeDshHome(t, workspace, []string{"session-x"}, []string{"session-x"})

	t.Setenv("DSH_HOME", home)
	withConfig(t, &UserConfig{})

	inst := &Instance{Tool: "deepseek", ProjectPath: workspace, DeepSeekSessionID: "stale"}
	inst.refreshDeepSeekSessionID()
	if inst.DeepSeekSessionID != "" {
		t.Errorf("DeepSeekSessionID = %q with resume off; want cleared so nothing stale survives", inst.DeepSeekSessionID)
	}

	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{ResumeFlag: "--resume"}})
	inst.refreshDeepSeekSessionID()
	if inst.DeepSeekSessionID != "session-x" {
		t.Errorf("DeepSeekSessionID = %q, want %q", inst.DeepSeekSessionID, "session-x")
	}
}

func TestDeepSeekProfileBundles(t *testing.T) {
	dir := t.TempDir()
	manifest := `{"name":"dsh-profile-web","dsh":{"profile":{"bundles":["@deepseek-ai/dsh-base","@deepseek-ai/dsh-web-app"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	want := []string{"@deepseek-ai/dsh-base", "@deepseek-ai/dsh-web-app"}
	if got := DeepSeekProfileBundles(dir); !reflect.DeepEqual(got, want) {
		t.Errorf("DeepSeekProfileBundles = %v, want %v", got, want)
	}
	if got := DeepSeekProfileBundles(t.TempDir()); got != nil {
		t.Errorf("DeepSeekProfileBundles with no manifest = %v, want nil", got)
	}
}

// --- capability gates --------------------------------------------------------

func TestDeepSeekCapabilityGates(t *testing.T) {
	withConfig(t, &UserConfig{})
	inst := &Instance{Tool: "deepseek", ID: "abc", Title: "ds"}

	if !inst.CanRestart() {
		t.Error("CanRestart() = false; a live dsh session must be restartable")
	}
	if inst.CanFork() {
		t.Error("CanFork() = true; dsh 0.1.0-rc.6 has no fork command")
	}
	if inst.CanRestartFresh() {
		t.Error("CanRestartFresh() = true with resume off; every restart is already fresh")
	}

	withConfig(t, &UserConfig{DeepSeek: DeepSeekSettings{ResumeFlag: "--resume"}})
	if !inst.CanRestartFresh() {
		t.Error("CanRestartFresh() = false with a resume binding to discard")
	}
	if IsClaudeCompatible("deepseek") || IsCodexCompatible("deepseek") {
		t.Error("deepseek must not claim Claude/Codex capability gates")
	}
}

func TestDeepSeekEnvFileChain(t *testing.T) {
	cfg := &UserConfig{
		DeepSeek: DeepSeekSettings{EnvFile: "/global.env"},
		Conductors: map[string]ConductorOverrides{
			"boss": {DeepSeek: ConductorDeepSeekSettings{EnvFile: "/conductor.env"}},
		},
		Groups: map[string]GroupSettings{
			"team": {DeepSeek: GroupDeepSeekSettings{EnvFile: "/group.env"}},
		},
	}
	withConfig(t, cfg)

	if got := cfg.GetConductorDeepSeekEnvFile("boss"); got != "/conductor.env" {
		t.Errorf("conductor env_file = %q", got)
	}
	if got := cfg.GetGroupDeepSeekEnvFile("team"); got != "/group.env" {
		t.Errorf("group env_file = %q", got)
	}
	// Ancestor walk: a nested group inherits the parent's override.
	if got := cfg.GetGroupDeepSeekEnvFile("team/sub"); got != "/group.env" {
		t.Errorf("nested group env_file = %q, want the inherited /group.env", got)
	}
	if got := cfg.GetGroupDeepSeekProfile("team"); got != "" {
		t.Errorf("group profile = %q with none configured", got)
	}
}

func TestDeepSeekProfileChain(t *testing.T) {
	cfg := &UserConfig{
		DeepSeek: DeepSeekSettings{Profile: "web"},
		Conductors: map[string]ConductorOverrides{
			"boss": {DeepSeek: ConductorDeepSeekSettings{Profile: "conductor-profile"}},
		},
		Groups: map[string]GroupSettings{
			"team": {DeepSeek: GroupDeepSeekSettings{Profile: "group-profile"}},
		},
	}
	withConfig(t, cfg)

	inst := &Instance{Tool: "deepseek", GroupPath: "team", Title: "conductor-boss"}
	if got := inst.resolveDeepSeekProfile(); got != "conductor-profile" {
		t.Errorf("conductor profile: got %q", got)
	}

	inst = &Instance{Tool: "deepseek", GroupPath: "team", Title: "plain"}
	if got := inst.resolveDeepSeekProfile(); got != "group-profile" {
		t.Errorf("group profile: got %q", got)
	}

	inst = &Instance{Tool: "deepseek", Title: "plain"}
	if got := inst.resolveDeepSeekProfile(); got != "web" {
		t.Errorf("global profile: got %q", got)
	}

	// A per-session option beats every config level.
	raw, err := MarshalToolOptions(&DeepSeekOptions{Profile: "session-profile"})
	if err != nil {
		t.Fatal(err)
	}
	inst = &Instance{Tool: "deepseek", GroupPath: "team", Title: "conductor-boss", ToolOptionsJSON: raw}
	if got := inst.resolveDeepSeekProfile(); got != "session-profile" {
		t.Errorf("per-session profile: got %q", got)
	}
}
