package session

import (
	"bytes"
	"context"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestMuseResumePreservesLiteralArgument(t *testing.T) {
	old := userConfigCache
	userConfigCache = &UserConfig{Muse: MuseSettings{YoloMode: true}}
	t.Cleanup(func() { userConfigCache = old })
	for _, id := range []string{"sess-uuid-1", "two words", "owner's-session", "literal$MUSE_LITERAL", "line\nbreak", "unicode-日本"} {
		for _, custom := range []bool{false, true} {
			t.Run(id+"/custom="+map[bool]string{false: "false", true: "true"}[custom], func(t *testing.T) {
				inst := &Instance{Tool: "muse", Command: "muse"}
				want := []string{"--trust-workspace", "resume", id, "--yolo"}
				if custom {
					inst.Command = "muse --provider fixture"
					want = []string{"--provider", "fixture", "resume", id}
				}
				assertMuseCommandArguments(t, inst.buildMuseResumeCommand(id), want)
			})
		}
	}
}

func TestMuseTrustPolicyArguments(t *testing.T) {
	yoloFalse := false
	cases := []struct {
		name        string
		settings    MuseSettings
		sessionYolo *bool
		wantFresh   []string
	}{
		{
			name:      "default trust without yolo",
			wantFresh: []string{"--trust-workspace"},
		},
		{
			name:      "bare configured command opts out of trust",
			settings:  MuseSettings{Command: "muse"},
			wantFresh: []string{},
		},
		{
			name:        "explicit session false overrides global yolo",
			settings:    MuseSettings{YoloMode: true},
			sessionYolo: &yoloFalse,
			wantFresh:   []string{"--trust-workspace"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := userConfigCache
			userConfigCache = &UserConfig{Muse: tc.settings}
			t.Cleanup(func() { userConfigCache = old })
			inst := &Instance{Tool: "muse", Command: "muse"}
			if tc.sessionYolo != nil {
				opts, err := MarshalToolOptions(&MuseOptions{YoloMode: tc.sessionYolo})
				if err != nil {
					t.Fatal(err)
				}
				inst.ToolOptionsJSON = opts
			}
			t.Run("fresh", func(t *testing.T) {
				assertMuseCommandArguments(t, inst.buildMuseCommand(inst.Command), tc.wantFresh)
			})
			t.Run("resume", func(t *testing.T) {
				want := append(append([]string{}, tc.wantFresh...), "resume", "session-id")
				assertMuseCommandArguments(t, inst.buildMuseResumeCommand("session-id"), want)
			})
		})
	}
}

func assertMuseCommandArguments(t *testing.T, command string, want []string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	// Only bash executes. The muse function prints each argument, including an
	// empty argument, but emits nothing for a bare command with no arguments.
	script := "muse() { for arg in \"$@\"; do printf '%s\\0' \"$arg\"; done; }; " + command
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bash, "--noprofile", "--norc", "-c", script)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "MUSE_LITERAL=expanded"}
	cmd.Dir = t.TempDir()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("argument fixture failed: %v stderr=%q", err, stderr.String())
	}
	got := []string{}
	if len(out) > 0 {
		got = strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("argv=%q, want %q", got, want)
	}
}
