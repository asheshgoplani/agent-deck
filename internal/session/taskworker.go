package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Issue #1214: kernel-exact task-worker completion.
//
// A conductor that dispatches a discrete task does not need a persistent
// interactive pane — it needs a worker that does the task and reports back. Run
// that worker ONE-SHOT (e.g. `claude -p "<task>"`, `codex exec`, any command
// that exits when done) under the thin wrapper below. When the worker process
// EXITS, the kernel delivers that edge exactly once via cmd.Wait(); the wrapper
// parses the last #1186 sentinel (last-wins), writes a durable completion
// record, and wakes the parent's live session exactly once through the existing
// notifier (parent resolution + busy-defer queue + inbox reused verbatim).
//
// This is the structural replacement for poll-inference done-detection on the
// task-worker path: exactly-once is the kernel's guarantee, not something the
// daemon reconstructs with a freshness window and multi-layer dedup. The
// daemon's Stop-hook/sentinel path is kept INTACT for persistent INTERACTIVE
// sessions, which never exit; see emitDoneSignals' completion-record guard.
//
// The mechanism is tool-agnostic: the wrapper runs whatever command it is
// handed and only depends on the #1186 sentinel contract, not on claude.

// CompletionRecord is the durable, atomically-written record a task-worker
// wrapper produces. A record with an empty Status is a "claim" written when the
// worker starts (so the daemon suppresses poll-inference for the whole run,
// closing the race against the worker's own Stop hook); it is finalized to
// ok|fail on exit.
type CompletionRecord struct {
	ChildID    string    `json:"child_id"`
	Profile    string    `json:"profile"`
	Title      string    `json:"title,omitempty"`
	Status     string    `json:"status"` // "" = pending; "ok" | "fail" = finished
	Summary    string    `json:"summary,omitempty"`
	ExitCode   int       `json:"exit_code"`
	CreatedAt  time.Time `json:"created_at"`
	FinishedAt time.Time `json:"finished_at,omitzero"`
	Acked      bool      `json:"acked"`
}

func completionsDir() (string, error) {
	dir, err := GetAgentDeckDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "runtime", "completions"), nil
}

// safeRecordName keeps a child id usable as a single path segment, defending the
// completions directory against traversal from an unexpected id value.
func safeRecordName(childID string) string {
	cleaned := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, childID)
	if cleaned == "" {
		return "_"
	}
	return cleaned
}

func completionRecordPath(childID string) (string, error) {
	dir, err := completionsDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, safeRecordName(childID)+".json"), nil
}

// WriteCompletionRecord persists a record atomically (tmp + rename).
func WriteCompletionRecord(rec CompletionRecord) error {
	if strings.TrimSpace(rec.ChildID) == "" {
		return errors.New("completion record: empty child id")
	}
	path, err := completionRecordPath(rec.ChildID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readCompletionRecord(childID string) (CompletionRecord, bool) {
	path, err := completionRecordPath(childID)
	if err != nil {
		return CompletionRecord{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return CompletionRecord{}, false
	}
	var rec CompletionRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return CompletionRecord{}, false
	}
	return rec, true
}

// CompletionRecordExists reports whether a wrapper has claimed/finished the
// given child under the given profile. The daemon uses it to stand down from
// poll-inference: if the kernel-exit path owns this child, the Stop-hook path
// must not also fire.
func CompletionRecordExists(profile, childID string) bool {
	rec, ok := readCompletionRecord(childID)
	if !ok {
		return false
	}
	return rec.Profile == profile
}

// LoadCompletionRecords returns every record for the given profile.
func LoadCompletionRecords(profile string) ([]CompletionRecord, error) {
	dir, err := completionsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []CompletionRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var rec CompletionRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.Profile != profile {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

// AckCompletion marks a record delivered so replay never re-fires it.
func AckCompletion(profile, childID string) error {
	rec, ok := readCompletionRecord(childID)
	if !ok || rec.Profile != profile {
		return nil
	}
	if rec.Acked {
		return nil
	}
	rec.Acked = true
	return WriteCompletionRecord(rec)
}

// deriveCompletion turns captured worker output + exit code into a finished
// record. The #1186 sentinel wins when present (last-wins via ScanDoneSentinel);
// otherwise the exit code decides — a one-shot worker that exits 0 finished its
// task, a non-zero exit without an assertion is a failure, not a silent success.
func deriveCompletion(childID, profile, title, output string, exitCode int) CompletionRecord {
	rec := CompletionRecord{
		ChildID:    childID,
		Profile:    profile,
		Title:      title,
		ExitCode:   exitCode,
		CreatedAt:  time.Now(),
		FinishedAt: time.Now(),
	}
	if sig, ok := ScanDoneSentinel(output); ok {
		rec.Status = sig.Status
		rec.Summary = sig.Summary
		return rec
	}
	if exitCode == 0 {
		rec.Status = "ok"
	} else {
		rec.Status = "fail"
	}
	return rec
}

// RunTaskWorker runs the one-shot worker command to completion. It claims the
// child first (so the daemon suppresses poll-inference for the whole run),
// blocks on the kernel exit via cmd.Run/Wait (exactly-once by construction),
// derives the completion from captured output + exit code, and writes the
// finalized durable record. It does NOT itself wake the parent — that is the
// caller's (DeliverCompletion) so the same record can be replayed if the parent
// is down. The worker's own non-zero exit is recorded, not returned as an
// error; a returned error means the wrapper itself failed (record write, etc.).
func RunTaskWorker(childID, profile, title string, cmd *exec.Cmd) (CompletionRecord, error) {
	// Claim: an empty-Status record present for the whole run.
	_ = WriteCompletionRecord(CompletionRecord{
		ChildID:   childID,
		Profile:   profile,
		Title:     title,
		CreatedAt: time.Now(),
	})

	var buf bytes.Buffer
	if cmd.Stdout == nil {
		cmd.Stdout = io.MultiWriter(os.Stdout, &buf)
	} else {
		cmd.Stdout = io.MultiWriter(cmd.Stdout, &buf)
	}
	if cmd.Stderr == nil {
		cmd.Stderr = io.MultiWriter(os.Stderr, &buf)
	} else {
		cmd.Stderr = io.MultiWriter(cmd.Stderr, &buf)
	}

	runErr := cmd.Run()
	exitCode := 0
	if runErr != nil {
		var ee *exec.ExitError
		if errors.As(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			// Command could not be started/found: treat as a failed task.
			exitCode = -1
		}
	}

	rec := deriveCompletion(childID, profile, title, buf.String(), exitCode)
	if err := WriteCompletionRecord(rec); err != nil {
		return rec, err
	}
	return rec, nil
}

// DeliverCompletion wakes the child's parent with a [DONE] event and reports
// whether the wake was accepted into the delivery system (sent, or deferred to
// the retry queue/inbox for a busy parent). A dropped/failed result — e.g. the
// parent (conductor) is down or unresolvable — returns false so the durable
// record stays unacked for replay. Pending (unfinished) records are ignored.
// DeliverCompletion commits a finished one-shot worker's completion to the
// parent's durable per-parent outbox (issue #1225). It returns true ONLY when
// the record durably landed in the inbox — i.e. it is safe to ack the durable
// completion record. It returns false when the parent was unresolvable or a
// transient error occurred, so the completion record stays unacked and the
// daemon's replay re-attempts it (and dead-letters after a bounded budget).
//
// This removes the old ack-on-defer bug (taskworker.go:301): a record is no
// longer acked when it merely reached a volatile in-process retry queue; it is
// acked only once provably committed to the durable inbox.
func (n *TransitionNotifier) DeliverCompletion(rec CompletionRecord) bool {
	if strings.TrimSpace(rec.Status) == "" {
		return false // still running; nothing to deliver
	}
	event := TransitionNotificationEvent{
		ChildSessionID: strings.TrimSpace(rec.ChildID),
		ChildTitle:     strings.TrimSpace(rec.Title),
		Profile:        strings.TrimSpace(rec.Profile),
		Kind:           transitionKindFinished,
		DoneStatus:     strings.ToLower(strings.TrimSpace(rec.Status)),
		DoneSummary:    strings.TrimSpace(rec.Summary),
		Timestamp:      time.Now(),
	}
	if event.ChildSessionID == "" || event.Profile == "" {
		return false
	}
	if isConductorSessionTitle(event.ChildTitle) {
		return false
	}
	// The replay path (ReplayUnackedCompletions) handles the not-committed case
	// via the bounded dead-letter sink, so the terminal reason is discarded here.
	committed, _, _ := n.commitEventToInbox(event)
	return committed
}

// ShouldRecycleForVersion reports whether a long-lived daemon should exit so
// the supervisor restarts it on a freshly-upgraded binary. STEP 1 of issue
// #1214: the transition-notifier unit is Restart=always, so a clean exit on a
// version change guarantees it never keeps running 20-day-stale code. It only
// recycles on a definite mismatch — empty/unknown versions never trigger a flap.
func ShouldRecycleForVersion(running, onDisk string) bool {
	r := strings.TrimSpace(running)
	d := strings.TrimSpace(onDisk)
	if r == "" || d == "" {
		return false
	}
	return r != d
}
