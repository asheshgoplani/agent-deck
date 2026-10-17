package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"syscall"
	"time"
)

// Upload cadence and retry policy (TELEMETRY.md "When data is sent").
const (
	uploadInterval     = 6 * time.Hour
	maxAttemptsPerDay  = 4
	maxRetryAfter      = 6 * time.Hour
	rejectedDropDays   = 3
	uninstallTimeout   = 2 * time.Second
	logFileName        = "telemetry-log.ndjson"
	maxLogBytes        = 4 << 20
	lastPayloadMaxSize = 64 << 10
)

var retryBackoff = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// uploadDeadline bounds the network requests of an upload, counted from when
// the send lock is taken. It does not bound the blocking state lock waits that
// prepare the batch and record the result, so `telemetry off` can wait for
// the send lock longer than this while the state lock is busy.
var uploadDeadline = 8 * time.Second

// UploadResult describes what MaybeUpload did, for tests and `status`.
type UploadResult struct {
	Attempted bool
	Sent      bool
	Events    int
	Reason    string
}

// Status strings stored in UploadState.LastResult.
const (
	resultOK        = "ok"
	resultRetry     = "retry"
	resultRejected  = "rejected"
	resultLogged    = "logged"
	errKindNetwork  = "network"
	errKindRejected = "rejected_4xx"
	errKindServer   = "server"
)

// uploadGate returns why this process must not upload, or "".
func uploadGate() string {
	if r := HardDisableReason(); r != ReasonNone {
		return string(r)
	}
	if !Interactive() {
		return "non-interactive context"
	}
	if surface != SurfaceTUI {
		return "only the interactive TUI uploads"
	}
	if LogMode() {
		return ""
	}
	return uploadDestinationGate()
}

// uploadDestinationGate is also used by read-only tick preview, which runs
// from the CLI but describes what an eligible human TUI would send.
func uploadDestinationGate() string {
	if !Configured() {
		return "not configured (no PostHog project key)"
	}
	if err := ValidateEndpoint(endpoint); err != nil {
		return err.Error()
	}
	if endpointUndeployed() {
		return "receiver is not deployed"
	}
	if safeVersion(processVersion) == "dev" {
		return "dev builds never send"
	}
	return ""
}

// MaybeUpload sends detailed telemetry; install.tick has its own ledger. It sends the
// completed hours and days waiting in the spool, at most every 6 hours,
// never on the consent day. It holds the send lock for the whole send
// (requests bounded by uploadDeadline) so `telemetry off` either waits for it
// or prevents it, and the state lock only to prepare the batch and to record
// the result, so events recorded meanwhile are kept.
func MaybeUpload(ctx context.Context) UploadResult {
	if reason := uploadGate(); reason != "" {
		return UploadResult{Reason: reason}
	}
	unlockSend, err := lockSend(syscall.LOCK_EX)
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	defer unlockSend()
	ctx, cancel := context.WithTimeout(ctx, uploadDeadline)
	defer cancel()
	b, res := prepareUpload()
	if b == nil {
		return res
	}

	res = UploadResult{Attempted: true}
	var failure postResult
	var lastPayload json.RawMessage
	payloadSet := false
	okGroups := 0
	for i, body := range b.bodies {
		if HardDisabled() {
			failure = postResult{err: errors.New("disabled during upload")}
			break
		}
		if n := realEvents(b.groups[i]); n > 0 {
			if LogMode() {
				failure.err = appendLog(body)
			} else {
				failure = post(ctx, body, sendTimeout)
			}
			if failure.err != nil {
				break
			}
			res.Events += n
			lastPayload, payloadSet = truncatedPayload(body), true
		}
		okGroups++
	}
	res.Sent = failure.err == nil
	if !res.Sent {
		res.Reason = failure.err.Error()
	}

	unlock, err := lockState()
	if err != nil {
		if res.Reason == "" {
			res.Reason = "sent; could not persist acknowledgement"
		}
		return res
	}
	defer unlock()
	s := LoadState()
	if ok, _ := Enabled(s); !ok || s.InstallID != b.installID {
		if res.Reason == "" {
			res.Reason = "consent or install id changed during upload; result discarded"
		}
		return res
	}
	// Remove what was acknowledged. A day's rollups go only when every one
	// of them was acknowledged; a partial day is rebuilt with the same uuids.
	sent := map[string]bool{}
	sentDays := map[string]bool{}
	unsentDays := map[string]bool{}
	chunked := 0
	for i, g := range b.groups {
		chunked += len(g)
		for _, p := range g {
			switch {
			case p.spoolUUID != "" && i < okGroups:
				sent[p.spoolUUID] = true
			case p.rollupDay != "" && i < okGroups:
				sentDays[p.rollupDay] = true
			case p.rollupDay != "":
				unsentDays[p.rollupDay] = true
			}
		}
	}
	for _, p := range b.events[chunked:] {
		if p.rollupDay != "" {
			unsentDays[p.rollupDay] = true
		}
	}
	for d := range sentDays {
		if !unsentDays[d] {
			delete(s.Daily, d)
		}
	}
	// Re-read the spool: lines appended during the send must survive. Lines
	// the current level does not record are deleted unsent, as when the batch
	// was built.
	lines, err := readSpool()
	if err == nil {
		kept := lines[:0:0]
		for _, l := range atLevel(trimSpool(lines, b.now), EffectiveLevel(s)) {
			if !sent[l.U] {
				kept = append(kept, l)
			}
		}
		err = writeSpool(kept)
	}
	if err != nil && res.Reason == "" {
		res.Reason = err.Error()
	}
	if payloadSet {
		s.LastPayload = lastPayload
	}
	s.recordOutcome(failure, b.now, res.Events)
	if err := saveStateLocked(s); err != nil && res.Reason == "" {
		res.Reason = "sent; could not persist acknowledgement"
	}
	return res
}

// uploadBatch is what one upload sends, prepared under the state lock.
type uploadBatch struct {
	installID string
	now       time.Time
	events    []pendingEvent
	bodies    [][]byte
	groups    [][]pendingEvent
}

// prepareUpload checks the schedule, builds the pending batch and durably
// reserves the attempt, under the state lock. A nil batch means nothing is
// sent, for the returned reason.
func prepareUpload() (*uploadBatch, UploadResult) {
	unlock, err := lockState()
	if err != nil {
		return nil, UploadResult{Reason: err.Error()}
	}
	defer unlock()
	s := LoadState()
	if ok, reason := Enabled(s); !ok {
		return nil, UploadResult{Reason: string(reason)}
	}
	now := nowFn()
	today := dayOf(now)
	switch {
	case s.ConsentDay >= today:
		return nil, UploadResult{Reason: "nothing is sent on the consent day"}
	case now.Before(s.Upload.NextTry):
		return nil, UploadResult{Reason: "next upload at " + s.Upload.NextTry.Format(time.RFC3339)}
	}
	if s.Upload.AttemptsDay != today {
		s.Upload.AttemptsDay, s.Upload.AttemptsToday = today, 0
	}
	if s.Upload.AttemptsToday >= maxAttemptsPerDay {
		return nil, UploadResult{Reason: "attempt budget for today used"}
	}

	lines, err := readSpool()
	if err != nil {
		return nil, UploadResult{Reason: err.Error()}
	}
	// A finished open hour joins the spool before its day's rollup is built.
	// It is emitted only after the read, so every return below saves the
	// state that records it as emitted.
	s.emitOpenHour(now, func(l spoolLine) error {
		if err := appendSpool(l); err != nil {
			return err
		}
		lines = append(lines, l)
		return nil
	})
	lines = atLevel(trimSpool(lines, now), EffectiveLevel(s))
	s.dropExpiredDaily(now)
	if s.Upload.RejectedVersion != "" && s.Upload.RejectedVersion == safeVersion(processVersion) {
		return nil, s.handleRejected(lines, now)
	}
	events := s.pending(lines, now)
	if len(events) == 0 {
		_ = writeSpool(lines)
		_ = saveStateLocked(s)
		return nil, UploadResult{Reason: "nothing to send"}
	}
	bodies, groups := chunk(events)
	if len(bodies) > maxBatchRequests {
		bodies, groups = bodies[:maxBatchRequests], groups[:maxBatchRequests]
	}

	// Reserve the attempt durably before any request (v1 rule). The lease
	// closes the NextTry gate that 1.16.26 and older check under the state
	// lock, so an older binary running side by side does not send this
	// spool again while the request is in flight; the outcome replaces it.
	s.Upload.AttemptsToday++
	s.Upload.NextTry = now.Add(uploadDeadline + time.Minute)
	if err := saveStateLocked(s); err != nil {
		return nil, UploadResult{Reason: err.Error()}
	}
	return &uploadBatch{installID: s.InstallID, now: now, events: events, bodies: bodies, groups: groups}, UploadResult{}
}

func realEvents(group []pendingEvent) int {
	n := 0
	for _, p := range group {
		if p.ev.Event != "" {
			n++
		}
	}
	return n
}

func truncatedPayload(body []byte) json.RawMessage {
	if len(body) > lastPayloadMaxSize {
		return nil
	}
	return json.RawMessage(append([]byte(nil), body...))
}

// recordOutcome schedules the next attempt from the result of this one.
func (s *State) recordOutcome(r postResult, now time.Time, events int) {
	u := &s.Upload
	u.LastAt = now
	u.LastEvents = events
	switch {
	case r.err == nil:
		u.LastResult, u.LastErrorKind = resultOK, ""
		if LogMode() {
			u.LastResult = resultLogged
		}
		u.NextTry = now.Add(uploadInterval)
		u.RejectedVersion, u.RejectedSinceDay = "", ""
		s.LastSentDay = dayOf(now)
	case r.status == 0 || r.status == 408 || r.status == 429 || r.status >= 500:
		u.LastResult = resultRetry
		u.LastErrorKind = errKindNetwork
		if r.status != 0 {
			u.LastErrorKind = errKindServer
		}
		next := nextLocalDay(now)
		if i := u.AttemptsToday - 1; i >= 0 && i < len(retryBackoff) {
			next = now.Add(retryBackoff[i])
		}
		if r.retryAfter > 0 {
			next = now.Add(min(r.retryAfter, maxRetryAfter))
		}
		u.NextTry = next
	default:
		// A schema or key rejection must not loop: stop until the next
		// agent-deck version, and drop the rejected data after 3 days.
		u.LastResult, u.LastErrorKind = resultRejected, errKindRejected
		u.RejectedVersion = safeVersion(processVersion)
		if u.RejectedSinceDay == "" {
			u.RejectedSinceDay = dayOf(now)
		}
		u.NextTry = nextLocalDay(now)
	}
}

// handleRejected drops completed data once a rejection is 3 days old.
func (s *State) handleRejected(lines []spoolLine, now time.Time) UploadResult {
	since, err := time.ParseInLocation(DayFormat, s.Upload.RejectedSinceDay, time.Local)
	if err != nil || now.Sub(since) < rejectedDropDays*24*time.Hour {
		_ = saveStateLocked(s)
		return UploadResult{Reason: "rejected by the endpoint; waiting for a new agent-deck version"}
	}
	today := dayOf(now)
	kept := lines[:0:0]
	for _, l := range lines {
		if l.D >= today {
			kept = append(kept, l)
		}
	}
	for d := range s.Daily {
		if d < today {
			delete(s.Daily, d)
		}
	}
	_ = writeSpool(kept)
	s.Upload.RejectedSinceDay = today
	_ = saveStateLocked(s)
	return UploadResult{Reason: "rejected data older than 3 days dropped"}
}

func (s *State) dropExpiredDaily(now time.Time) {
	cutoff := dayOf(now.AddDate(0, 0, -spoolExpiryDays))
	for d := range s.Daily {
		if d < cutoff {
			delete(s.Daily, d)
		}
	}
}

func nextLocalDay(now time.Time) time.Time {
	l := now.Local()
	return time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, time.Local)
}

// appendLog writes a would-be upload body to telemetry-log.ndjson (log mode).
// The file starts over once it would pass 4 MiB.
func appendLog(body []byte) error {
	path, err := siblingPath(logFileName)
	if err != nil {
		return err
	}
	flags := os.O_CREATE | os.O_APPEND | os.O_WRONLY
	if fi, err := os.Stat(path); err == nil && fi.Size()+int64(len(body))+1 > maxLogBytes {
		flags |= os.O_TRUNC
	}
	f, err := os.OpenFile(path, flags, 0600)
	if err != nil {
		return err
	}
	_, werr := f.Write(append(append([]byte(nil), body...), '\n'))
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr
}

// logWouldRecord writes an event that would be recorded with consent, marked
// "recorded":false (log mode without consent): to stderr for the CLI, to the
// log file for the TUI (stderr would corrupt the screen).
func logWouldRecord(name string, props map[string]any, at time.Time) {
	if Validate(name, props) != nil {
		return
	}
	line, err := json.Marshal(map[string]any{"recorded": false, "e": name, "d": dayOf(at), "p": props})
	if err != nil {
		return
	}
	if surface == SurfaceCLI {
		_, _ = os.Stderr.Write(append(line, '\n'))
		return
	}
	_ = appendLog(line)
}

// PreviewBatch returns the exact request bodies the next upload would send
// now, with the project key redacted, without sending, changing state or
// creating an id.
func PreviewBatch() ([][]byte, error) {
	s := LoadState()
	if s.Consent != ConsentGranted || s.InstallID == "" {
		return nil, nil
	}
	lines, err := readSpool()
	if err != nil {
		return nil, err
	}
	now := nowFn()
	s.emitOpenHour(now, func(l spoolLine) error { lines = append(lines, l); return nil })
	bodies, _ := chunk(s.pending(atLevel(trimSpool(lines, now), EffectiveLevel(s)), now))
	// Preview never reserves a nonce. Show a pending tick only if the TUI
	// could send it today; basic level does not suppress the daily tick.
	if ok, _ := Enabled(s); ok && !tickOwner() && !LogMode() && uploadDestinationGate() == "" && s.ConsentDay < dayOf(now) {
		tick, err := readInstallTick()
		if err != nil {
			return nil, err
		}
		if tick.Day == dayOf(now) && !tick.Sent {
			body, err := tickBody(tick)
			if err != nil {
				return nil, err
			}
			bodies = append([][]byte{body}, bodies...)
		}
	}
	return bodies, nil
}

// SendUninstall sends the uninstall event synchronously (2 s timeout). It is
// an immediate send with the same gates, except that it
// may run from the CLI.
func SendUninstall(ctx context.Context, sessions int, lastTool, reason string) UploadResult {
	if HardDisabled() || !Interactive() {
		return UploadResult{Reason: "disabled or non-interactive"}
	}
	s := LoadState()
	if ok, r := Enabled(s); !ok {
		return UploadResult{Reason: string(r)}
	}
	now := nowFn()
	props := map[string]any{
		"sessions_total": CountBucket(sessions), "last_tool": NormalizeTool(lastTool), "reason": oneOf(reason, uninstallWhy),
	}
	if Validate("uninstall", props) != nil {
		return UploadResult{Reason: "invalid event"}
	}
	// uninstall is always a CLI event
	l := s.newLine("uninstall", props, now, EffectiveLevel(s), SurfaceCLI)
	body, err := json.Marshal(phBatch{APIKey: redactedAPIKey, Batch: []phEvent{s.toPostHog(l)}}) //nolint:gosec // G117: only the redacted placeholder; post() inserts the key
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	if LogMode() {
		_, _ = os.Stderr.Write(append(body, '\n'))
		return UploadResult{Reason: "log mode"}
	}
	if !Configured() || endpointUndeployed() || safeVersion(processVersion) == "dev" || ValidateEndpoint(endpoint) != nil {
		return UploadResult{Reason: "not configured"}
	}
	r := post(ctx, body, uninstallTimeout)
	if r.err != nil {
		return UploadResult{Attempted: true, Reason: r.err.Error()}
	}
	return UploadResult{Attempted: true, Sent: true, Events: 1}
}
