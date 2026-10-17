package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// appHelpProbes are the help requests a client (the macOS app among them)
// sends to detect what a core supports, beyond the --help form of every
// documented command that helpSpecs already lists.
var appHelpProbes = [][]string{
	{"session", "image-upload", "--help"},
	{"session", "image-upload", "-h"},
	{"session", "image-upload", "-help"},
	{"-p", "default", "session", "image-upload", "--help"},
	{"open", "-h"},
	{"file", "help"},
	{"file", "bundle", "-h"},
	{"usage", "help"},
	{"usage", "statusline", "--help"},
	{"usage", "statusline", "-h"},
	{"usage", "statusline-wrap", "--help"},
	{"usage", "ingest", "--help"},
	{"session", "help"},
	{"session", "send", "--help"},
	{"session", "send-status", "--help"},
	{"session", "queue", "--help"},
	{"session", "queue", "help"},
	{"session", "queue", "list", "--help"},
	{"session", "queue", "release", "--help"},
	{"session", "queue", "cancel", "--help"},
	{"costs", "help"},
	{"costs", "-h"},
	{"costs", "daily", "--help"},
	{"costs", "sessions", "--help"},
	{"costs", "models", "--help"},
	{"costs", "groups", "--help"},
	{"costs", "budgets", "--help"},
	{"limits", "--help"},
	{"limits", "-h"},
	{"remote", "help"},
	{"recall", "timeline", "--help"},
	{"recall", "follow", "--help"},
	{"events", "--help"},
	{"events", "help"},
	{"events", "follow", "--help"},
	{"events", "publish", "--help"},
	{"events", "stats", "--help"},
	{"hooks", "install", "--help"},
	{"hooks", "uninstall", "--help"},
}

// silentHelp names internal hook entry points that print nothing for --help:
// their stdout is a hook protocol, so they only exit 0 quietly.
var silentHelp = map[string]bool{"hook-handler": true, "codex-notify": true}

// An explicit help request is an answer, not an error: the whole usage goes
// to stdout and the exit is 0. A caller that reads stdout on exit 0 (the app's
// capability probes, `cmd --help | less`) must see all of it.
func TestExplicitHelpPrintsToStdout(t *testing.T) {
	probes := append([][]string{}, appHelpProbes...)
	for _, spec := range helpSpecs() {
		probes = append(probes, spec.args)
	}
	home := t.TempDir()
	for _, args := range probes {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr, code := runAgentDeck(t, home, args...)
			wantUsage := !silentHelp[args[0]]
			if code != 0 || (wantUsage && strings.TrimSpace(stdout) == "") || stderr != "" {
				t.Fatalf("%v: exit=%d stdout=%dB stderr=%q, want exit 0, usage on stdout, empty stderr", args, code, len(stdout), stderr)
			}
		})
	}
}

// Usage printed because of a usage error stays on stderr with a non-zero exit.
func TestUsageErrorStaysOnStderr(t *testing.T) {
	home := t.TempDir()
	for _, args := range [][]string{
		{"session", "image-upload", "--bogus"},
		{"open", "--bogus"},
		{"file", "bundle", "--bogus"},
		{"usage", "statusline", "--bogus"},
		{"recall", "timeline", "--bogus"},
		{"events", "publish", "--bogus"},
		{"list", "--bogus"},
	} {
		args := args
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			stdout, stderr, code := runAgentDeck(t, home, args...)
			if code == 0 || stderr == "" || strings.Contains(stdout, "-bogus") {
				t.Fatalf("%v: exit=%d stdout=%q stderr=%q, want non-zero exit with the error on stderr", args, code, stdout, stderr)
			}
		})
	}
}

// The app probes a remote host through `remote <name> ...`: the remote's help
// must arrive on the controller's stdout with exit 0.
func TestRemoteForwardedHelpPrintsToStdout(t *testing.T) {
	controller, remote, shim := t.TempDir(), t.TempDir(), t.TempDir()
	configDir := filepath.Join(controller, ".config", "agent-deck")
	if err := os.MkdirAll(configDir, 0700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf("[remotes.lab]\nhost = 'test-host'\nagent_deck_path = '%s'\n", channelsCLIBinary(t))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	startParitySSH(t, remote, shim)
	env := []string{"PATH=" + shim + ":" + os.Getenv("PATH")}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"session", "image-upload", "--help"}, "Usage of session image-upload:"},
		{[]string{"session", "queue", "--help"}, "session queue"},
		{[]string{"session", "send-status", "--help"}, "send-status"},
		{[]string{"recall", "timeline", "--help"}, "Usage: agent-deck recall timeline"},
		{[]string{"limits", "--help"}, "Usage: agent-deck limits"},
	} {
		tc := tc
		t.Run(strings.Join(tc.args, "_"), func(t *testing.T) {
			stdout, stderr, code := runAgentDeckEnv(t, controller, "", env, append([]string{"remote", "lab"}, tc.args...)...)
			if code != 0 || !strings.Contains(stdout, tc.want) || stderr != "" {
				t.Fatalf("remote %v: exit=%d stdout=%q stderr=%q, want %q on stdout and empty stderr", tc.args, code, stdout, stderr, tc.want)
			}
		})
	}
}
