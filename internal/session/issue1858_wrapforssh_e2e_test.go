package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// End-to-end proof for issues #1858 and review findings F5/F6, plus the round-3
// "~" objection, driven through a fake `ssh` on PATH that reproduces ssh's own
// semantics: the remote command starts in the REMOTE user's home, and the final
// argument is handed to a shell to re-parse.
//
// Every symbol used here exists at the merge base, so these tests run — and
// fail — against the unfixed tree.

// fakeSSHDir installs an `ssh` shim that behaves like the real thing for the two
// properties under test: the remote command starts in the REMOTE user's home,
// and the final argument is handed to a shell to parse. It records the exact
// argv it was invoked with so a test can assert on the command line the remote
// host receives.
func fakeSSHDir(t *testing.T, remoteHome string) (binDir, argvLog string) {
	t.Helper()
	binDir = t.TempDir()
	argvLog = filepath.Join(binDir, "argv.log")

	script := fmt.Sprintf(`#!/usr/bin/env bash
# Fake ssh. Records argv, then reproduces the two behaviours under test:
#  * the remote command runs as the REMOTE user, in the REMOTE home
#  * ssh passes the command to the remote login shell, which re-parses it
{ printf '%%s\n' "$*"; } >> %q
cmd=""
for a in "$@"; do cmd=$a; done
export HOME=%q
cd "$HOME" || exit 97
exec bash -c "$cmd"
`, argvLog, remoteHome)

	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	return binDir, argvLog
}

// runRemote composes inst's spawn payload the way a real spawn does, sends it
// through wrapForSSH, and executes the result with the fake ssh first on PATH.
// It returns the remote side's stdout+stderr and the recorded command line.
func runRemote(t *testing.T, inst *Instance, remoteHome, probe string) (output, argv string) {
	t.Helper()

	binDir, argvLog := fakeSSHDir(t, remoteHome)
	payload := inst.buildBashExportPrefix(false) + probe
	composed := inst.wrapForSSH(payload)

	cmd := exec.Command("bash", "-c", composed)
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, _ := cmd.CombinedOutput()

	recorded, _ := os.ReadFile(argvLog)
	argv = strings.TrimSpace(string(recorded))
	// Logged unconditionally, not just on failure: "the exact command line the
	// remote host receives" is the artifact a reviewer wants to read for #1858,
	// and `go test -v` is the cheapest way to hand it to them.
	t.Logf("remote host received:\n  argv:   %s\n  output: %s", argv, strings.TrimSpace(string(out)))
	return string(out), argv
}

// remoteClaudeInstance builds an --ssh claude instance with an explicit config
// dir under the LOCAL home — the shape #1858 reports — and NO group, which is
// the reporter's exact repro and the case that used to break the remote parse.
func remoteClaudeInstance(t *testing.T, remotePath string) *Instance {
	t.Helper()
	localHome := t.TempDir()
	t.Setenv("HOME", localHome)
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(localHome, ".claude"))
	ClearUserConfigCache()
	t.Cleanup(ClearUserConfigCache)

	inst := NewInstanceWithTool("remote-host", t.TempDir(), "claude")
	inst.SSHHost = "cloudlydr@remote-host"
	inst.SSHRemotePath = remotePath
	inst.GroupPath = "" // ungrouped: emits AGENTDECK_RESOLVED_GROUP='' (review F5)
	return inst
}

// --- End to end through a fake ssh -------------------------------------------

// TestWrapForSSH_UngroupedRemoteSessionParsesOnTheRemoteShell is review finding
// F5. wrapForSSH escaped single quotes TWICE when SSHRemotePath was set: once
// into the payload and again over the whole thing in shellQuote. The spawn
// prefix always emits `export AGENTDECK_RESOLVED_GROUP=”;` for a claude
// session, so EVERY ungrouped --ssh session with a --remote-path produced a
// remote command the remote shell could not parse — `unexpected EOF while
// looking for matching '`, i.e. the ~250ms exit #1858 reports.
//
// The previous attempt's test evaluated the config-dir expression alone with
// printf and never composed it through wrapForSSH, so it passed green over a
// payload that could not run. This one runs the composed command line.
func TestWrapForSSH_UngroupedRemoteSessionParsesOnTheRemoteShell(t *testing.T) {
	remoteHome := t.TempDir()
	remoteDir := filepath.Join(remoteHome, "app")
	if err := os.MkdirAll(remoteDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	inst := remoteClaudeInstance(t, remoteDir)
	out, argv := runRemote(t, inst, remoteHome, `printf 'REMOTE_OK\n'`)

	if strings.Contains(out, "unexpected EOF") || strings.Contains(out, "syntax error") {
		t.Fatalf("the remote shell could not parse the spawn payload — this is the ~250ms crash:\n%s\n\ncommand line was:\n%s", out, argv)
	}
	if !strings.Contains(out, "REMOTE_OK") {
		t.Fatalf("remote command did not run:\n%s\n\ncommand line was:\n%s", out, argv)
	}
	if !strings.Contains(argv, "AGENTDECK_RESOLVED_GROUP") {
		t.Fatalf("this test is not exercising the ungrouped payload it claims to; argv:\n%s", argv)
	}
}

// TestWrapForSSH_ConfigDirResolvesAgainstTheRemoteHome is #1858 itself, plus
// review finding F6 for the second variable. Both config-dir vars must name a
// directory that exists on the REMOTE host; a literal local-home path does not,
// and two vars naming the config dir while disagreeing is worse than one.
func TestWrapForSSH_ConfigDirResolvesAgainstTheRemoteHome(t *testing.T) {
	remoteHome := t.TempDir()
	inst := remoteClaudeInstance(t, "")
	localHome := os.Getenv("HOME")

	out, argv := runRemote(t, inst, remoteHome,
		`printf 'CFG=[%s]\nRESOLVED=[%s]\n' "$CLAUDE_CONFIG_DIR" "$AGENTDECK_RESOLVED_CONFIG_DIR"`)

	wantCfg := "CFG=[" + filepath.Join(remoteHome, ".claude") + "]"
	if !strings.Contains(out, wantCfg) {
		t.Errorf("CLAUDE_CONFIG_DIR did not resolve against the REMOTE user's home.\nwant %s\ngot:\n%s\n\ncommand line:\n%s", wantCfg, out, argv)
	}
	wantResolved := "RESOLVED=[" + filepath.Join(remoteHome, ".claude") + "]"
	if !strings.Contains(out, wantResolved) {
		t.Errorf("AGENTDECK_RESOLVED_CONFIG_DIR (what hooks and statusline scripts read) did not resolve against the REMOTE home.\nwant %s\ngot:\n%s", wantResolved, out)
	}
	if strings.Contains(argv, localHome) {
		t.Errorf("the literal LOCAL home path %q was shipped to the remote host:\n%s", localHome, argv)
	}
}

// TestWrapForSSH_RemoteWorkingDirectoryMatchesIdentity is the ~ rule proven end
// to end, through the real composed command line rather than through
// RemoteCDPrefix alone.
func TestWrapForSSH_RemoteWorkingDirectoryMatchesIdentity(t *testing.T) {
	remoteHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(remoteHome, "work"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, c := range []struct{ remotePath, wantDir string }{
		{"", remoteHome},
		{"~", remoteHome},
		{"~/work", filepath.Join(remoteHome, "work")},
	} {
		inst := remoteClaudeInstance(t, c.remotePath)
		out, argv := runRemote(t, inst, remoteHome, `printf 'PWD=[%s]\n' "$PWD"`)

		want := "PWD=[" + c.wantDir + "]"
		if !strings.Contains(out, want) {
			t.Errorf("--remote-path %q ran in the wrong directory.\nwant %s\ngot:\n%s\n\ncommand line:\n%s", c.remotePath, want, out, argv)
		}
	}
}

// TestWrapForSSH_QuotesTheRemotePathExactlyOnce guards the shape of the fix
// directly: the payload must appear in the composed command with its quotes
// escaped once (by shellQuote) and not twice.
func TestWrapForSSH_QuotesTheRemotePathExactlyOnce(t *testing.T) {
	inst := NewInstanceWithTool("q", t.TempDir(), "claude")
	inst.SSHHost = "alice@host-a"
	inst.SSHRemotePath = "/srv/app"

	composed := inst.wrapForSSH(`export X=''; run`)

	// Double-escaping shows up as the four-character sequence '\'' having been
	// applied twice, i.e. a backslash before a backslash-quote.
	if regexp.MustCompile(`\\'\\\\''`).MatchString(composed) {
		t.Fatalf("payload appears to be escaped twice:\n%s", composed)
	}
	// One round of shellQuote turns each ' into '\''.
	if !strings.Contains(composed, `export X='\'''\''; run`) {
		t.Fatalf("payload is not escaped exactly once by shellQuote:\n%s", composed)
	}
}
