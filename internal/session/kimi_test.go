package session

import (
	"strings"
	"testing"

	"al.essio.dev/pkg/shellescape"
	"github.com/BurntSushi/toml"
)

func TestKimiRegistryIdentity(t *testing.T) {
	r := Init(nil)
	def := r.Get("kimi")
	if def == nil || def.Command != "kimi" || def.Icon != "🌙" {
		t.Fatalf("Get(kimi) = %+v, want canonical command and icon", def)
	}
	if got := r.Color("kimi"); got != "purple" {
		t.Fatalf("Color(kimi) = %q, want purple", got)
	}
	for _, command := range []string{"kimi", "/usr/local/bin/kimi --session abc", "KIMI"} {
		if got := r.Match(command); got != "kimi" {
			t.Errorf("Match(%q) = %q, want kimi", command, got)
		}
	}
	for _, command := range []string{"kimi-cli", "/usr/local/bin/kimi-cli", "echo kimi"} {
		if got := r.Match(command); got == "kimi" {
			t.Errorf("Match(%q) incorrectly selected official Kimi", command)
		}
	}
	if got := GetToolIcon("kimi"); got != "🌙" {
		t.Errorf("GetToolIcon(kimi) = %q, want 🌙", got)
	}
	pickerNames := PickerToolNames()
	if !containsString(pickerNames, "kimi") {
		t.Fatalf("PickerToolNames() = %v, want kimi", pickerNames)
	}
	if pickerNames[len(pickerNames)-1] != "kimi" {
		t.Fatalf("PickerToolNames() = %v, want kimi last", pickerNames)
	}
}

func TestKimiSettingsTOMLAndDefaults(t *testing.T) {
	var cfg UserConfig
	doc := `[kimi]
command = "/opt/kimi --custom"
config_dir = "/tmp/kimi home"
env_file = "/tmp/kimi.env"
default_model = "k2.5"
permission_mode = "auto"
hooks_enabled = false
`
	if _, err := toml.Decode(doc, &cfg); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if cfg.Kimi.Command != "/opt/kimi --custom" || cfg.Kimi.ConfigDir != "/tmp/kimi home" ||
		cfg.Kimi.EnvFile != "/tmp/kimi.env" || cfg.Kimi.DefaultModel != "k2.5" || cfg.Kimi.PermissionMode != "auto" {
		t.Fatalf("Kimi settings did not decode: %+v", cfg.Kimi)
	}
	if cfg.Kimi.GetHooksEnabled() {
		t.Fatal("GetHooksEnabled() = true with hooks_enabled = false")
	}
	var defaults KimiSettings
	if !defaults.GetHooksEnabled() {
		t.Fatal("default GetHooksEnabled() = false, want true")
	}
}

func TestBuildKimiCommandFresh(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{
		DefaultModel:   "model with spaces",
		PermissionMode: "auto",
	}})
	defer restore()

	inst := &Instance{ID: "kimi-123", Title: "dev `id`", Tool: "kimi", HermesHookGeneration: "generation-1"}
	got := inst.buildKimiCommand("kimi")
	wantSuffix := "kimi --model " + shellescape.Quote("model with spaces") + " --auto"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("buildKimiCommand() = %q, want suffix %q", got, wantSuffix)
	}
	for _, want := range []string{
		"AGENTDECK_INSTANCE_ID=kimi-123",
		"AGENTDECK_TITLE=" + shellescape.Quote("dev `id`"),
		"AGENTDECK_TOOL=kimi",
		"AGENTDECK_PROFILE=",
		"AGENTDECK_HOOK_GENERATION=generation-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("buildKimiCommand() = %q, missing %q", got, want)
		}
	}
	if strings.Contains(got, "--prompt") || strings.Contains(got, "KIMI_CODE_HOME") {
		t.Errorf("fresh command contains forbidden print mode or home override: %q", got)
	}
}

func TestKimiLaunchModelOverride(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{DefaultModel: "configured-model"}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if !SupportsLaunchModel("kimi") {
		t.Fatal("Kimi should support per-session model selection")
	}
	if err := inst.ApplyLaunchModel("session-model"); err != nil {
		t.Fatalf("ApplyLaunchModel() error = %v", err)
	}
	if got := inst.LaunchModelID(); got != "session-model" {
		t.Fatalf("LaunchModelID() = %q, want session-model", got)
	}
	if command := inst.buildKimiCommand("kimi"); !strings.HasSuffix(command, "kimi --model session-model") {
		t.Fatalf("buildKimiCommand() = %q, want per-session model", command)
	}

	if err := inst.ClearLaunchModel(); err != nil {
		t.Fatalf("ClearLaunchModel() error = %v", err)
	}
	if command := inst.buildKimiCommand("kimi"); !strings.HasSuffix(command, "kimi --model configured-model") {
		t.Fatalf("buildKimiCommand() after clear = %q, want configured default", command)
	}
}

func TestBuildKimiCommandResume(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{DefaultModel: "fresh-only", PermissionMode: "yolo"}})
	defer restore()

	sessionID := "id with spaces;$(touch /tmp/kimi-pwned)"
	inst := &Instance{Tool: "kimi", KimiSessionID: sessionID}
	got := inst.buildKimiCommand("kimi")
	wantSuffix := "kimi --session " + shellescape.Quote(sessionID) + " --yolo"
	if !strings.HasSuffix(got, wantSuffix) {
		t.Fatalf("resume command = %q, want suffix %q", got, wantSuffix)
	}
	if strings.Contains(got, "--model") || strings.Contains(got, "fresh-only") {
		t.Fatalf("resume command reapplied default model: %q", got)
	}
}

func TestBuildKimiCommandPassthrough(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{DefaultModel: "unused", PermissionMode: "auto"}})
	defer restore()

	inst := &Instance{ID: "abc", Title: "dev", Tool: "kimi", KimiSessionID: "native-id"}
	got := inst.buildKimiCommand("kimi --operator-flag")
	if !strings.HasSuffix(got, "kimi --operator-flag") {
		t.Fatalf("passthrough command = %q", got)
	}
	for _, forbidden := range []string{"--session", "--model", "--yolo", "--auto"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("passthrough command %q contains derived flag %q", got, forbidden)
		}
	}
	for _, want := range []string{"AGENTDECK_INSTANCE_ID=abc", "AGENTDECK_TITLE=dev", "AGENTDECK_TOOL=kimi", "AGENTDECK_PROFILE="} {
		if !strings.Contains(got, want) {
			t.Errorf("passthrough command %q missing %q", got, want)
		}
	}
}

func TestBuildKimiCommandConfigDirAndEnvFile(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{
		ConfigDir: "/tmp/kimi home",
		EnvFile:   "/tmp/kimi.env",
	}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if got := inst.getToolEnvFile(); got != "/tmp/kimi.env" {
		t.Fatalf("getToolEnvFile() = %q, want /tmp/kimi.env", got)
	}
	got := inst.buildKimiCommand("kimi")
	if !strings.Contains(got, "KIMI_CODE_HOME="+shellescape.Quote("/tmp/kimi home")) {
		t.Fatalf("command %q does not export explicit KIMI_CODE_HOME", got)
	}
}

func TestBuildKimiCommandConfiguredCommand(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{Command: "/opt/kimi --custom", DefaultModel: "k2.5", PermissionMode: "auto"}})
	defer restore()

	inst := &Instance{Tool: "kimi", KimiSessionID: "native-id"}
	got := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(got, "/opt/kimi --custom") {
		t.Fatalf("configured command = %q", got)
	}
	for _, forbidden := range []string{"--session", "--model", "--yolo", "--auto"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("configured command %q contains derived flag %q", got, forbidden)
		}
	}
}

func TestGetToolCommandKimi(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{Command: "/opt/kimi --custom"}})
	defer restore()
	if got := GetToolCommand("kimi"); got != "/opt/kimi --custom" {
		t.Fatalf("GetToolCommand(kimi) = %q", got)
	}
}
