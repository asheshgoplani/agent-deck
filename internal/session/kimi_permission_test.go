package session

import (
	"reflect"
	"strings"
	"testing"
)

func TestKimiOptionsYoloModeArgs(t *testing.T) {
	tests := []struct {
		name string
		yolo bool
		want []string
	}{
		{name: "off", want: nil},
		{name: "on", yolo: true, want: []string{"--yolo"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := &KimiOptions{YoloMode: tt.yolo}
			if got := opts.ToArgs(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ToArgs() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestKimiSessionYoloFalseOverridesConfigured(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{Kimi: KimiSettings{YoloMode: true}})
	defer restore()

	inst := &Instance{Tool: "kimi"}
	if err := inst.SetKimiOptions(&KimiOptions{YoloMode: false}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}
	command := inst.buildKimiCommand("kimi")
	if strings.Contains(command, " --yolo") {
		t.Fatalf("Kimi per-session off emitted --yolo: %q", command)
	}
}

func TestKimiSessionYoloModeReachesResumeCommand(t *testing.T) {
	restore := resetUserConfigCache(t, &UserConfig{})
	defer restore()

	inst := &Instance{Tool: "kimi", KimiSessionID: "native-id"}
	if err := inst.SetKimiOptions(&KimiOptions{YoloMode: true}); err != nil {
		t.Fatalf("SetKimiOptions: %v", err)
	}

	command := inst.buildKimiCommand("kimi")
	if !strings.HasSuffix(command, "kimi --session native-id --yolo") {
		t.Fatalf("per-session yolo mode missing from resume command: %q", command)
	}
}
