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
	cmd := exec.CommandContext(ctx, "ssh", r.sshBaseArgs(r.buildRemoteCommand(args...))...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}
