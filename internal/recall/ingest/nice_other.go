//go:build !linux

package ingest

// niceCurrentGoroutine is a no-op outside Linux: docs/recall.md scopes the
// background backfill's priority lowering to Linux (the platform the
// notify-daemon unit normally runs on); darwin has no equivalent
// setpriority(PRIO_PROCESS, tid, .) that targets one goroutine's thread
// rather than the whole process.
func niceCurrentGoroutine(delta int) {}
