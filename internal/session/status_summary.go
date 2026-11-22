package session

// SummaryBucket maps a status onto the five buckets every status summary
// counts: running, waiting, idle, stopped, error. Starting, queued and any
// other value count as idle, the TUI header's rule (internal/ui
// statusBucket), so the buckets always add up to the total. `status`,
// `status --json`, `group list` (text and --json) and the web chips and group
// panel use it; archived sessions are left out of all of them.
func SummaryBucket(status Status) Status {
	switch status {
	case StatusRunning, StatusWaiting, StatusIdle, StatusStopped, StatusError:
		return status
	default:
		return StatusIdle
	}
}
