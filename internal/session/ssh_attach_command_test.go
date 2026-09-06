package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSSHAttachCommandExecution(t *testing.T) {
	for _, tc := range []struct{ name, terminal, want string }{
		{"portable", "screen", "screen"},
		{"unknown", "xterm-ghostty", "xterm-256color"},
		{"hostile", "x; touch /unwanted", "xterm-256color"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERM", tc.terminal)
			executable := filepath.Join(t.TempDir(), "capture executable")
			if err := os.WriteFile(executable, []byte("#!/bin/sh\nprintf '%s\\n' \"$TERM\" \"$@\"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runner := &SSHRunner{Host: "fixture", AgentDeckPath: executable, Profile: "profile ' quoted"}
			args := runner.buildAttachArgs("session ' quoted")
			output, err := exec.Command("/bin/sh", "-c", args[len(args)-1]).CombinedOutput()
			if err != nil {
				t.Fatalf("command failed: %v: %s", err, output)
			}
			want := []string{tc.want, "-p", "profile ' quoted", "session", "attach", "session ' quoted"}
			got := strings.Split(strings.TrimSuffix(string(output), "\n"), "\n")
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("captured = %q, want %q", got, want)
			}
		})
	}
}
