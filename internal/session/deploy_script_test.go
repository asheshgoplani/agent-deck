package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// shellRunner executes the deploy command under a local sh with a prelude
// (used to shadow sudo), the way the remote's login shell would, so the
// script itself is exercised rather than only its text (#2164).
func shellRunner(t *testing.T, prelude string) *SSHRunner {
	t.Helper()
	return &SSHRunner{
		Host:          "tester@remote",
		AgentDeckPath: "agent-deck",
		remoteExecFn: func(ctx context.Context, remoteCmd string, stdin []byte) ([]byte, error) {
			cmd := exec.CommandContext(ctx, "sh", "-c", prelude+remoteCmd)
			cmd.Stdin = strings.NewReader(string(stdin))
			var stderr strings.Builder
			cmd.Stderr = &stderr
			out, err := cmd.Output()
			if err != nil {
				return nil, errors.New("remote command failed: " + err.Error() + ": " + stderr.String())
			}
			return out, nil
		},
	}
}

func TestDeployScript_WritableDirInstallsWithoutSudo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, "sudo() { echo sudo-must-not-run >&2; exit 99; }; ")

	if err := r.DeployBinary(context.Background(), []byte("new-binary"), target); err != nil {
		t.Fatalf("DeployBinary: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new-binary" {
		t.Fatalf("installed %q (%v), want new-binary", got, err)
	}
	if info, _ := os.Stat(target); info.Mode()&0o100 == 0 {
		t.Errorf("installed binary is not executable: %v", info.Mode())
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Errorf("staged file must be renamed away, stat err = %v", err)
	}
}

func TestDeployScript_UnwritableDirWithoutSudoNamesPathAndUser(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; the permission failure cannot be reproduced")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, "sudo() { return 1; }; ")

	err := r.DeployBinary(context.Background(), []byte("new-binary"), target)
	var notWritable *update.InstallPathNotWritableError
	if !errors.As(err, &notWritable) {
		t.Fatalf("got %v, want InstallPathNotWritableError", err)
	}
	if notWritable.Path != target || notWritable.User == "" {
		t.Errorf("error carries path %q user %q, want %q and the remote user", notWritable.Path, notWritable.User, target)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Errorf("nothing may be installed: %v", statErr)
	}
}

func TestDeployScript_UnwritableDirUsesPasswordlessSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; the sudo branch is never reached")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	log := filepath.Join(t.TempDir(), "sudo.log")
	// A stand-in for passwordless sudo: answers `sudo -n true` and records
	// what the deploy hands it. It cannot actually gain privileges, so the
	// install itself still fails, which is what the assertion below checks:
	// the script chose the sudo route and did not report "not writable".
	r := shellRunner(t, "sudo() { shift; printf '%s\\n' \"$*\" >> "+shellQuote(log)+"; \"$@\"; }; ")

	err := r.DeployBinary(context.Background(), []byte("new-binary"), target)
	if err == nil {
		t.Fatal("the fake sudo cannot write the directory; expected a failure")
	}
	var notWritable *update.InstallPathNotWritableError
	if errors.As(err, &notWritable) {
		t.Fatalf("with passwordless sudo the deploy must not report an unwritable path: %v", err)
	}
	logged, _ := os.ReadFile(log)
	if !strings.Contains(string(logged), "true\n") || !strings.Contains(string(logged), "sh -c mkdir -p "+shellQuote(dir)+" && cat > ") {
		t.Errorf("sudo must be probed with -n true and then run the staged install; got:\n%s", logged)
	}
}
