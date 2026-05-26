package session

import "strings"

// DoneSentinelMarker is the token a worker prints to assert that it has
// finished its assigned task. Issue #1186: the conductor previously had no
// trustworthy "worker finished" signal because Claude's Stop hook fires at
// the end of every turn and is mapped to the generic "waiting" status.
// Completion is now asserted by the only party that knows — the worker —
// by ending its final turn with a line of the form:
//
//	===AGENTDECK_DONE=== status=<ok|fail> summary=<text to end of line>
const DoneSentinelMarker = "===AGENTDECK_DONE==="

// DoneSignal is the parsed payload of a completion sentinel.
type DoneSignal struct {
	Status  string // "ok" or "fail"
	Summary string // free text to end of line; may be empty
}

// ParseDoneSentinel parses a single line. It returns the signal and true only
// when the line contains the marker followed by a valid status=<ok|fail>.
// A summary= field is optional; its value runs to the end of the line.
// Anything that doesn't match — no marker, missing status, an unrecognized
// status value — is rejected (ok=false) rather than guessed at.
func ParseDoneSentinel(line string) (DoneSignal, bool) {
	_, afterMarker, hasMarker := strings.Cut(line, DoneSentinelMarker)
	if !hasMarker {
		return DoneSignal{}, false
	}
	rest := strings.TrimSpace(afterMarker)

	_, afterStatus, hasStatus := strings.Cut(rest, "status=")
	if !hasStatus {
		return DoneSignal{}, false
	}

	// status value is the first whitespace-delimited token after status=.
	status := afterStatus
	if cut := strings.IndexAny(status, " \t"); cut >= 0 {
		status = status[:cut]
	}
	status = strings.ToLower(strings.TrimSpace(status))
	if status != "ok" && status != "fail" {
		return DoneSignal{}, false
	}

	summary := ""
	if _, after, hasSummary := strings.Cut(rest, "summary="); hasSummary {
		summary = strings.TrimSpace(after)
	}

	return DoneSignal{Status: status, Summary: summary}, true
}

// ScanDoneSentinel scans multi-line text and returns the LAST line that parses
// as a valid sentinel. Last-wins so a worker that retried (printing fail then
// ok) reports its final outcome.
func ScanDoneSentinel(text string) (DoneSignal, bool) {
	var found DoneSignal
	var ok bool
	for line := range strings.SplitSeq(text, "\n") {
		if sig, valid := ParseDoneSentinel(line); valid {
			found = sig
			ok = true
		}
	}
	return found, ok
}
