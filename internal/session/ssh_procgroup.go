package session

import (
	"context"
	"errors"
	"math"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/term"
)

// One-shot ssh children run in their own process group. A cancelled or
// timed-out command kills that whole group (ssh plus anything it started,
// such as a ProxyCommand), and the CLI's signal path kills every group still
// tracked here, so a poller that times the CLI out never leaves an ssh child
// reparented to init/launchd holding a remote command open (2026-10 remote
// list pile-up). A ControlPersist master daemonizes into its own session, so
// it is never part of these groups.

// sshServerAliveOpts bound a link that died under a one-shot command: ssh
// gives up after about 45 s without a reply instead of waiting on TCP.
var sshServerAliveOpts = []string{"-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=3"}

// remoteKillGraceSeconds is the remote `timeout -k` grace: a remote command
// that ignores SIGTERM at its deadline is SIGKILLed this much later.
const remoteKillGraceSeconds = 5

// remoteTimeoutSlack keeps the remote deadline a little behind the local
// one, so the local side reports its own timeout first.
const remoteTimeoutSlack = 2 * time.Second

var errSSHShuttingDown = errors.New("ssh: process is shutting down")

// sshGroups tracks the process groups of live one-shot ssh children.
var sshGroups = struct {
	sync.Mutex
	pids    map[int]struct{}
	closing bool
}{pids: map[int]struct{}{}}

// newSSHCommand builds an ssh command whose cancellation kills its whole
// process group.
func newSSHCommand(ctx context.Context, args ...string) *exec.Cmd {
	// #nosec G204 -- literal ssh; every caller passes fixed options, a
	// validated host and a shell-quoted remote command.
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		killSSHGroup(cmd)
		return nil
	}
	cmd.WaitDelay = sshWaitDelay
	return cmd
}

// runSSHCommand is cmd.Run for a newSSHCommand command, with its group
// tracked for the signal path.
func runSSHCommand(cmd *exec.Cmd) error {
	if err := startSSHCommand(cmd); err != nil {
		return err
	}
	return waitSSHCommand(cmd)
}

// startSSHCommand starts cmd and tracks its group until waitSSHCommand.
//
// A terminal stdin is handed to ssh as the file itself, never wrapped in a
// reader: exec would then copy it through a pipe from a goroutine that
// stays blocked reading the terminal after ssh exits, so Wait would fail
// with exec.ErrWaitDelay on every successful command and the goroutine
// would leak. A child in its own process group would be stopped (SIGTTIN)
// by its first read of the terminal, so such a command stays in the
// CLI's foreground group instead; a terminal hangup or ^C reaches it
// directly, and cancellation still kills it by pid.
func startSSHCommand(cmd *exec.Cmd) error {
	if f, ok := cmd.Stdin.(*os.File); ok && term.IsTerminal(int(f.Fd())) { // #nosec G115 -- fd is a small positive int
		if cmd.SysProcAttr != nil {
			cmd.SysProcAttr.Setpgid = false
		}
	}
	sshGroups.Lock()
	defer sshGroups.Unlock()
	if sshGroups.closing {
		return errSSHShuttingDown
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	sshGroups.pids[cmd.Process.Pid] = struct{}{}
	return nil
}

// waitSSHCommand waits for cmd and stops tracking its group.
func waitSSHCommand(cmd *exec.Cmd) error {
	err := cmd.Wait()
	untrackSSHGroup(cmd)
	return err
}

func untrackSSHGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	sshGroups.Lock()
	delete(sshGroups.pids, cmd.Process.Pid)
	sshGroups.Unlock()
}

func killSSHGroup(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	// The group id is the leader's pid (Setpgid with Pgid 0). Kill the
	// leader directly too, in case the group signal raced its setpgid or
	// the command kept the CLI's group (terminal stdin); then no group
	// with that id exists and the group signal is a no-op.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}

// KillSSHProcesses kills every tracked ssh process group and refuses new
// ones. The CLI calls it when it is signalled, right before it exits.
func KillSSHProcesses() {
	sshGroups.Lock()
	defer sshGroups.Unlock()
	sshGroups.closing = true
	for pid := range sshGroups.pids {
		_ = syscall.Kill(-pid, syscall.SIGKILL)
		// An ssh that kept the terminal's foreground group (stdin-reading
		// verb on a tty) has no group of its own: kill it by pid too.
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// remoteTimeoutCommand wraps a shell-quoted remote command so the remote
// host ends it at its own deadline even when ssh vanished and nothing on
// this side can reach it any more. Remotes without timeout(1) run the
// command unwrapped. The quoted words are passed to sh as "$@", never
// re-parsed, so this adds no quoting layer to the command itself.
func remoteTimeoutCommand(remoteCmd string, limit time.Duration) string {
	if limit <= 0 {
		return remoteCmd
	}
	secs := strconv.Itoa(int(math.Ceil((limit + remoteTimeoutSlack).Seconds())))
	script := `if command -v timeout >/dev/null 2>&1; then exec timeout -k ` + strconv.Itoa(remoteKillGraceSeconds) + ` ` + secs + ` "$@"; fi; exec "$@"`
	return "exec /bin/sh -c " + shellQuote(script) + " sh " + remoteCmd
}

// remoteCommandWithDeadline applies remoteTimeoutCommand with the time
// left on ctx, or returns remoteCmd unchanged when ctx has no deadline.
func remoteCommandWithDeadline(ctx context.Context, remoteCmd string) string {
	deadline, ok := ctx.Deadline()
	if !ok {
		return remoteCmd
	}
	return remoteTimeoutCommand(remoteCmd, time.Until(deadline))
}
