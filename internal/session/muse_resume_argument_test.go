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
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sess-uuid-1", "two words", "owner's-session", "literal$MUSE_LITERAL", "line\nbreak", "unicode-日本"} {
		for _, custom := range []bool{false, true} {
			t.Run(id+"/custom="+map[bool]string{false: "false", true: "true"}[custom], func(t *testing.T) {
				inst := &Instance{Tool: "muse", Command: "muse"}
				want := []string{"--trust-workspace", "resume", id, "--yolo"}
				if custom {
					inst.Command = "muse --provider fixture"
					want = []string{"--provider", "fixture", "resume", id}
				}
				// The only executable is bash. Its muse function prints arguments; no
				// actual provider, login initialization, tmux or network is involved.
				script := "muse() { printf '%s\\0' \"$@\"; }; " + inst.buildMuseResumeCommand(id)
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
				got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
				if !reflect.DeepEqual(got, want) {
					t.Errorf("argv=%q, want %q", got, want)
				}
			})
		}
	}
}
