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
		dst := filepath.Join(dir, fmt.Sprintf("%d-%d-%s", now.UnixMilli(), i, filepath.Base(src)))
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
	events.PublishProfile(profile, "session.send", r.SessionID, map[string]string{"send_id": r.SendID, "state": r.State, "reason": r.Reason})
}

// queueSend records the send and hands it to the target's worker. It never
// types anything itself; it returns at once.
func queueSend(profile string, storage *session.Storage, inst *session.Instance, message string, images []string, out *CLIOutput) {
	now := time.Now()
	dir := sendQueueDir(storage)
	status := "stopped"
	if inst.Exists() {
		status, _ = fetchHookDrivenStatus(profile, inst.ID)
	}
	rec := &sendqueue.Record{
		SendID: sendqueue.NewID(now), State: sendqueue.StateQueued, TargetStatus: status,
		SessionID: inst.ID, SessionTitle: inst.Title, Tool: inst.Tool, Message: message, Images: images,
		CreatedAt: now.UTC().Format(time.RFC3339Nano), UpdatedAt: now.UTC().Format(time.RFC3339Nano),
		Deadline: now.Add(sendqueue.DefaultRetryBudget).UTC().Format(time.RFC3339Nano),
	}
	if !inst.Exists() {
		rec.State, rec.Reason = sendqueue.StateFailed, "target not running"
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

// handleSessionSendStatus implements `agent-deck session send-status <send-id> --json`.
func handleSessionSendStatus(profile string, args []string) {
	fs := flag.NewFlagSet("session send-status", flag.ContinueOnError)
	fs.SetOutput(os.Stdout)
	jsonOutput := fs.Bool("json", false, "Output as JSON")
	fs.Usage = func() {
		fmt.Println("Usage: agent-deck session send-status <send-id> [--json]")
		fmt.Println()
		fmt.Println("State of a `session send --queue` message: queued, typed, submitted, landed or failed,")
		fmt.Println("with reason, target_status, attempts, and landed_row_id/landed_at once the text is")
		fmt.Println("in the transcript (the row id recall timeline/follow use). Exit 0 when found, 2 for an unknown id.")
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

// sendWorkerTiming is tunable by tests through the environment.
func sendWorkerPoll() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTDECK_SEND_WORKER_POLL")); err == nil && d > 0 {
		return d
	}
	return time.Second
}

// sendLandWindow is how long a delivered send is watched for in the
// transcript before it is settled without a landed row.
func sendLandWindow() time.Duration {
	if d, err := time.ParseDuration(os.Getenv("AGENTDECK_SEND_LAND_WINDOW")); err == nil && d > 0 {
		return d
	}
	return 2 * time.Minute
}

// handleSessionSendWorker is the hidden detached worker: it owns delivery
// for one target, walks its queued records oldest first, and exits when
// none are left.
func handleSessionSendWorker(profile string, args []string) {
	fs := flag.NewFlagSet("session send-worker", flag.ContinueOnError)
	target := fs.String("target", "", "deck session id")
	if err := fs.Parse(args); err != nil || *target == "" {
		os.Exit(2)
	}
	storage, err := session.NewStorageWithProfile(profile)
	if err != nil {
		os.Exit(1)
	}
	dir := sendQueueDir(storage)
	storage.Close()
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
			deliverQueued(profile, dir, rec)
		}
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
		if !r.Final() {
			return r
		}
	}
	return nil
}

// deliverQueued drives one record to landed or failed.
func deliverQueued(profile, dir string, rec *sendqueue.Record) {
	poll := sendWorkerPoll()
	deadline, _ := time.Parse(time.RFC3339Nano, rec.Deadline)
	set := func(fn func(*sendqueue.Record)) {
		if r, err := sendqueue.Update(dir, rec.SendID, time.Now(), fn); err == nil {
			if r.State != rec.State || r.Reason != rec.Reason {
				publishSendState(profile, r)
			}
			*rec = *r
		}
	}
	fail := func(reason string) {
		set(func(r *sendqueue.Record) { r.State, r.Reason = sendqueue.StateFailed, reason })
	}
	for rec.State == sendqueue.StateQueued {
		_, instances, _, err := loadSessionData(profile)
		if err != nil {
			fail("cannot load sessions: " + err.Error())
			return
		}
		var inst *session.Instance
		for _, i := range instances {
			if i.ID == rec.SessionID {
				inst = i
			}
		}
		if inst == nil {
			fail("target removed")
			return
		}
		if !inst.Exists() {
			fail("target not running")
			return
		}
		status, _ := fetchHookDrivenStatus(profile, inst.ID)
		if status == "running" || status == "starting" {
			if !deadline.IsZero() && time.Now().After(deadline) {
				fail("target stayed busy past the retry budget")
				return
			}
			set(func(r *sendqueue.Record) { r.TargetStatus = status })
			time.Sleep(poll)
			continue
		}
		path := session.LiveTranscriptPath(inst)
		var from int64
		if info, err := os.Stat(path); err == nil {
			from = info.Size()
		}
		sentAt := time.Now()
		set(func(r *sendqueue.Record) {
			r.Attempts++
			r.TargetStatus, r.TranscriptPath, r.TranscriptFrom = status, path, from
			r.SentAt = sentAt.UTC().Format(time.RFC3339Nano)
		})
		result, code := runChildSend(profile, inst.ID, rec.Message)
		delivery, _ := result["delivery"].(string)
		switch {
		case code == 0:
			state := sendqueue.StateTyped
			if submitted, _ := result["submitted"].(bool); submitted || result["confirmation"] == "confirmed" {
				state = sendqueue.StateSubmitted
			}
			set(func(r *sendqueue.Record) { r.State, r.Reason = state, "" })
		case delivery == deliveryTargetBusy || delivery == deliveryComposerBlocked:
			// Nothing was typed: safe to try again once the target settles.
			if !deadline.IsZero() && time.Now().After(deadline) {
				fail("not delivered before the retry budget ran out: " + delivery)
				return
			}
			set(func(r *sendqueue.Record) { r.Reason = "retrying: " + delivery })
			time.Sleep(poll)
		default:
			reason, _ := result["error"].(string)
			if reason == "" {
				reason = fmt.Sprintf("session send exited %d", code)
			}
			if delivery != "" {
				reason = delivery + ": " + reason
			}
			fail(reason)
			return
		}
	}
	// Typed or submitted: wait for the text to land in the transcript, then
	// for the target to take the turn up, so the next queued message is
	// not typed into this one's turn.
	harness := rowsHarness(rec.Tool)
	landBy := time.Now().Add(sendLandWindow())
	for time.Now().Before(landBy) {
		if rec.TranscriptPath == "" {
			if p := liveTranscriptForID(profile, rec.SessionID); p != "" {
				set(func(r *sendqueue.Record) { r.TranscriptPath = p })
			}
		}
		if rec.TranscriptPath != "" {
			if id, ts, ok := query.FindLanded(context.Background(), harness, rec.TranscriptPath, rec.TranscriptFrom, rec.Message); ok {
				set(func(r *sendqueue.Record) {
					r.State, r.Reason, r.LandedRowID, r.LandedAt = sendqueue.StateLanded, "", id, ts
				})
				waitTurnStarted(profile, rec.SessionID, 5*time.Second)
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	// Never claim more than we saw: it stays typed/submitted, with a reason,
	// and is settled so it is never typed a second time.
	set(func(r *sendqueue.Record) {
		r.Reason, r.Settled = "not seen in the transcript within "+sendLandWindow().String(), true
	})
}

func liveTranscriptForID(profile, id string) string {
	_, instances, _, err := loadSessionData(profile)
	if err != nil {
		return ""
	}
	for _, i := range instances {
		if i.ID == id {
			return session.LiveTranscriptPath(i)
		}
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

// runChildSend delivers through the normal `session send` path (readiness
// wait, composer guard, submit verification) in a child process.
func runChildSend(profile, id, message string) (map[string]interface{}, int) {
	exe, err := os.Executable()
	if err != nil {
		return map[string]interface{}{"error": err.Error()}, 1
	}
	cmd := exec.Command(exe, profileArgs(profile, "session", "send", id, "--message-file", "-", "--json")...)
	cmd.Stdin = strings.NewReader(message)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	err = cmd.Run()
	code := 0
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code = exitErr.ExitCode()
	} else if err != nil {
		return map[string]interface{}{"error": err.Error()}, 1
	}
	result := map[string]interface{}{}
	_ = json.Unmarshal(stdout.Bytes(), &result)
	return result, code
}

// deliveryFrames turns queued sends of one session into follow delivery
// frames: every state change after the first scan, plus the current state
// of sends still in flight at start.
func deliveryFrames(storage *session.Storage, sessionID string) func() []query.RowFrame {
	if storage == nil || sessionID == "" {
		return nil
	}
	dir := sendQueueDir(storage)
	seen := map[string]string{}
	first := true
	return func() []query.RowFrame {
		recs, _ := sendqueue.List(dir, sessionID)
		var out []query.RowFrame
		for _, r := range recs {
			prev, known := seen[r.SendID]
			seen[r.SendID] = r.State
			if (first && !r.Final()) || (!first && (!known || prev != r.State)) {
				out = append(out, query.RowFrame{Frame: "delivery", Reason: r.Reason, Delivery: &query.Delivery{SendID: r.SendID, State: r.State}})
			}
		}
		first = false
		return out
	}
}
