package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestKimiOptionsPermissionModeArgs(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want []string
	}{
		{name: "unset", mode: "", want: nil},
		{name: "always ask", mode: "always-ask", want: nil},
		{name: "ask when needed", mode: "ask-when-needed", want: []string{"--yolo"}},
		{name: "never ask", mode: "never-ask", want: []string{"--auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &KimiOptions{PermissionMode: tt.mode}
			if got := opts.ToArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ToArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidateKimiPermissionMode(t *testing.T) {
	for _, mode := range []string{"", "always-ask", "ask-when-needed", "never-ask"} {
		if err := ValidateKimiPermissionMode(mode); err != nil {
			t.Errorf("ValidateKimiPermissionMode(%q) = %v, want nil", mode, err)
		}
	}
	for _, mode := range []string{"yolo", "auto", "manual", "bypass"} {
		if err := ValidateKimiPermissionMode(mode); err == nil {
			t.Errorf("ValidateKimiPermissionMode(%q) = nil, want error", mode)
		}
	}
}

func TestBuildKimiCommandInheritsGlobalMode(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{PermissionMode: "ask-when-needed"}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --yolo") {
		t.Fatalf("buildKimiCommand() = %q, want inherited --yolo", command)
	}
}

func TestBuildKimiCommandExplicitAlwaysAskOverridesGlobal(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{PermissionMode: "never-ask"}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "always-ask"}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if strings.Contains(command, "--yolo") || strings.Contains(command, "--auto") {
		t.Fatalf("buildKimiCommand() = %q, want no permission flag", command)
	}
}

func TestBuildKimiCommandResumeNeverAsk(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := &Instance{Tool: "kimi", KimiSessionID: "native-id"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "never-ask"}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --session native-id --auto") {
		t.Fatalf("buildKimiCommand() = %q, want resume with --auto", command)
	}
	if strings.Contains(command, "--model") {
		t.Fatalf("resume command reapplied --model: %q", command)
	}
}

func TestKimiSessionAlwaysAskOverridesConfigured(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{PermissionMode: "never-ask"}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "always-ask"}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if strings.Contains(command, "--auto") {
		t.Fatalf("per-session always-ask emitted --auto: %q", command)
	}
}

func TestKimiSessionAskWhenNeededReachesResumeCommand(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := &Instance{Tool: "kimi", KimiSessionID: "native-id"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "ask-when-needed"}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --session native-id --yolo") {
		t.Fatalf("per-session ask-when-needed missing from resume command: %q", command)
	}
}

func TestBuildKimiCommandNormalizesSessionPermissionMode(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{PermissionMode: "never-ask"}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: " ask-when-needed "}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --yolo") {
		t.Fatalf("buildKimiCommand() = %q, want normalized per-session --yolo", command)
	}
	if strings.Count(command, "--yolo") != 1 || strings.Contains(command, "--auto") {
		t.Fatalf("buildKimiCommand() = %q, want exactly one normalized permission flag", command)
	}
}
