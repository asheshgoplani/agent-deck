package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/childenv"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Issue #2897: `session switch-account` and `session restart` run from a tool
// call inside the session they target kill themselves partway. Their restart
// stops the session's tmux pane, and the calling agent-deck process lives in
// that pane, so it dies before the account commit (switch-account) or the
// new start (restart) ever runs. The session comes back on the old account
// with `account` unset, or not at all.
//
// When the target is the calling session, the command is handed to a copy of
// itself started in a new session (setsid): no controlling terminal, so the
// pane's SIGHUP does not reach it, and its output goes to a log file instead
// of the dying pane. The parent prints the receipt and exits; the child waits
// for the parent to be gone (so it is no longer in the pane's process tree,
// which the stop reaps) and then runs the unchanged command. Calls from
// anywhere else run in place exactly as before.

// selfHandoffParentEnv marks the detached child and carries its parent's pid.
const selfHandoffParentEnv = "AGENTDECK_SELF_HANDOFF_PARENT"

// selfHandoffParentWait bounds the child's wait for its parent to exit. The
// parent exits right after printing the receipt; the bound only keeps a
// stuck parent from holding the switch forever.
const selfHandoffParentWait = 10 * time.Second

// selfHandoff is a started detached child.
type selfHandoff struct {
	PID int
	Log string
}

// Seams for tests.
var (
	selfHandoffCallerID = func() string { return strings.TrimSpace(os.Getenv("AGENTDECK_INSTANCE_ID")) }
	selfHandoffTmuxName = currentAgentDeckTmuxName
	selfHandoffStart    = startSelfHandoff
)

// callerRunsInsideTarget reports whether this process runs in inst's own
// pane: $AGENTDECK_INSTANCE_ID names it, or the calling tmux session is its
// tmux session (the id can be stale in a Claude background worker, #2683, so
// the pane is checked too). Always false in the detached child.
func callerRunsInsideTarget(inst *session.Instance) bool {
	if inst == nil || os.Getenv(selfHandoffParentEnv) != "" {
		return false
	}
	if id := selfHandoffCallerID(); id != "" && id == inst.ID {
		return true
	}
	name := selfHandoffTmuxName()
	if name == "" {
		return false
	}
	ts := inst.GetTmuxSession()
	return ts != nil && ts.Name == name
}

// selfHandoffEnviron is the detached child's environment: the caller's,
// without AGENTDECK_INSTANCE_ID (the child no longer runs inside the
// session) and with exactly one parent marker.
func selfHandoffEnviron(environ []string, parentPID int) []string {
	env := childenv.SelfReexecFilter(environ, "AGENTDECK_INSTANCE_ID", selfHandoffParentEnv)
	return append(env, selfHandoffParentEnv+"="+strconv.Itoa(parentPID))
}

// selfHandoffCommand builds the detached child: this binary with the same
// profile and subcommand, in its own session, stdin closed and output to log.
func selfHandoffCommand(exe, profile string, sub, environ []string, parentPID int, log *os.File) *exec.Cmd {
	// exe is this agent-deck binary (os.Executable) and argv goes straight to
	// execve with no shell: sub is the caller's own argv for this command.
	cmd := exec.Command(exe, profileArgs(profile, sub...)...) //nolint:gosec // G702: own binary, caller's own argv, no shell (see above)
	cmd.Stdin = nil
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Env = selfHandoffEnviron(environ, parentPID)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	return cmd
}

// startSelfHandoff starts the detached child for sub ("session", verb, args...)
// and returns its pid and log path.
func startSelfHandoff(profile string, sub []string) (selfHandoff, error) {
	exe, err := os.Executable()
	if err != nil {
		return selfHandoff{}, fmt.Errorf("locating the agent-deck binary: %w", err)
	}
	verb := "command"
	if len(sub) > 1 {
		verb = sub[1]
	}
	log, err := os.CreateTemp("", "agent-deck-"+verb+"-*.log")
	if err != nil {
		return selfHandoff{}, fmt.Errorf("creating the handoff log: %w", err)
	}
	cmd := selfHandoffCommand(exe, profile, sub, childenv.ForSelfReexec(), os.Getpid(), log)
	if err := cmd.Start(); err != nil {
		_ = log.Close() // nothing was written; the start error is the one to report
		return selfHandoff{}, fmt.Errorf("starting the detached agent-deck: %w", err)
	}
	// The child holds its own copy of the descriptor.
	_ = log.Close()
	h := selfHandoff{PID: cmd.Process.Pid, Log: log.Name()}
	return h, cmd.Process.Release()
}

// awaitSelfHandoffParent runs in the detached child: it clears the parent
// marker (nothing the child starts may inherit it) and waits, bounded, until
// the parent has exited, so the child is out of the pane's process tree
// before the command stops the pane.
func awaitSelfHandoffParent() bool {
	v, ok := os.LookupEnv(selfHandoffParentEnv)
	if !ok || v == "" {
		return false
	}
	_ = os.Unsetenv(selfHandoffParentEnv)
	parent, err := strconv.Atoi(v)
	if err != nil || parent <= 1 {
		return true
	}
	deadline := time.Now().Add(selfHandoffParentWait)
	for os.Getppid() == parent && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	return true
}

// handOffIfSelfTarget hands `session <verb> args...` to a detached child when
// this process runs inside inst's pane, prints the receipt and returns true;
// the caller must then return without doing the work. It returns false (run
// in place) for any other caller and in the detached child itself.
func handOffIfSelfTarget(out *CLIOutput, profile, verb string, args []string, inst *session.Instance) bool {
	if awaitSelfHandoffParent() || !callerRunsInsideTarget(inst) {
		return false
	}
	sub := append([]string{"session", verb}, args...)
	return handOffSelfCommand(out, profile, verb, sub, inst)
}

// handOffSelfCommand starts a command whose self-target has already been resolved.
func handOffSelfCommand(out *CLIOutput, profile, verb string, sub []string, inst *session.Instance) bool {
	h, err := selfHandoffStart(profile, sub)
	if err != nil {
		// Running in place would kill this process mid-command; refuse.
		out.Error(fmt.Sprintf("%s targets the session this command runs in, and handing it to a detached process failed: %v; run it from another shell", verb, err), ErrCodeInvalidOperation)
		exitCLI(1)
		return true
	}
	var msg string
	if verb == "restart --all" {
		msg = fmt.Sprintf("Handed restart --all to a detached agent-deck (pid %d): the batch targets all active sessions, including %s; restarting the invoking session ends this pane. Read the batch result in %s.",
			h.PID, inst.Title, h.Log)
	} else {
		msg = fmt.Sprintf("Handed %s of %s to a detached agent-deck (pid %d): it targets the session this command runs in, whose restart ends this pane. Output: %s. Check the result with `agent-deck session show %s` once the session is back.",
			verb, inst.Title, h.PID, h.Log, inst.ID)
	}
	out.Success(msg, map[string]interface{}{
		"success":    true,
		"handed_off": true,
		"command":    verb,
		"id":         inst.ID,
		"title":      inst.Title,
		"pid":        h.PID,
		"log":        h.Log,
	})
	out.QuietNotice(msg)
	return true
}

// handOffSelfRestart is handOffIfSelfTarget for `session restart <id|--all>`,
// which resolves its session later (or in the core registry): it resolves
// identifier only when the caller could be inside a session at all.
func handOffSelfRestart(out *CLIOutput, profile string, args []string, identifier string, all bool) bool {
	if awaitSelfHandoffParent() || (!all && identifier == "") {
		return false
	}
	callerID, callerTmux := selfHandoffCallerID(), selfHandoffTmuxName()
	if callerID == "" && callerTmux == "" {
		return false
	}
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		return false // the restart path reports it
	}
	_ = storage.Close()
	if all {
		return handOffActiveSelfRestart(out, profile, args, instances, callerID, callerTmux)
	}
	inst, _, _ := ResolveSession(identifier, instances)
	if inst == nil {
		return false
	}
	ts := inst.GetTmuxSession()
	if inst.ID != callerID && (callerTmux == "" || ts == nil || ts.Name != callerTmux) {
		return false
	}
	return handOffSelfCommand(out, profile, "restart", append([]string{"session", "restart"}, args...), inst)
}

// The legacy handler already loaded its profile's instances. Consume the child
// marker once, then reuse that snapshot rather than reopening storage.
func handOffSelfRestartAll(out *CLIOutput, profile string, args []string, instances []*session.Instance) bool {
	if awaitSelfHandoffParent() {
		return false
	}
	return handOffActiveSelfRestart(out, profile, args, instances, selfHandoffCallerID(), selfHandoffTmuxName())
}

func handOffActiveSelfRestart(out *CLIOutput, profile string, args []string, instances []*session.Instance, callerID, callerTmux string) bool {
	// Prefer the actual pane when a background worker inherited a stale ID.
	// Only probe matching candidates: batch membership is Exists, not Status.
	var target *session.Instance
	if callerTmux != "" {
		for _, inst := range instances {
			ts := inst.GetTmuxSession()
			if ts != nil && ts.Name == callerTmux && inst.Exists() {
				target = inst
				break
			}
		}
	}
	if target == nil && callerID != "" {
		for _, inst := range instances {
			if inst.ID == callerID && inst.Exists() {
				target = inst
				break
			}
		}
	}
	if target == nil {
		return false
	}
	return handOffSelfCommand(out, profile, "restart --all", append([]string{"session", "restart"}, args...), target)
}
