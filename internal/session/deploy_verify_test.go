package session

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #2340: agent-deck v1.16.15's unattended remote sweep landed a truncated
// binary (20,840,448 of 42,549,432 bytes) at the final install path on a
// shared Linux remote ("agentbox"): `agent-deck --version` on it segfaulted.
// InstallBinary/InstallBinaryWithForce called deployResolvedBinary with no
// expected version, so remoteDeployScript's staged-binary `--version` gate
// (which InstallLocalArchive already exercised) was silently skipped for
// every ordinary release deploy — the exact path the sweep uses. These tests
// pin the fix: the expected version now always reaches the script, so a
// staged binary that cannot execute, or reports the wrong version, is
// rejected before the atomic rename, and the previously installed binary is
// left untouched.

// fakeAgentDeckScript is a shell "binary" the deploy script can exec with a
// `version` argument, mirroring the real CLI's `Agent Deck vX.Y.Z` line.
func fakeAgentDeckScript(version string) []byte {
	return []byte("#!/bin/sh\necho 'Agent Deck v" + version + "'\n")
}

// TestDeployScript_ExpectedVersionVerifiedBeforeRename is the happy path:
// a staged binary that reports the expected version is renamed into place.
func TestDeployScript_ExpectedVersionVerifiedBeforeRename(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, noSudo)

	if err := r.deployResolvedBinary(context.Background(), fakeAgentDeckScript("1.16.15"), target, "1.16.15"); err != nil {
		t.Fatalf("deployResolvedBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read installed binary: %v", err)
	}
	if string(got) != string(fakeAgentDeckScript("1.16.15")) {
		t.Fatalf("installed binary does not match staged payload")
	}
}

// TestDeployScript_TruncatedTransferKeepsOldBinary simulates the SSH
// transfer that agentbox actually saw: the payload stream is cut mid-write
// (the local process was killed, exactly as #2340's launch-agent bootout
// killed the sweep's child mid-transfer). The staged file cannot execute, so
// the `--version` gate must refuse the rename and the previously installed
// binary must survive untouched.
func TestDeployScript_TruncatedTransferKeepsOldBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	old := fakeAgentDeckScript("1.16.14")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}

	// A real binary's tar-extracted bytes truncated mid-stream: valid
	// shebang line, but the body (and therefore the echo'd version) is cut
	// before completion, so it cannot even parse as intended -- here
	// simulated with a half-written script that fails to execute cleanly.
	truncated := fakeAgentDeckScript("1.16.15")
	truncated = truncated[:len(truncated)-6] // cut mid "v1.16.15\n"

	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- pipeRunner(t, noSudo, pr).deployResolvedBinary(context.Background(), []byte{}, target, "1.16.15")
	}()
	if _, err := io.WriteString(pw, string(truncated)); err != nil {
		t.Fatal(err)
	}
	// Close the pipe early, exactly as a killed sender would: the remote's
	// `cat > "$t"` sees EOF after only the partial bytes arrive.
	pw.Close()

	err := <-done
	if err == nil {
		t.Fatal("a truncated transfer must be rejected, not renamed into place")
	}
	if !strings.Contains(err.Error(), "staged binary") {
		t.Errorf("error should name the staged-binary check, got: %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatalf("old binary must survive a rejected deploy: %v", readErr)
	}
	if string(got) != string(old) {
		t.Fatalf("old binary was replaced by a rejected deploy: got %q", got)
	}
	entries, _ := filepath.Glob(target + ".new.*")
	if len(entries) != 0 {
		t.Errorf("staging file left behind after a rejected deploy: %v", entries)
	}
}

// TestDeployScript_VersionMismatchRejectsAndKeepsOldBinary: the staged
// binary executes fine but reports a version other than the one the
// controller expected to deploy (a stale cache, a swapped asset, a build
// mismatch) -- never renamed into place either.
func TestDeployScript_VersionMismatchRejectsAndKeepsOldBinary(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	old := fakeAgentDeckScript("1.16.14")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRunner(t, noSudo)

	err := r.deployResolvedBinary(context.Background(), fakeAgentDeckScript("1.16.13"), target, "1.16.15")
	if err == nil {
		t.Fatal("a version mismatch must be rejected, not renamed into place")
	}
	if !strings.Contains(err.Error(), "staged binary") {
		t.Errorf("error should name the staged-binary check, got: %v", err)
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != string(old) {
		t.Fatalf("old binary must survive a version-mismatch deploy: %q, %v", got, readErr)
	}
}

// TestDeployScript_UnexecutableStagedBinaryRejected: the staged bytes are
// not a script or binary at all (no shebang, no ELF/Mach-O header) -- the
// same shape a corrupted or truncated-mid-header transfer produces.
func TestDeployScript_UnexecutableStagedBinaryRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	old := fakeAgentDeckScript("1.16.14")
	if err := os.WriteFile(target, old, 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRunner(t, noSudo)

	err := r.deployResolvedBinary(context.Background(), []byte{0x00, 0x01, 0x02, 0x03}, target, "1.16.15")
	if err == nil {
		t.Fatal("garbage bytes must be rejected, not renamed into place")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil || string(got) != string(old) {
		t.Fatalf("old binary must survive an unexecutable deploy: %q, %v", got, readErr)
	}
}

// TestDeployScript_NoExpectedVersionSkipsGate documents the intentional
// escape hatch: DeployBinary (used by tests and any caller with no version
// to check) still streams bytes straight through with no `--version` gate.
// Production code must never take this path for a real release install --
// InstallBinary/InstallBinaryWithForce always pass one (see
// TestInstallBinaryWithForce_PassesExpectedVersionToScript below) -- but the
// escape hatch itself must keep behaving exactly as before.
func TestDeployScript_NoExpectedVersionSkipsGate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, noSudo)

	if err := r.DeployBinary(context.Background(), []byte("not-a-binary-at-all"), target); err != nil {
		t.Fatalf("DeployBinary with no expected version must not gate on --version: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "not-a-binary-at-all" {
		t.Fatalf("installed %q, want the raw payload", got)
	}
}

// TestInstallBinaryWithForce_PassesExpectedVersionToScript is a regression
// test for the actual #2340 bug: InstallBinaryWithForce must always hand
// its expected version through to the remote script, never "" (which
// disables the gate entirely, as TestDeployScript_NoExpectedVersionSkipsGate
// shows). It uses the stub remoteExecFn harness (issue1171 style) so the
// assertion is on the exact script arguments sent over the wire, not on a
// real sh execution.
func TestInstallBinaryWithForce_PassesExpectedVersionToScript(t *testing.T) {
	r, calls := recordingRunner(func(cmd string) (string, error) {
		switch {
		case strings.Contains(cmd, "command -v agent-deck"):
			return "NONE", nil // nothing on the remote's $PATH
		case strings.Contains(cmd, "$HOME"):
			return "/home/tester\n", nil
		case strings.Contains(cmd, "cat >"): // deploy
			return "", nil
		case strings.Contains(cmd, "'agent-deck' version"): // post-deploy $PATH check
			return "Agent Deck v1.16.15\n", nil
		}
		return "", nil
	})

	if err := r.InstallBinaryWithForce(context.Background(), []byte("BINARY"), "1.16.15", true); err != nil {
		t.Fatalf("InstallBinaryWithForce: %v", err)
	}

	var sawDeploy bool
	for _, c := range *calls {
		if strings.Contains(c, "cat >") {
			sawDeploy = true
			if !strings.Contains(c, "'1.16.15'") {
				t.Errorf("deploy script call did not carry the expected version 1.16.15: %s", c)
			}
		}
	}
	if !sawDeploy {
		t.Fatal("no deploy ('cat >') command was sent")
	}
}
