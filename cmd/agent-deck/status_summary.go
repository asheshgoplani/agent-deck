package main

import (
	"fmt"

	"github.com/asheshgoplani/agent-deck/internal/session"
)

// summaryBucket is session.SummaryBucket, the bucket rule every status
// summary shares.
func summaryBucket(status session.Status) session.Status {
	return session.SummaryBucket(status)
}

// compactStatusLine is the one-line `status` summary: waiting, running and
// idle always, then stopped and error when not zero, so a profile of errored
// sessions no longer reads "0 waiting • 0 running • 0 idle".
func compactStatusLine(waiting, running, idle, stopped, errored int) string {
	line := fmt.Sprintf("%d waiting • %d running • %d idle", waiting, running, idle)
	if stopped > 0 {
		line += fmt.Sprintf(" • %d stopped", stopped)
	}
	if errored > 0 {
		line += fmt.Sprintf(" • %d error", errored)
	}
	return line
}
