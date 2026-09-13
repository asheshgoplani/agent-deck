package session

import (
	"context"
	"io"
	"os"
	"os/exec"
)

// RunIO forwards the CLI's streams without buffering or combining diagnostics
// with JSON output. Unlike background status probes, an explicit CLI command
// may legitimately run longer than the probe timeout (for example send --wait).
func (r *SSHRunner) RunIO(ctx context.Context, stdin io.Reader, stdout, stderr io.Writer, args ...string) error {
	if err := ValidateSSHHost(r.Host); err != nil {
		return err
	}
	if err := os.MkdirAll(sshControlDir, 0700); err != nil {
		return err
	}
	CleanStaleSSHSockets()
	sshArgs := r.sshBaseArgs(r.buildRemoteCommand(args...))
	// #nosec G204 -- Literal ssh executable; fixed connection options and a
	// validated non-option host. Remote executable/profile/arguments are each
	// shell-quoted by buildRemoteCommand, with no local shell evaluation.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}
