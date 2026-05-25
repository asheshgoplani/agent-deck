package session

import (
	"context"
	"strings"
	"testing"
)

// Issue #1171 (reported by @javierciccarelli): `agent-deck remote update` SCP'd
// the binary to the bare relative path "agent-deck" (→ ~/agent-deck), while the
// version check ran `agent-deck version` through the remote $PATH (→
// ~/.local/bin/agent-deck from install.sh). Deploy and check targeted DIFFERENT
// files, so the command printed "✓ Installed" while the remote kept running the
// old version. These tests pin the two-part fix:
//   1. resolve the remote's REAL binary location (command -v, else install.sh default)
//   2. verify the $PATH binary reports the new version before claiming success
//
// The SSH layer is stubbed via remoteExecFn so no real remote is required.

// recordingRunner returns an SSHRunner whose remote commands are answered by
// respond(remoteCmd) and recorded for assertions.
func recordingRunner(respond func(remoteCmd string) (string, error)) (*SSHRunner, *[]string) {
	var calls []string
	r := &SSHRunner{
		Host:          "tester@remote",
		AgentDeckPath: "agent-deck",
		remoteExecFn: func(_ context.Context, remoteCmd string, _ []byte) ([]byte, error) {
			calls = append(calls, remoteCmd)
			out, err := respond(remoteCmd)
			return []byte(out), err
		},
	}
	return r, &calls
}

func TestResolveRemotePath_PrefersCommandV(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		if strings.Contains(cmd, "command -v agent-deck") {
			return "/usr/local/bin/agent-deck\n", nil
		}
		return "", nil
	})

	got := r.ResolveRemotePath(context.Background())
	if got != "/usr/local/bin/agent-deck" {
		t.Fatalf("ResolveRemotePath = %q, want the command -v result /usr/local/bin/agent-deck", got)
	}
}

func TestResolveRemotePath_FallsBackToInstallDefault(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "command -v agent-deck"):
			return "", context.Canceled // command -v finds nothing → error
		case strings.Contains(cmd, "$HOME"):
			return "/home/tester\n", nil
		}
		return "", nil
	})

	got := r.ResolveRemotePath(context.Background())
	want := "/home/tester/.local/bin/agent-deck"
	if got != want {
		t.Fatalf("ResolveRemotePath fallback = %q, want install.sh default %q", got, want)
	}
}

func TestResolveRemotePath_HonorsExplicitConfig(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		if strings.Contains(cmd, "$HOME") {
			return "/home/tester\n", nil
		}
		t.Fatalf("explicit config must not probe the remote $PATH; got cmd %q", cmd)
		return "", nil
	})
	r.configuredPath = "~/custom/agent-deck"

	got := r.ResolveRemotePath(context.Background())
	if got != "/home/tester/custom/agent-deck" {
		t.Fatalf("ResolveRemotePath with explicit ~ config = %q, want /home/tester/custom/agent-deck", got)
	}
}

// TestInstallBinary_HappyPath: deploy lands at the resolved path and the $PATH
// binary then reports the expected version → success (nil error).
func TestInstallBinary_HappyPath(t *testing.T) {
	r, calls := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "command -v agent-deck"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		case strings.Contains(cmd, "cat >"): // deploy
			return "", nil
		case strings.Contains(cmd, "'agent-deck' version"): // $PATH check
			return "Agent Deck v1.9.32\n", nil
		}
		return "", nil
	})

	if err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.9.32"); err != nil {
		t.Fatalf("InstallBinary happy path returned error: %v", err)
	}

	var sawDeployToResolved bool
	for _, c := range *calls {
		if strings.Contains(c, "cat >") && strings.Contains(c, "/home/tester/.local/bin/agent-deck") {
			sawDeployToResolved = true
		}
	}
	if !sawDeployToResolved {
		t.Fatalf("expected deploy to the resolved $PATH location; calls=%v", *calls)
	}
}

// TestInstallBinary_VersionMismatch is the core false-success regression: the
// deployed binary reports the new version, but the $PATH binary still reports
// the old one. InstallBinary MUST return an actionable error, NOT success.
func TestInstallBinary_VersionMismatch(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "command -v agent-deck"):
			return "/home/tester/.local/bin/agent-deck\n", nil
		case strings.Contains(cmd, "cat >"):
			return "", nil
		case strings.Contains(cmd, "'agent-deck' version"): // $PATH still old
			return "Agent Deck v1.9.31\n", nil
		case strings.Contains(cmd, "version"): // deployed path reports new
			return "Agent Deck v1.9.32\n", nil
		}
		return "", nil
	})

	err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.9.32")
	if err == nil {
		t.Fatal("InstallBinary must return an error when the $PATH version does not advance, got nil (false success)")
	}
	if !strings.Contains(err.Error(), "$PATH") {
		t.Fatalf("error should be actionable about the $PATH mismatch; got: %v", err)
	}
}

// TestInstallBinary_NotOnPath: fresh remote where nothing is on $PATH. The
// deployed binary reports the right version but the bare `agent-deck` is not
// found → surface an actionable PATH warning rather than a false success.
func TestInstallBinary_NotOnPath(t *testing.T) {
	r, _ := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "command -v agent-deck"):
			return "", context.Canceled // not on PATH
		case strings.Contains(cmd, "$HOME"):
			return "/home/tester\n", nil
		case strings.Contains(cmd, "cat >"):
			return "", nil
		case strings.Contains(cmd, "'agent-deck' version"): // PATH lookup fails
			return "", context.Canceled
		case strings.Contains(cmd, "version"): // deployed absolute path works
			return "Agent Deck v1.9.32\n", nil
		}
		return "", nil
	})

	err := r.InstallBinary(context.Background(), []byte("BINARY"), "1.9.32")
	if err == nil {
		t.Fatal("InstallBinary must not report success when the binary is not on the remote $PATH")
	}
	if !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("error should mention PATH so the user knows to fix it; got: %v", err)
	}
}
