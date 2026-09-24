package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/asheshgoplani/agent-deck/internal/events"
	"github.com/asheshgoplani/agent-deck/internal/recall/query"
	"github.com/asheshgoplani/agent-deck/internal/sendqueue"
	"github.com/asheshgoplani/agent-deck/internal/session"
)

// imageList is the repeatable --image flag.
type imageList []string

func (l *imageList) String() string     { return strings.Join(*l, ",") }
func (l *imageList) Set(v string) error { *l = append(*l, v); return nil }

var imageExtensions = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".webp": true}

// errImagesUnsupported marks a harness that cannot take an image in a
// running session; the CLI exits 2 for it.
var errImagesUnsupported = errors.New("images not supported")

// attachImages copies images next to the session and returns the message
// with the harness's image reference appended. Claude Code and Gemini CLI
// read `@path` from the composer. Codex takes images only at launch (-i), so
// a running Codex session refuses them, as does any other harness.
func attachImages(inst *session.Instance, message string, images []string, now time.Time) (string, []string, error) {
	if len(images) == 0 {
		return message, nil, nil
	}
	switch {
	case session.IsClaudeCompatible(inst.Tool), inst.Tool == "gemini":
	case session.IsCodexCompatible(inst.Tool):
		return "", nil, fmt.Errorf("%w for codex in a running session (Codex accepts images only at launch with -i)", errImagesUnsupported)
	default:
		return "", nil, fmt.Errorf("%w for %s", errImagesUnsupported, inst.Tool)
	}
	dir := filepath.Join(inst.EffectiveWorkingDir(), ".agentdeck-images")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", nil, fmt.Errorf("image dir: %w", err)
	}
	// Keep the copies out of the user's git status.
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); os.IsNotExist(err) {
		_ = os.WriteFile(ignore, []byte("*\n"), 0o644)
	}
	var saved []string
	refs := []string{strings.TrimSpace(message)}
	for i, src := range images {
		if !imageExtensions[strings.ToLower(filepath.Ext(src))] {
			return "", nil, fmt.Errorf("%s: not an image (png, jpg, jpeg, gif, webp)", src)
		}
		in, err := os.Open(src)
		if err != nil {
			return "", nil, err
		}
		info, err := in.Stat()
		if err != nil || !info.Mode().IsRegular() {
			in.Close()
			return "", nil, fmt.Errorf("%s: not a regular file", src)
		}
		// The composer reads @path up to the first space: the copy's name
		// never has one.
		name := strings.Join(strings.Fields(filepath.Base(src)), "-")
		dst := filepath.Join(dir, fmt.Sprintf("%d-%d-%s", now.UnixMilli(), i, name))
		out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err == nil {
			_, err = io.Copy(out, in)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		in.Close()
		if err != nil {
			return "", nil, fmt.Errorf("copy %s: %w", src, err)
		}
		saved = append(saved, dst)
		refs = append(refs, "@"+dst)
	}
	return strings.TrimSpace(strings.Join(refs, " ")), saved, nil
}

// profileArgs prefixes -p <profile> when a profile was chosen explicitly.
func profileArgs(profile string, args ...string) []string {
	if profile == "" {
		return args
	}
	return append([]string{"-p", profile}, args...)
}

func sendQueueDir(storage *session.Storage) string {
	return sendqueue.Dir(filepath.Dir(storage.Path()))
}

// publishSendState mirrors a queued send's state on the bus so a client
// following `events follow --kind session.send` never polls send-status.
func publishSendState(profile string, r *sendqueue.Record) {
	events.PublishProfile(profile, "session.send", r.SessionID, map[string]string{"send_id": r.SendID, "state": r.State, "verdict": r.Verdict, "reason": r.Reason})
}

// queueSend records the send and hands it to the target's worker. It never
// types anything itself; it returns at once.
func queueSend(profile string, storage *session.Storage, inst *session.Instance, message string, images []string, out *CLIOutput) {
	now := time.Now()
	dir := sendQueueDir(storage)
	status := "unknown"
	id, err := sendqueue.NextID(dir, now)
	if err != nil {
		out.Error(fmt.Sprintf("cannot queue send: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	rec := &sendqueue.Record{
		SendID: id, State: sendqueue.StateQueued, Verdict: "queued", TargetStatus: status,
		SessionID: inst.ID, SessionTitle: inst.Title, Tool: inst.Tool, Message: message, Images: images,
		CreatedAt: now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano),
		Deadline: now.Add(sendqueue.DefaultRetryBudget).UTC().Format(time.RFC3339Nano),
	}
	if !inst.Exists() {
		rec.State, rec.Reason, rec.Verdict = sendqueue.StateFailed, "target not running", "unknown"
	}
	if err := sendqueue.Save(dir, rec); err != nil {
		out.Error(fmt.Sprintf("cannot queue send: %v", err), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	publishSendState(profile, rec)
	if rec.State == sendqueue.StateFailed {
		out.ErrorWithData(fmt.Sprintf("send %s failed: %s", rec.SendID, rec.Reason), ErrCodeDeliveryFailed, recordFields(rec))
		os.Exit(1)
	}
	if err := spawnSendWorker(profile, inst.ID); err != nil {
		// The record stays queued; the next --queue or send-status for
		// this target starts a worker again.
		fmt.Fprintf(os.Stderr, "Warning: could not start the delivery worker yet: %v\n", err)
	}
	out.Success(fmt.Sprintf("Queued %s for '%s' (%s)", rec.SendID, inst.Title, status), recordFields(rec))
}

func recordFields(r *sendqueue.Record) map[string]interface{} {
	b, _ := json.Marshal(r)
	var m map[string]interface{}
	_ = json.Unmarshal(b, &m)
	return m
}

// spawnSendWorker starts a detached worker for the target. A second worker
// for the same target exits at once on the target lock.
func spawnSendWorker(profile, sessionID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, profileArgs(profile, "session", "send-worker", "--target", sessionID)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// kickPendingSendWorkers starts a worker for every target in the queue
// directory dir (or only sessionID) that still has an unfinished queued
// send, e.g. after a reboot killed the old workers. A live worker keeps its
// target lock, so the extra one exits at once.
func kickPendingSendWorkers(profile, dir, sessionID string) {
	for _, target := range sendqueue.PendingTargets(dir) {
		if sessionID == "" || target == sessionID {
			_ = spawnSendWorker(profile, target)
		}
	}
}

// handleSessionSendStatus implements `agent-deck session send-status <send-id> --json`.
func handleSessionSendStatus(profile string, args []string) {
	fs := flag.NewFlagSet("session send-status", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session send-status <send-id> [--json]")
		fmt.Println()
		fmt.Println("State of a `session send --queue` message: queued, typing, typed, submitted, landed or failed,")
		fmt.Println("with reason, target_status, attempts, and landed_row_id/landed_at once the text is")
		fmt.Println("in the transcript (the row id recall timeline/follow use). Exit 0 when found, 2 for an unknown id.")
		fmt.Println()
		fmt.Println("landed is reported only on transcript evidence (a user row, or a queued message absorbed")
		fmt.Println("into the turn). failed means nothing was typed, so resending is safe. A send whose outcome")
		fmt.Println("could not be proven settles as typed/submitted (settled: true) and is never typed again.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(normalizeArgs(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(2)
	}
	out := NewCLIOutput(*jsonOutput, false)
	if fs.NArg() != 1 {
		fs.Usage()
		os.Exit(2)
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		out.Error(err.Error(), ErrCodeInvalidOperation)
		os.Exit(1)
	}
	defer storage.Close()
	dir := sendQueueDir(storage)
	rec, err := sendqueue.Load(dir, fs.Arg(0))
	if err != nil {
		out.Error(fmt.Sprintf("send %s: %v", fs.Arg(0), err), ErrCodeNotFound)
		os.Exit(2)
	}
	if !rec.Final() {
		// A worker that died (reboot, kill) is restarted by any status read.
		_ = spawnSendWorker(profile, rec.SessionID)
	}
	out.Success(fmt.Sprintf("%s: %s %s", rec.SendID, rec.State, rec.Reason), recordFields(rec))
}

// envDuration reads a positive duration from the environment, so tests can
// tune the worker's timing; def otherwise.
func envDuration(name string, def time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(name)); err == nil && d > 0 {
		return d
	}
	return def
}

// sendWorkerPoll is how often the worker rechecks a busy target.
func sendWorkerPoll() time.Duration {
	return envDuration("AGENTDECK_SEND_WORKER_POLL", time.Second)
}

// sendLandWindow is how long a delivered send is watched for in the
// transcript before it is settled without a landed row.
func sendLandWindow() time.Duration {
	return envDuration("AGENTDECK_SEND_LAND_WINDOW", 2*time.Minute)
}

// handleSessionSendWorker is the hidden detached worker: it owns delivery
// for one target, walks its queued records oldest first, and exits when
// none are left.
func handleSessionSendWorker(profile string, args []string) {
	fs := flag.NewFlagSet("session send-worker", flag.ContinueOnError)
	target := fs.String("target", "", "deck session id")
	watch := fs.String("watch", "", "send id whose transcript is being watched")
	if err := fs.Parse(args); err != nil || (*target == "" && *watch == "") {
		os.Exit(2)
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		os.Exit(1)
	}
	dir := sendQueueDir(storage)
	storage.Close()
	if *watch != "" {
		watchQueuedSend(profile, dir, *watch)
		return
	}
	sendqueue.Prune(dir, time.Now().Add(-sendqueue.RetainFinished))
	for {
		lock, ok, err := sendqueue.TryLock(dir, *target)
		if err != nil || !ok {
			return // another worker owns this target
		}
		for {
			rec := nextPending(dir, *target)
			if rec == nil {
				break
			}
			deliverQueuedAsync(profile, dir, rec)
		}
		startPendingWatchers(profile, dir, *target)
		lock.Release()
		// A send queued while this worker was finishing: pick it up.
		if nextPending(dir, *target) == nil {
			return
		}
	}
}

func nextPending(dir, target string) *sendqueue.Record {
	recs, _ := sendqueue.List(dir, target)
	for _, r := range recs {
		if r.State == sendqueue.StateQueued || r.State == sendqueue.StateTyping {
			return r
		}
	}
	return nil
}

// Transcript confirmation is independent of typing. Watching one send must
// not block later messages from entering a busy Claude harness's own queue.
func startPendingWatchers(profile, dir, target string) {
	recs, _ := sendqueue.List(dir, target)
	for _, r := range recs {
		if !r.Final() && (r.State == sendqueue.StateTyped || r.State == sendqueue.StateSubmitted) {
			if err := spawnSendWatcher(profile, r.SendID); err != nil {
				watchQueuedSend(profile, dir, r.SendID)
			}
		}
	}
}

func spawnSendWatcher(profile, sendID string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := exec.Command(exe, profileArgs(profile, "session", "send-worker", "--watch", sendID)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

func watchQueuedSend(profile, dir, sendID string) {
	lock, ok, err := sendqueue.TryLock(dir, "watch-"+sendID)
	if err != nil || !ok {
		return
	}
	defer lock.Release()
	rec, err := sendqueue.Load(dir, sendID)
	if err != nil || rec.Final() || (rec.State != sendqueue.StateTyped && rec.State != sendqueue.StateSubmitted) {
		return
	}
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, sendID, time.Now(), fn)
		if err != nil {
			return err
		}
		if r.State != rec.State || r.Reason != rec.Reason || r.Verdict != rec.Verdict {
			publishSendState(profile, r)
		}
		*rec = *r
		return nil
	}
	watchLanded(profile, rec, set)
}

// childOutcome is what a `session send` child's result says happened.
type childOutcome int

const (
	childTyped     childOutcome = iota // exit 0: typed, submission not confirmed
	childSubmitted                     // exit 0 with confirmed submission
	childNotSent                       // refused before typing: safe to retry
	childUnknown                       // anything else: may have been typed
)

// notSentDeliveries are the `session send` outcomes that guarantee nothing
// was typed, so the worker may try again once the target settles.
var notSentDeliveries = map[string]bool{
	deliveryTargetBusy:        true,
	deliveryComposerBlocked:   true,
	deliveryAcceptanceRefused: true,
}

// classifyChild reads a `session send --json` result. Only a refusal that
// guarantees nothing was typed may be retried. Every other failure — an open
// menu, a readiness timeout, no_evidence, a crash — may have typed the text,
// so it is never reported failed (a client that resends on failed would
// double the message): it settles as typed and the transcript decides.
func classifyChild(result map[string]interface{}, code int) (childOutcome, string) {
	delivery, _ := result["delivery"].(string)
	success, _ := result["success"].(bool)
	if code == 0 && (success || len(result) == 0) {
		if submitted, _ := result["submitted"].(bool); submitted || result["confirmation"] == "confirmed" {
			return childSubmitted, ""
		}
		return childTyped, ""
	}
	if notSentDeliveries[delivery] {
		return childNotSent, delivery
	}
	reason, _ := result["error"].(string)
	if reason == "" {
		reason = fmt.Sprintf("session send exited %d", code)
	}
	if delivery != "" {
		reason = delivery + ": " + reason
	}
	return childUnknown, reason
}

// sendChild starts the child that types one queued message; tests replace it.
var sendChild = startChildSend

// deliverQueued drives one record to landed, failed (only when nothing was
// typed), or settled typed/submitted.
func deliverQueued(profile, dir string, rec *sendqueue.Record) {
	deliverQueuedMode(profile, dir, rec, true)
}

func deliverQueuedAsync(profile, dir string, rec *sendqueue.Record) {
	deliverQueuedMode(profile, dir, rec, false)
}

func deliverQueuedMode(profile, dir string, rec *sendqueue.Record, watch bool) {
	poll := sendWorkerPoll()
	deadline, _ := time.Parse(time.RFC3339Nano, rec.Deadline)
	set := func(fn func(*sendqueue.Record)) error {
		r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn)
		if err != nil {
			return err
		}
		if r.State != rec.State || r.Reason != rec.Reason || r.Verdict != rec.Verdict {
			publishSendState(profile, r)
		}
		*rec = *r
		return nil
	}
	fail := func(reason string) {
		_ = set(func(r *sendqueue.Record) { r.State, r.Reason, r.Verdict = sendqueue.StateFailed, reason, "unknown" })
	}
	pastDeadline := func() bool { return !deadline.IsZero() && time.Now().After(deadline) }
	if rec.State == sendqueue.StateTyping {
		// The previous worker died while its child was delivering.
		reconcileTyping(dir, rec, set)
	}
	for rec.State == sendqueue.StateQueued {
		_, instances, _, err := loadSessionData(profile)
		if err != nil {
			fail("cannot load sessions: " + err.Error())
			return
		}
		inst := instanceByID(instances, rec.SessionID)
		if inst == nil {
			fail("target removed")
			return
		}
		if !inst.Exists() {
			fail("target not running")
			return
		}
		status, _ := fetchHookDrivenStatus(profile, inst.ID)
		if shouldWaitForIdle(inst.Tool, status) {
			if pastDeadline() {
				fail("target stayed busy past the retry budget")
				return
			}
			_ = set(func(r *sendqueue.Record) { r.TargetStatus = status })
			time.Sleep(poll)
			continue
		}
		path := session.LiveTranscriptPath(inst, instances)
		var from int64
		if info, err := os.Stat(path); err == nil {
			from = info.Size()
		}
		if !typeQueued(profile, dir, rec, status, path, from, set) {
			fail("cannot record the send before typing it")
			return
		}
		if rec.State == sendqueue.StateQueued {
			// Refused before typing: safe to try again once the target settles.
			if pastDeadline() {
				fail("not delivered before the retry budget ran out: " + strings.TrimPrefix(rec.Reason, "retrying: "))
				return
			}
			time.Sleep(poll)
		}
	}
	if rec.Final() {
		return
	}
	if watch {
		watchLanded(profile, rec, set)
	}
}

func shouldWaitForIdle(tool, status string) bool {
	return status == "" || status == "unknown" || status == "starting" || (status == "running" && !session.AcceptsInputWhileBusy(tool))
}

// typeQueued hands one queued record to a `session send` child. The record
// is written as typing (attempts, sent_at, transcript offset) BEFORE the
// child starts, so a worker that dies at any point after this leaves a
// record no later worker types again. It returns false only when that
// write failed, in which case nothing was typed.
func typeQueued(profile, dir string, rec *sendqueue.Record, status, path string, from int64, set func(func(*sendqueue.Record)) error) bool {
	result := sendqueue.ResultPath(dir, rec.SendID)
	_ = os.Remove(result)
	if err := set(func(r *sendqueue.Record) {
		r.State, r.Reason, r.ChildPID = sendqueue.StateTyping, "", 0
		r.Attempts++
		r.TargetStatus, r.TranscriptPath, r.TranscriptFrom = status, path, from
		r.SentAt = time.Now().UTC().Format(time.RFC3339Nano)
	}); err != nil {
		return false
	}
	pid, wait, err := sendChild(profile, rec.SessionID, rec.Message, result)
	if err != nil {
		// The child never started, so nothing was typed.
		_ = set(func(r *sendqueue.Record) {
			r.State, r.Reason = sendqueue.StateQueued, "retrying: cannot start session send: "+err.Error()
		})
		return true
	}
	_ = set(func(r *sendqueue.Record) { r.ChildPID = pid })
	code := wait()
	applyChildResult(rec, readChildResult(result), code, true, set)
	return true
}

// applyChildResult moves a typing record on from its child's result.
func applyChildResult(rec *sendqueue.Record, result map[string]interface{}, code int, haveResult bool, set func(func(*sendqueue.Record)) error) {
	outcome, reason := childUnknown, "worker restarted mid-send and the child left no result"
	if haveResult {
		outcome, reason = classifyChild(result, code)
	}
	_ = set(func(r *sendqueue.Record) {
		r.ChildPID = 0
		switch outcome {
		case childSubmitted:
			r.State, r.Reason, r.Verdict = sendqueue.StateSubmitted, "", "delivered"
		case childTyped:
			r.State, r.Reason = sendqueue.StateTyped, ""
			if result["delivery"] == deliveryQueued || result["delivery"] == deliveryDelivered {
				r.Verdict = "delivered"
			} else {
				r.Verdict = "unknown"
			}
		case childNotSent:
			r.State, r.Reason, r.Verdict = sendqueue.StateQueued, "retrying: "+reason, "queued"
		default:
			r.State, r.Reason, r.Verdict = sendqueue.StateTyped, "outcome unknown ("+reason+"); not retyped, watching the transcript", "unknown"
		}
	})
}

// reconcileTyping settles a record a dead worker left in typing. The
// child may still be running (it outlives the worker): wait for it, then
// use its result file. Without a result the outcome is unknown and the
// record goes to the transcript watch; it is never typed again.
func reconcileTyping(dir string, rec *sendqueue.Record, set func(func(*sendqueue.Record)) error) {
	resultPath := sendqueue.ResultPath(dir, rec.SendID)
	end := time.Now().Add(sendChildWaitMax())
	for {
		if result, ok := parseChildResult(resultPath); ok {
			code := 1
			if success, _ := result["success"].(bool); success {
				code = 0
			}
			applyChildResult(rec, result, code, true, set)
			return
		}
		if !processAlive(rec.ChildPID) || time.Now().After(end) {
			applyChildResult(rec, nil, 0, false, set)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// watchLanded waits for a typed or submitted record's text to land in the
// transcript, then for the target to take the turn up, so the next queued
// message is not typed into this one's turn. Without evidence it settles
// typed/submitted with a reason: never landed, and never typed again.
func watchLanded(profile string, rec *sendqueue.Record, set func(func(*sendqueue.Record)) error) {
	harness := rowsHarness(rec.Tool)
	if !query.SupportsDirectRows(harness) {
		_ = set(func(r *sendqueue.Record) {
			r.Settled = true
			if r.Reason == "" {
				r.Reason = "no transcript to confirm landing for " + r.Tool
			}
		})
		return
	}
	sentAt, _ := time.Parse(time.RFC3339Nano, rec.SentAt)
	landBy := time.Now().Add(sendLandWindow())
	for time.Now().Before(landBy) {
		if rec.TranscriptPath == "" {
			if p := liveTranscriptForID(profile, rec.SessionID); p != "" {
				_ = set(func(r *sendqueue.Record) { r.TranscriptPath = p })
			}
		}
		if rec.TranscriptPath != "" {
			if id, ts, ok := query.FindLanded(context.Background(), harness, rec.TranscriptPath, rec.TranscriptFrom, rec.Message, sentAt); ok {
				_ = set(func(r *sendqueue.Record) {
					r.State, r.Reason, r.Verdict, r.LandedRowID, r.LandedAt = sendqueue.StateLanded, "", "delivered", id, ts
				})
				waitTurnStarted(profile, rec.SessionID, 5*time.Second)
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	_ = set(func(r *sendqueue.Record) {
		if r.Reason == "" {
			r.Reason = "not seen in the transcript within " + sendLandWindow().String()
		}
		r.Settled = true
	})
}

// sendChildWaitMax bounds how long a restarted worker waits for the child
// a dead worker left running.
func sendChildWaitMax() time.Duration {
	return envDuration("AGENTDECK_SEND_CHILD_WAIT", 15*time.Minute)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// parseChildResult reads a child's complete JSON result; a partial write
// does not parse.
func parseChildResult(path string) (map[string]interface{}, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	result := map[string]interface{}{}
	if json.Unmarshal(bytes.TrimSpace(b), &result) != nil {
		return nil, false
	}
	return result, true
}

func readChildResult(path string) map[string]interface{} {
	result, _ := parseChildResult(path)
	if result == nil {
		result = map[string]interface{}{}
	}
	return result
}

func instanceByID(instances []*session.Instance, id string) *session.Instance {
	for _, i := range instances {
		if i.ID == id {
			return i
		}
	}
	return nil
}

func liveTranscriptForID(profile, id string) string {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return ""
	}
	if inst := instanceByID(instances, id); inst != nil {
		return session.LiveTranscriptPath(inst, instances)
	}
	return ""
}

func waitTurnStarted(profile, id string, max time.Duration) {
	end := time.Now().Add(max)
	for time.Now().Before(end) {
		if s, _ := fetchHookDrivenStatus(profile, id); s == "running" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// startChildSend delivers through the normal `session send` path (readiness
// wait, composer guard, submit verification) in a child process. Its input
// and JSON result are files, not pipes: the child outlives a worker that
// dies, reads the whole message regardless, and the next worker reads the
// outcome from resultPath.
func startChildSend(profile, id, message, resultPath string) (int, func() int, error) {
	exe, err := os.Executable()
	if err != nil {
		return 0, nil, err
	}
	msgPath := strings.TrimSuffix(resultPath, ".result") + ".message"
	if err := os.WriteFile(msgPath, []byte(message), 0o600); err != nil {
		return 0, nil, err
	}
	out, err := os.OpenFile(resultPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, nil, err
	}
	cmd := exec.Command(exe, profileArgs(profile, "session", "send", id, "--message-file", msgPath, "--json", "--queue-worker")...)
	cmd.Stdout = out
	if err := cmd.Start(); err != nil {
		out.Close()
		return 0, nil, err
	}
	wait := func() int {
		err := cmd.Wait()
		out.Close()
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		if err != nil {
			return 1
		}
		return 0
	}
	return cmd.Process.Pid, wait, nil
}

// deliveryFrames turns queued sends of one session into follow delivery
// frames: every state change after the first scan, plus the current state
// of sends still in flight at start.
func deliveryFrames(profile string, storage *session.Storage, sessionID string) func() []query.RowFrame {
	if storage == nil || sessionID == "" {
		return nil
	}
	dir := sendQueueDir(storage)
	kickPendingSendWorkers(profile, dir, sessionID)
	seen := map[string]string{}
	first := true
	return func() []query.RowFrame {
		recs, _ := sendqueue.List(dir, sessionID)
		var out []query.RowFrame
		for _, r := range recs {
			prev, known := seen[r.SendID]
			seen[r.SendID] = r.State + ":" + r.Verdict
			if (first && !r.Final()) || (!first && (!known || prev != seen[r.SendID])) {
				out = append(out, query.RowFrame{Frame: "delivery", Reason: r.Reason, Delivery: &query.Delivery{SendID: r.SendID, State: r.State, Verdict: r.Verdict}})
			}
		}
		first = false
		return out
	}
}
