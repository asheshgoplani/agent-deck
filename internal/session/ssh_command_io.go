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
	// ValidateSSHHost checks the destination and buildRemoteCommand shell-quotes
	// every remote argument. exec receives an argv vector and never invokes a
	// local shell, so caller input cannot be interpreted locally.
	cmd := exec.CommandContext(ctx, "ssh", sshArgs...) //nolint:gosec // validated SSH argv, no shell
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}
