package session

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFakeTrustRunner installs fake ssh and docker binaries that ignore the
// script on stdin and print the given stdout/stderr text instead.
func writeFakeTrustRunner(t *testing.T, stdout, stderr string) {
	t.Helper()
	fakeBin := t.TempDir()
	body := "#!/bin/sh\ncat >/dev/null\nprintf '%s' \"$FAKE_TRUST_STDOUT\"\nprintf '%s' \"$FAKE_TRUST_STDERR\" >&2\n"
	for _, name := range []string{"ssh", "docker"} {
		if err := os.WriteFile(filepath.Join(fakeBin, name), []byte(body), 0o755); err != nil {
			t.Fatalf("write fake %s: %v", name, err)
		}
	}
	t.Setenv("PATH", fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_TRUST_STDOUT", stdout)
	t.Setenv("FAKE_TRUST_STDERR", stderr)
}

func TestCursorTrustRunners_GrantMarkerWholeStdoutLineOnly(t *testing.T) {
	tests := []struct {
		name    string
		stdout  string
		stderr  string
		granted bool
	}{
		{"exact line on stdout", "agent-deck-cursor-trust-granted\n", "", true},
		{"exact line among other stdout lines", "motd\n  agent-deck-cursor-trust-granted\r\n", "", true},
		{"embedded in another stdout line", "note: agent-deck-cursor-trust-granted later\n", "", false},
		{"marker with suffix", "agent-deck-cursor-trust-granted-not\n", "", false},
		{"exact line on stderr only", "", "agent-deck-cursor-trust-granted\n", false},
		{"no marker", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeFakeTrustRunner(t, tt.stdout, tt.stderr)
			got, err := runCursorTrustRemoteScript("user@remote", "true\n")
			if err != nil {
				t.Fatalf("runCursorTrustRemoteScript: %v", err)
			}
			if got != tt.granted {
				t.Errorf("ssh granted = %v, want %v", got, tt.granted)
			}
			got, err = runCursorTrustContainerScript("agent-deck-test", "true\n")
			if err != nil {
				t.Fatalf("runCursorTrustContainerScript: %v", err)
			}
			if got != tt.granted {
				t.Errorf("docker granted = %v, want %v", got, tt.granted)
			}
		})
	}
}
