package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// handleRunTask is the task-worker completion wrapper (issue #1214). It runs a
// ONE-SHOT worker command to completion, captures the kernel exit exactly once,
// writes a durable completion record, and wakes the child's parent exactly once.
//
// It is tool-agnostic: the command after `--` is run verbatim, so any tool that
// can run one-shot and exit works (claude -p, codex exec, a shell script, ...).
// The child id and profile are taken from the pane environment that agent-deck
// already injects (AGENTDECK_INSTANCE_ID / AGENTDECK_PROFILE), so a session can
// opt in with nothing more than:
//
//	--wrapper "agent-deck run-task -- {command}"
//
// Flags override the env for non-pane callers and tests.
//
// Usage: agent-deck run-task [--child ID] [--profile P] [--title T] -- <cmd> [args...]
func handleRunTask(args []string) {
	flagArgs := args
	var cmdArgs []string
	for i, a := range args {
		if a == "--" {
			flagArgs = args[:i]
			cmdArgs = args[i+1:]
			break
		}
	}

	child, profile, title := "", "", ""
	for i := 0; i < len(flagArgs); i++ {
		switch flagArgs[i] {
		case "--child":
			if i+1 < len(flagArgs) {
				child = flagArgs[i+1]
				i++
			}
		case "--profile":
			if i+1 < len(flagArgs) {
				profile = flagArgs[i+1]
				i++
			}
		case "--title":
			if i+1 < len(flagArgs) {
				title = flagArgs[i+1]
				i++
			}
		case "-h", "--help":
			runTaskUsage()
			return
		}
	}

	if child == "" {
		child = os.Getenv("AGENTDECK_INSTANCE_ID")
	}
	if profile == "" {
		profile = os.Getenv("AGENTDECK_PROFILE")
	}
	if profile == "" {
		profile = session.DefaultProfile
	}
	if title == "" {
		title = os.Getenv("AGENTDECK_TITLE")
	}

	// Same path-traversal guard the hook handler uses on the instance id.
	if child == "" || !validInstanceID.MatchString(child) || strings.Contains(child, "..") {
		fmt.Fprintln(os.Stderr, "run-task: missing or invalid child id (set AGENTDECK_INSTANCE_ID or pass --child)")
		os.Exit(2)
	}
	if len(cmdArgs) == 0 {
		runTaskUsage()
		os.Exit(2)
	}

	cmd := exec.Command(cmdArgs[0], cmdArgs[1:]...) //nolint:gosec // wrapper deliberately runs the operator-provided one-shot command
	cmd.Stdin = os.Stdin

	rec, err := session.RunTaskWorker(child, profile, title, cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run-task: %v\n", err)
	}

	// Active wake on the exit edge. On success the durable record is acked so
	// the daemon's replay never double-fires; on a down/unresolvable parent it
	// stays unacked and the daemon replays it when the parent returns.
	n := session.NewTransitionNotifier()
	defer n.Close()
	if n.DeliverCompletion(rec) {
		_ = session.AckCompletion(profile, child)
	}

	// Mirror the worker's exit status so callers/tmux see the real outcome.
	if rec.ExitCode > 0 {
		os.Exit(rec.ExitCode)
	}
}

func runTaskUsage() {
	fmt.Println("Usage: agent-deck run-task [--child ID] [--profile P] [--title T] -- <command> [args...]")
	fmt.Println()
	fmt.Println("Run a one-shot task worker under the completion wrapper: the worker")
	fmt.Println("exits when done, the kernel exit is captured exactly once, a durable")
	fmt.Println("completion record is written, and the parent session is woken once.")
	fmt.Println()
	fmt.Println("child/profile default to $AGENTDECK_INSTANCE_ID / $AGENTDECK_PROFILE.")
	fmt.Println()
	fmt.Println("Example (set as a session wrapper):")
	fmt.Println("  agent-deck add -c claude --wrapper \"agent-deck run-task -- {command}\" .")
}
