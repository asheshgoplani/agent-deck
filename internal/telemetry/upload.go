package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	lastPayloadMaxSize = 64 << 10
)

var retryBackoff = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// uploadDeadline bounds a whole upload (every request of it), and so how
// long `telemetry off` can wait for the state lock an upload holds.
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

// MaybeUpload is the one outbound path besides SendUninstall. It sends the
// completed hours and days waiting in the spool, at most every 6 hours,
// never on the consent day, and holds the state lock for the whole send
// (at most uploadDeadline) so `telemetry off` either waits for it or
// prevents it.
func MaybeUpload(ctx context.Context) UploadResult {
	if reason := uploadGate(); reason != "" {
		return UploadResult{Reason: reason}
	}
	unlock, err := lockState()
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, uploadDeadline)
	defer cancel()
	s := LoadState()
	if ok, reason := Enabled(s); !ok {
		return UploadResult{Reason: string(reason)}
	}
	now := nowFn()
	today := dayOf(now)
	switch {
	case s.ConsentDay >= today:
		return UploadResult{Reason: "nothing is sent on the consent day"}
	case now.Before(s.Upload.NextTry):
		return UploadResult{Reason: "next upload at " + s.Upload.NextTry.Format(time.RFC3339)}
	}
	if s.Upload.AttemptsDay != today {
		s.Upload.AttemptsDay, s.Upload.AttemptsToday = today, 0
	}
	if s.Upload.AttemptsToday >= maxAttemptsPerDay {
		return UploadResult{Reason: "attempt budget for today used"}
	}

	lines, err := readSpool()
	if err != nil {
		return UploadResult{Reason: err.Error()}
	}
	lines = trimSpool(lines, now)
	s.dropExpiredDaily(now)
	if s.Upload.RejectedVersion != "" && s.Upload.RejectedVersion == safeVersion(processVersion) {
		return s.handleRejected(lines, now)
	}
	events := s.pending(lines, now)
	if len(events) == 0 {
		_ = writeSpool(lines)
		_ = saveStateLocked(s)
		return UploadResult{Reason: "nothing to send"}
	}
	bodies, groups := chunk(events)
	if len(bodies) > maxBatchRequests {
		bodies, groups = bodies[:maxBatchRequests], groups[:maxBatchRequests]
	}

	// Reserve the attempt durably before any request (v1 rule).
	s.Upload.AttemptsToday++
	if err := saveStateLocked(s); err != nil {
		return UploadResult{Reason: err.Error()}
	}

	res := UploadResult{Attempted: true}
	var failure postResult
	okGroups := 0
	for i, body := range bodies {
		if HardDisabled() {
			failure = postResult{err: errors.New("disabled during upload")}
			break
		}
		if n := realEvents(groups[i]); n > 0 {
			if LogMode() {
				failure.err = appendLog(body)
			} else {
				failure = post(ctx, body, sendTimeout)
			}
			if failure.err != nil {
				break
			}
			res.Events += n
			s.LastPayload = truncatedPayload(body)
		}
		okGroups++
	}
	// Remove what was acknowledged. A day's rollups go only when every one
	// of them was acknowledged; a partial day is rebuilt with the same uuids.
	sent := map[string]bool{}
	sentDays := map[string]bool{}
	unsentDays := map[string]bool{}
	chunked := 0
	for i, g := range groups {
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
	for _, p := range events[chunked:] {
		if p.rollupDay != "" {
			unsentDays[p.rollupDay] = true
		}
	}
	for d := range sentDays {
		if !unsentDays[d] {
			delete(s.Daily, d)
		}
	}
	kept := lines[:0:0]
	for _, l := range lines {
		if !sent[l.U] {
			kept = append(kept, l)
		}
	}
	if err := writeSpool(kept); err != nil {
		res.Reason = err.Error()
	}
	s.recordOutcome(failure, now, res.Events)
	res.Sent = failure.err == nil
	if !res.Sent && res.Reason == "" {
		res.Reason = failure.err.Error()
	}
	if err := saveStateLocked(s); err != nil && res.Reason == "" {
		res.Reason = "sent; could not persist acknowledgement"
	}
	return res
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
func appendLog(body []byte) error {
	path, err := siblingPath(logFileName)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
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
	bodies, _ := chunk(s.pending(trimSpool(lines, now), now))
	return bodies, nil
}

// SendUninstall sends the uninstall event synchronously (2 s timeout). It is
// the only send outside MaybeUpload and has the same gates, except that it
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
	l := s.newLine("uninstall", props, now, EffectiveLevel(s))
	// uninstall is always a CLI event
	l.SF = string(SurfaceCLI)
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
