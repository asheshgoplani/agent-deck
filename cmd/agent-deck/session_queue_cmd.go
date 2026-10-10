package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
	"github.com/fsnotify/fsnotify"
)

// `session queue` lets a client see the durable queued sends of a session
// and act on one: release it now, or cancel it before it is typed. The
// target's worker stays the only process that types or changes a queued
// record; the CLI files a control request and reports the worker's answer.

// queueReleaseStatus is the target_status a record carries while a release
// types it, so a control caller waits for the release's own answer.
const queueReleaseStatus = "release"

// queueControlTimeout bounds how long `session queue release|cancel` waits
// for the worker's answer.
func queueControlTimeout() time.Duration {
	return envDuration("AGENTDECK_QUEUE_CONTROL_TIMEOUT", 3*time.Minute)
}

// sessionQueueEntry is the small, stable view used by both queue list and show.
type sessionQueueEntry struct {
	ID          string `json:"id"`
	TextPreview string `json:"text_preview"`
	EnqueuedAt  string `json:"enqueued_at"`
	State       string `json:"state"`
}

func sessionQueueEntries(storage *session.Storage, sessionID string) ([]sessionQueueEntry, error) {
	records, err := sendqueue.List(sendQueueDir(storage), sessionID)
	if err != nil {
		return nil, err
	}
	entries := make([]sessionQueueEntry, 0, len(records))
	for _, record := range records {
		preview := strings.Join(strings.Fields(record.Message), " ")
		const previewRunes = 120
		if runes := []rune(preview); len(runes) > previewRunes {
			preview = string(runes[:previewRunes]) + "…"
		}
		entries = append(entries, sessionQueueEntry{
			ID: record.SendID, TextPreview: preview,
			EnqueuedAt: record.CreatedAt, State: record.State,
		})
	}
	return entries, nil
}

func handleSessionQueue(profile string, args []string) {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printSessionQueueHelp()
		return
	}
	switch args[0] {
	case "list":
		handleSessionQueueList(profile, args[1:])
	case "release", "cancel":
		handleSessionQueueControl(profile, args)
	default:
		fmt.Fprintf(os.Stderr, "Error: unknown session queue command: %s\n", args[0])
		printSessionQueueHelp()
		exitCLI(2)
	}
}

func printSessionQueueHelp() {
	fmt.Println("Usage: agent-deck session queue <list|release|cancel> [arguments] [--json]")
	fmt.Println()
	fmt.Println("  list <session>  List durable queued sends by ID, preview, enqueue time, and state")
	fmt.Println("  release <id>    Send a queued entry now and report the confirmed outcome")
	fmt.Println("  cancel <id>     Cancel an unsent entry or report that it was already sent")
}

func handleSessionQueueList(profile string, args []string) {
	fs := flag.NewFlagSet("session queue list", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session queue list <session> [--json]")
		fmt.Println()
		fmt.Println("List durable queued sends, including retained finished entries, oldest first.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exitCLI(2)
	}
	if fs.NArg() != 1 {
		fs.Usage()
		exitCLI(2)
	}
	out := NewCLIOutput(*jsonOutput, false)
	storage, instances, _, err := loadSessionData(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeNotFound)
		exitCLI(1)
	}
	defer storage.Close()
	inst, errMsg, errCode := ResolveSession(fs.Arg(0), instances)
	if inst == nil {
		out.Error(errMsg, errCode)
		if errCode == ErrCodeNotFound {
			exitCLI(2)
		}
		exitCLI(1)
	}
	entries, err := sessionQueueEntries(storage, inst.ID)
	if err != nil {
		out.Error(fmt.Sprintf("cannot list queued sends: %v", err), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if *jsonOutput {
		out.Print("", map[string]interface{}{"session_id": inst.ID, "queue": entries})
		return
	}
	if len(entries) == 0 {
		fmt.Printf("No queued sends for %s\n", inst.Title)
		return
	}
	for _, entry := range entries {
		fmt.Printf("%s  %s  %s  %s\n", entry.ID, entry.State, entry.EnqueuedAt, entry.TextPreview)
	}
}

// queueControlResult is the worker's answer to a control request.
type queueControlResult struct {
	Outcome string            `json:"outcome"`
	Reason  string            `json:"reason,omitempty"`
	Record  *sendqueue.Record `json:"record,omitempty"`
}

// requestQueueControl files a release or cancel request. O_EXCL keeps one
// request per entry at a time.
func requestQueueControl(dir, id, operation string) error {
	f, err := os.OpenFile(sendqueue.ControlPath(dir, id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = f.WriteString(operation)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// queueControlReply writes the answer atomically, so the waiting CLI never
// reads half of it.
func queueControlReply(dir, id string, result queueControlResult) {
	b, err := json.Marshal(result)
	if err != nil {
		return
	}
	tmp := filepath.Join(dir, "."+id+".control-result.tmp")
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, sendqueue.ControlResultPath(dir, id))
	}
}

// queueAlreadySent answers a control request for a record that has left
// queued. already_sent is claimed only with the child's delivery evidence.
func queueAlreadySent(r *sendqueue.Record) queueControlResult {
	switch {
	case r.State == sendqueue.StateCancelled:
		return queueControlResult{Outcome: "cancelled", Record: r}
	case r.State == sendqueue.StateFailed:
		// failed is only ever written when nothing was typed: there is no
		// pending entry left to release or cancel.
		return queueControlResult{Outcome: "not_found", Reason: r.Reason, Record: r}
	case r.State == sendqueue.StateTyping || (r.Attempts > 0 && len(r.DeliveryEvidence) == 0):
		return queueControlResult{Outcome: "unknown", Reason: "delivery is in progress or lacks child evidence", Record: r}
	}
	return queueControlResult{Outcome: "already_sent", Reason: r.Reason, Record: r}
}

// queueReleaseResult maps the record a release left behind to its outcome.
// delivered needs confirmed submission or a landed row; a typed record is
// unconfirmed whatever its verdict says.
func queueReleaseResult(rec *sendqueue.Record) queueControlResult {
	result := queueControlResult{Record: rec}
	switch rec.State {
	case sendqueue.StateQueued:
		result.Outcome, result.Reason = "refused", strings.TrimPrefix(rec.Reason, "retrying: ")
	case sendqueue.StateFailed:
		result.Outcome, result.Reason = "refused", rec.Reason
	case sendqueue.StateSubmitted, sendqueue.StateLanded:
		result.Outcome = "delivered"
	default:
		result.Outcome, result.Reason = "unconfirmed", rec.Reason
	}
	return result
}

// processQueueControl runs only in the target's worker, under its per-target
// lock, and answers a pending control request for rec. It returns true when
// it answered one. The answer follows the durable state transition or the
// child's result.
func processQueueControl(profile, dir string, rec *sendqueue.Record) bool {
	if _, err := os.Stat(sendqueue.ControlResultPath(dir, rec.SendID)); err == nil {
		return false
	}
	b, err := os.ReadFile(sendqueue.ControlPath(dir, rec.SendID))
	if err != nil {
		return false
	}
	op := strings.TrimSpace(string(b))
	if rec.State != sendqueue.StateQueued {
		queueControlReply(dir, rec.SendID, queueAlreadySent(rec))
		return true
	}
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err != nil {
			return err
		}
		queuedSendChanged(profile, rec, r)
		*rec = *r
		return nil
	}
	switch op {
	case "cancel":
		if err := set(func(r *sendqueue.Record) {
			r.State, r.Verdict, r.Reason = sendqueue.StateCancelled, "cancelled", "cancelled before typing"
		}); err != nil {
			queueControlReply(dir, rec.SendID, queueControlResult{Outcome: "unknown", Reason: err.Error()})
			return true
		}
		events.PublishProfile(profile, "queue.cancelled", rec.SessionID, map[string]string{"id": rec.SendID, "state": rec.State})
		queueControlReply(dir, rec.SendID, queueControlResult{Outcome: "cancelled", Record: rec})
		return true
	case "release":
	default:
		queueControlReply(dir, rec.SendID, queueControlResult{Outcome: "unknown", Reason: "invalid queue operation"})
		return true
	}
	refuse := func(reason string) bool {
		queueControlReply(dir, rec.SendID, queueControlResult{Outcome: "refused", Reason: reason, Record: rec})
		return true
	}
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return refuse(err.Error())
	}
	inst := instanceByID(instances, rec.SessionID)
	if inst == nil || !inst.Exists() {
		return refuse("target not running")
	}
	// Release skips the queue's wait and backoff, never the harness's own
	// readiness: a target that cannot take input now is refused untyped.
	status, _ := fetchHookDrivenStatus(profile, inst.ID)
	if shouldWaitForIdle(inst.Tool, status) {
		if status == "" {
			status = "unknown"
		}
		return refuse(fmt.Sprintf("%s: %s is %s; nothing was typed", deliveryTargetBusy, inst.Tool, status))
	}
	path := session.LiveTranscriptPath(inst, instances)
	var from int64
	if info, err := os.Stat(path); err == nil {
		from = info.Size()
	}
	if !typeQueued(profile, dir, rec, queueReleaseStatus, path, from, set) {
		return refuse("could not record the typing transition; nothing was typed")
	}
	result := queueReleaseResult(rec)
	events.PublishProfile(profile, "queue.released", rec.SessionID, map[string]string{"id": rec.SendID, "state": rec.State, "outcome": result.Outcome, "reason": result.Reason})
	queueControlReply(dir, rec.SendID, result)
	return true
}

// serviceOtherQueueControls answers control requests for the target's later
// queued entries while the worker waits on an older busy one, without a
// second poller.
func serviceOtherQueueControls(profile, dir, target, waitingID string) {
	recs, _ := sendqueue.List(dir, target)
	for _, rec := range recs {
		if rec.SendID == waitingID || rec.State != sendqueue.StateQueued {
			continue
		}
		if _, err := os.Stat(sendqueue.ControlPath(dir, rec.SendID)); err == nil {
			processQueueControl(profile, dir, rec)
		}
	}
}

// queueControlPending reports an unanswered control request for target.
func queueControlPending(dir, target string) bool {
	paths, _ := filepath.Glob(filepath.Join(dir, "*.control"))
	for _, p := range paths {
		id := strings.TrimSuffix(filepath.Base(p), ".control")
		if _, err := os.Stat(sendqueue.ControlResultPath(dir, id)); err == nil {
			continue
		}
		if r, err := sendqueue.Load(dir, id); err == nil && r.SessionID == target && r.State == sendqueue.StateQueued {
			return true
		}
	}
	return false
}

// sleepUnlessQueueControl waits d, or less when a control request for the
// target arrives, so release and cancel do not wait out a retry backoff.
func sleepUnlessQueueControl(dir, target string, d time.Duration) {
	const slice = 200 * time.Millisecond
	end := time.Now().Add(d)
	for {
		left := time.Until(end)
		if left <= 0 || queueControlPending(dir, target) {
			return
		}
		if left > slice {
			left = slice
		}
		time.Sleep(left)
	}
}

// handleSessionQueueControl performs a confirmed release or cancellation by
// asking the serial queue worker to decide at the typing boundary.
func handleSessionQueueControl(profile string, args []string) {
	fs := flag.NewFlagSet("session queue", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session queue <release|cancel> <id> [--json]")
		fmt.Println("release sends a pending entry now through the queue worker's guarded send path and reports delivery evidence.")
		fmt.Println("cancel confirms it was removed before typing, or reports already_sent with delivery evidence.")
		fmt.Println("outcome: delivered, unconfirmed, refused (nothing typed), cancelled, already_sent, not_found or unknown.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		exitCLI(2)
	}
	if fs.NArg() != 2 || (fs.Arg(0) != "release" && fs.Arg(0) != "cancel") {
		fs.Usage()
		exitCLI(2)
	}
	op, id := fs.Arg(0), fs.Arg(1)
	out := NewCLIOutput(*jsonOutput, false)
	report := func(result queueControlResult) {
		fields := map[string]interface{}{}
		if result.Record != nil {
			fields = recordFields(result.Record)
		}
		fields["id"], fields["outcome"] = id, result.Outcome
		if result.Reason != "" {
			fields["reason"] = result.Reason
		}
		out.Success(fmt.Sprintf("%s: %s", id, result.Outcome), fields)
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	dir := sendQueueDir(storage)
	storage.Close()
	rec, err := sendqueue.Load(dir, id)
	if errors.Is(err, sendqueue.ErrUnknown) {
		report(queueControlResult{Outcome: "not_found"})
		return
	}
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	if rec.State != sendqueue.StateQueued && rec.State != sendqueue.StateTyping {
		report(queueAlreadySent(rec))
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		out.Error("cannot watch queue control result: "+err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(dir); err != nil {
		out.Error("cannot watch queue control result: "+err.Error(), ErrCodeInvalidOperation)
		exitCLI(1)
	}
	// A typing entry is not asked anything: its send is already under way
	// and the answer is the evidence its child leaves behind.
	if rec.State == sendqueue.StateQueued {
		if err := requestQueueControl(dir, id, op); err != nil {
			out.Error(fmt.Sprintf("queue entry %s already has a control request: %v", id, err), ErrCodeInvalidOperation)
			exitCLI(1)
		}
		_ = sendqueue.SpawnWorker(profile, rec.SessionID)
	}
	readOutcome := func() bool {
		if b, err := os.ReadFile(sendqueue.ControlResultPath(dir, id)); err == nil {
			var result queueControlResult
			if json.Unmarshal(b, &result) == nil {
				_ = os.Remove(sendqueue.ControlResultPath(dir, id))
				_ = os.Remove(sendqueue.ControlPath(dir, id))
				report(result)
				return true
			}
		}
		// The normal dequeue may have crossed the typing boundary just
		// before the control file appeared. Wait for its child evidence.
		current, err := sendqueue.Load(dir, id)
		if err != nil || current.State == sendqueue.StateQueued || current.State == sendqueue.StateTyping ||
			current.State == sendqueue.StateCancelled || (op == "release" && current.TargetStatus == queueReleaseStatus) {
			return false
		}
		_ = os.Remove(sendqueue.ControlPath(dir, id))
		report(queueAlreadySent(current))
		return true
	}
	timer := time.NewTimer(queueControlTimeout())
	defer timer.Stop()
	for {
		if readOutcome() {
			return
		}
		select {
		case <-watcher.Events:
		case err := <-watcher.Errors:
			report(queueControlResult{Outcome: "unknown", Reason: fmt.Sprintf("queue watcher failed: %v", err)})
			return
		case <-timer.C:
			report(queueControlResult{Outcome: "unknown", Reason: "worker did not confirm before timeout"})
			return
		}
	}
}
