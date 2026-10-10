package sendqueue

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// Send is one message to queue for a target session.
type Send struct {
	SessionID    string
	SessionTitle string
	Tool         string
	// ClaudeSessionID is the target's Claude conversation at queue time
	// (#2397); empty for other tools.
	ClaudeSessionID string
	// Running reports whether the target's pane exists. A send to a target
	// that is not running fails at once.
	Running            bool
	Message            string
	Images             []string
	RequireInputPrompt bool
	// Sender is who queued the send: the calling session's id, or "cli".
	Sender string
	// Deadline ends the wait for a busy target; zero means no deadline.
	Deadline time.Time
	// Ledger also records the send in the Comms Ledger.
	Ledger bool
}

// Enqueue writes s to dir as a new record: queued, or failed at once when
// the target is not running. It records the send in the Comms Ledger when
// asked and publishes the record's state on the profile's event bus. It
// types nothing and starts no worker (SpawnWorker does).
func Enqueue(profile, dir string, s Send, now time.Time) (*Record, error) {
	id, err := NextID(dir, now)
	if err != nil {
		return nil, err
	}
	rec := &Record{
		SendID: id, State: StateQueued, Verdict: "queued", TargetStatus: "unknown",
		SessionID: s.SessionID, SessionTitle: s.SessionTitle, Tool: s.Tool, Message: s.Message, Images: s.Images,
		RequireInputPrompt: s.RequireInputPrompt,
		CreatedAt:          now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano),
		Sender:          s.Sender,
		ClaudeSessionID: s.ClaudeSessionID,
	}
	if !s.Deadline.IsZero() {
		rec.Deadline = s.Deadline.UTC().Format(time.RFC3339Nano)
	}
	if !s.Running {
		rec.State, rec.Reason, rec.Verdict = StateFailed, "target not running", "unknown"
	}
	if err := Save(dir, rec); err != nil {
		return nil, err
	}
	// Comms Ledger (P3): reuse the queue's durable sender identity.
	if s.Ledger {
		session.SpoolCommsSend(rec.Sender, s.SessionID, s.Message, "queue", rec.SendID)
	}
	PublishState(profile, rec)
	return rec, nil
}

// PublishState mirrors a queued send's state on the bus so a client
// following `events follow --kind session.send` never polls send-status.
// sender (additive) lets the sender pick out its own sends.
func PublishState(profile string, r *Record) {
	events.PublishProfile(profile, "session.send", r.SessionID, map[string]string{"send_id": r.SendID, "state": r.State, "verdict": r.Verdict, "reason": r.Reason, "sender": r.Sender})
}

// validWorkerTarget is the hook handler's instance-id guard: a session id
// that matches it cannot be read as a flag or a path.
var validWorkerTarget = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

// SpawnWorker starts a detached worker (`agent-deck session send-worker`)
// for the target. A second worker for the same target exits at once on the
// target lock. sessionID comes from storage or an on-disk queue record, so it
// is checked against the same instance-id guard the hook handler uses before
// it reaches argv.
func SpawnWorker(profile, sessionID string) error {
	if !validWorkerTarget.MatchString(sessionID) || strings.Contains(sessionID, "..") {
		return fmt.Errorf("invalid session id %q", sessionID)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	args := []string{"session", "send-worker", "--target", sessionID}
	if profile != "" {
		args = append([]string{"-p", profile}, args...)
	}
	// #nosec G702 -- exe is this binary (os.Executable), argv is passed as
	// separate arguments with no shell, and sessionID was checked against
	// validWorkerTarget above. gosec's taint analysis does not treat that check
	// as a sanitizer and reaches this call through unrelated flows (#2411).
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// KickWorkers starts a worker for every target in dir (or only sessionID)
// that still has an unfinished queued send, e.g. after a reboot killed the
// old workers. A live worker keeps its target lock, so the extra one exits
// at once.
func KickWorkers(profile, dir, sessionID string) {
	for _, target := range PendingTargets(dir) {
		if sessionID == "" || target == sessionID {
			_ = SpawnWorker(profile, target)
		}
	}
}
