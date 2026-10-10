package sendqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	// Key makes the send idempotent (EnqueueOnce): while a record queued
	// under the same key is kept, the send is not queued again.
	Key string
	// WaitWhileStopped makes the worker wait for a target that is not
	// running, until Deadline if one is set, instead of failing the send.
	WaitWhileStopped bool
}

// Enqueue writes s to dir as a new record: queued, or failed at once when
// the target is not running (unless s.WaitWhileStopped). It records the send
// in the Comms Ledger when asked and publishes the record's state on the
// profile's event bus. It types nothing and starts no worker (SpawnWorker
// does).
func Enqueue(profile, dir string, s Send, now time.Time) (*Record, error) {
	rec, err := newRecord(dir, s, now)
	if err != nil {
		return nil, err
	}
	if err := Save(dir, rec); err != nil {
		return nil, err
	}
	announce(profile, rec, s.Ledger)
	return rec, nil
}

// EnqueueOnce is Enqueue for a send with a Key. When a record queued under
// that key is still kept (RetainFinished after it ends), it returns that
// record with created false and writes nothing.
func EnqueueOnce(profile, dir string, s Send, now time.Time) (_ *Record, created bool, err error) {
	if s.Key == "" {
		return nil, false, errors.New("sendqueue: EnqueueOnce needs a key")
	}
	unlock, err := lockFile(filepath.Join(dir, ".keys.lock"))
	if err != nil {
		return nil, false, err
	}
	defer unlock()
	keyPath := KeyPath(dir, s.Key)
	if b, err := os.ReadFile(keyPath); err == nil {
		if rec, err := Load(dir, strings.TrimSpace(string(b))); err == nil && rec.Key == s.Key {
			return rec, false, nil
		}
	}
	rec, err := newRecord(dir, s, now)
	if err != nil {
		return nil, false, err
	}
	// The key is written first: a crash before the record is saved leaves
	// a key naming no record, which the next call replaces, never a record
	// without its key, which a second call would queue again.
	if err := writeFileAtomic(keyPath, []byte(rec.SendID)); err != nil {
		return nil, false, err
	}
	if err := Save(dir, rec); err != nil {
		return nil, false, err
	}
	announce(profile, rec, s.Ledger)
	return rec, true, nil
}

// KeyPath is the file naming the record queued under key.
func KeyPath(dir, key string) string {
	sum := sha256.Sum256([]byte(key))
	return filepath.Join(dir, "keys", hex.EncodeToString(sum[:16]))
}

func newRecord(dir string, s Send, now time.Time) (*Record, error) {
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
		Key:             s.Key, WaitWhileStopped: s.WaitWhileStopped,
	}
	if !s.Deadline.IsZero() {
		rec.Deadline = s.Deadline.UTC().Format(time.RFC3339Nano)
	}
	if !s.Running && !s.WaitWhileStopped {
		rec.State, rec.Reason, rec.Verdict = StateFailed, "target not running", "unknown"
	}
	return rec, nil
}

// announce spools a new record to the Comms Ledger (when ledger is set) and
// publishes its state.
func announce(profile string, rec *Record, ledger bool) {
	// Comms Ledger (P3): reuse the queue's durable sender identity.
	if ledger {
		session.SpoolCommsSend(rec.Sender, rec.SessionID, rec.Message, "queue", rec.SendID)
	}
	PublishState(profile, rec)
}

// lockFile takes an exclusive flock on path, creating it.
func lockFile(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// writeFileAtomic writes b to path through a temporary file and a rename.
func writeFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
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
