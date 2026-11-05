package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestKimiOptionsPermissionModeArgs(t *testing.T) {
	tests := []struct {
		mode string
		want []string
	}{
		{mode: "", want: nil},
		{mode: "yolo", want: []string{"--yolo"}},
		{mode: "auto", want: []string{"--auto"}},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			opts := &KimiOptions{PermissionMode: tt.mode}
			if got := opts.ToArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ToArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKimiOptionsRejectInvalidPermissionMode(t *testing.T) {
	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "manual"}); err == nil {
		t.Fatal("SetKimiOptions accepted an invalid permission mode")
	}
}

func TestKimiSessionDefaultOverridesConfiguredAuto(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{PermissionMode: KimiPermissionAuto}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: KimiPermissionDefault}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if strings.Contains(command, " --auto") || strings.Contains(command, " --yolo") {
		t.Fatalf("Kimi default emitted a permission flag: %q", command)
	}
}

func TestKimiSessionPermissionModeReachesResumeCommand(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := &Instance{Tool: "kimi", KimiSessionID: "native-id"}
	if err := inst.SetKimiOptions(&KimiOptions{PermissionMode: "auto"}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}

	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --session native-id --auto") {
		t.Fatalf("per-session permission mode missing from resume command: %q", command)
	}
}
