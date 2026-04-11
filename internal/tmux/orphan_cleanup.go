package tmux

import (
	"bufio"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// CleanupOrphanedControlPipes finds and terminates stale `tmux -C attach-session`
// subscriber processes that belong to agent-deck (session name starts with
// SessionPrefix) but whose parent agent-deck process has died. When an
// agent-deck instance crashes without running its shutdown hooks, its control-
// mode children are reparented to PID 1 (launchd on macOS, init/systemd on
// Linux), leaving them as orphaned subscribers that keep consuming tmux event
// fan-out for no reason. Running multiple instances amplifies the problem: each
// crash leaves behind one orphan per tracked session.
//
// An orphan is identified by two conditions:
//  1. Command matches `tmux -C attach-session -t agentdeck_*`
//  2. Parent PID is 1 (reparented to init)
//
// Returns the number of orphans terminated. Individual kill failures are logged
// but not returned as errors so one unkillable process doesn't block cleanup of
// the rest. A ps-enumeration failure returns the error without killing anything.
func CleanupOrphanedControlPipes() (int, error) {
	// `ps -axo pid=,ppid=,command=` is portable across macOS and Linux.
	// The `=` suppresses the header row.
	out, err := exec.Command("ps", "-axo", "pid=,ppid=,command=").Output()
	if err != nil {
		return 0, err
	}

	killed := 0
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		command := strings.Join(fields[2:], " ")

		if !isAgentDeckControlPipe(command) {
			continue
		}
		if ppid != 1 {
			continue
		}

		if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
			pipeLog.Debug("orphan_kill_failed",
				slog.Int("pid", pid),
				slog.String("error", err.Error()))
			continue
		}
		pipeLog.Info("orphan_pipe_killed",
			slog.Int("pid", pid),
			slog.String("command", command))
		killed++
	}

	return killed, nil
}

// isAgentDeckControlPipe returns true for a process whose command line is a
// tmux control-mode subscriber attached to an agent-deck session. We require
// the SessionPrefix match so we never touch tmux processes the user started
// manually or that belong to other tools.
func isAgentDeckControlPipe(command string) bool {
	if !strings.Contains(command, "tmux") {
		return false
	}
	if !strings.Contains(command, "-C") {
		return false
	}
	if !strings.Contains(command, "attach-session") {
		return false
	}
	return strings.Contains(command, SessionPrefix)
}
