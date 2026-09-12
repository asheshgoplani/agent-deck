package session

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/update"
)

// shellRunner executes the deploy command under a local sh with a prelude
// (used to shadow sudo and set the umask), the way the remote's login shell
// would, so the script itself is exercised rather than only its text (#2164).
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

// noSudo shadows sudo with a function that always refuses.
const noSudo = "sudo() { return 1; }; "

func mustMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
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
	if mode := mustMode(t, target); mode != 0o755 {
		t.Errorf("installed mode = %o, want 0755", mode)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "agent-deck" {
			t.Errorf("staging file or lock left behind: %s", e.Name())
		}
	}
}

// A restrictive umask (root's 077 is common) must not leave the binary
// unreadable or non-executable: the mode is set explicitly, not with +x.
func TestDeployScript_RestrictiveUmaskStillInstalls0755(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, "umask 077; "+noSudo)

	if err := r.DeployBinary(context.Background(), []byte("new-binary"), target); err != nil {
		t.Fatalf("DeployBinary: %v", err)
	}
	if mode := mustMode(t, target); mode != 0o755 {
		t.Errorf("installed mode = %o under umask 077, want 0755", mode)
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
	r := shellRunner(t, noSudo)

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

// Passwordless sudo: the stand-in answers the `sudo -n sh -c true` probe,
// then runs the real script the way root would, with write access to the
// directory and root's usual umask 077. The installed binary must still be
// 0755 and complete.
func TestDeployScript_UnwritableDirUsesPasswordlessSudo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; the sudo branch is never reached")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	target := filepath.Join(dir, "agent-deck")
	log := filepath.Join(t.TempDir(), "sudo.log")
	fakeSudo := "sudo() { shift; printf '%s\\n' \"$*\" >> " + shellQuote(log) + "; chmod 755 " + shellQuote(dir) + "; umask 077; \"$@\"; }; "
	r := shellRunner(t, fakeSudo)

	if err := r.DeployBinary(context.Background(), []byte("root-binary"), target); err != nil {
		t.Fatalf("DeployBinary through sudo: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "root-binary" {
		t.Fatalf("installed %q, want root-binary", got)
	}
	if mode := mustMode(t, target); mode != 0o755 {
		t.Errorf("installed mode = %o under sudo with umask 077, want 0755", mode)
	}
	logged, _ := os.ReadFile(log)
	if !strings.HasPrefix(string(logged), "sh -c true\nsh -c ") || !strings.HasSuffix(strings.TrimSpace(string(logged)), " sh "+dir+" "+target) {
		t.Errorf("sudo must be probed with `sh -c true` and then run the script with the dir and target; got:\n%s", logged)
	}
}

// The probe passing does not prove the real command is allowed: when sudo
// refuses the script, the failure is still the not-writable error with the
// remedy, carrying sudo's own message.
func TestDeployScript_SudoRefusesRealCommandKeepsRemedy(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can write anywhere; the sudo branch is never reached")
	}
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	r := shellRunner(t, "sudo() { shift; [ \"$3\" = true ] && return 0; echo 'sudo: a password is required' >&2; return 1; }; ")

	err := r.DeployBinary(context.Background(), []byte("new-binary"), target)
	var notWritable *update.InstallPathNotWritableError
	if !errors.As(err, &notWritable) {
		t.Fatalf("got %v, want InstallPathNotWritableError", err)
	}
	if !strings.Contains(err.Error(), "a password is required") {
		t.Errorf("sudo's own message must be kept: %v", err)
	}
}

// Two deploys onto the same path never share a staging file: the second
// either waits its turn (here: is refused as busy while the lock is held)
// and the installed binary is always one complete payload.
func TestDeployScript_ConcurrentDeploysNeverCorrupt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	target := filepath.Join(dir, "agent-deck")
	payloads := []string{strings.Repeat("A", 1<<20), strings.Repeat("B", 1<<20)}

	var wg sync.WaitGroup
	errs := make([]error, len(payloads))
	for i, payload := range payloads {
		wg.Add(1)
		go func(i int, payload string) {
			defer wg.Done()
			// Hold the payload open briefly so the two writes overlap.
			r := shellRunner(t, noSudo+"deploy_delay() { sleep 0.2; }; deploy_delay; ")
			errs[i] = r.DeployBinary(context.Background(), []byte(payload), target)
		}(i, payload)
	}
	wg.Wait()

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("nothing installed: %v", err)
	}
	if string(got) != payloads[0] && string(got) != payloads[1] {
		t.Fatalf("installed binary is not one complete payload (len %d, first byte %q)", len(got), got[:1])
	}
	successes := 0
	for _, e := range errs {
		switch {
		case e == nil:
			successes++
		case errors.Is(e, ErrRemoteDeployBusy):
		default:
			t.Errorf("unexpected failure: %v", e)
		}
	}
	if successes == 0 {
		t.Fatal("at least one deploy must succeed")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "agent-deck" {
			t.Errorf("staging file or lock left behind: %s", e.Name())
		}
	}
}

// A lock left by a crashed deploy is honoured while fresh and cleared once
// it is old enough to be abandoned.
func TestDeployScript_LockIsHonouredThenExpires(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "agent-deck")
	lock := target + ".lock"
	if err := os.Mkdir(lock, 0o755); err != nil {
		t.Fatal(err)
	}
	r := shellRunner(t, noSudo)

	err := r.DeployBinary(context.Background(), []byte("new-binary"), target)
	if !errors.Is(err, ErrRemoteDeployBusy) {
		t.Fatalf("a fresh lock must refuse the deploy as busy, got %v", err)
	}
	if _, statErr := os.Stat(target); !os.IsNotExist(statErr) {
		t.Fatal("nothing may be installed while the lock is held")
	}

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(lock, old, old); err != nil {
		t.Fatal(err)
	}
	if err := r.DeployBinary(context.Background(), []byte("new-binary"), target); err != nil {
		t.Fatalf("an abandoned lock must be cleared: %v", err)
	}
	if got, _ := os.ReadFile(target); string(got) != "new-binary" {
		t.Fatalf("installed %q, want new-binary", got)
	}
}
