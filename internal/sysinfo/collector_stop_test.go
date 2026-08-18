package sysinfo

import "testing"

// The TUI shutdown path can run more than once (two queued quit messages), so a
// second Stop() must not panic on a double close of stopCh.
func TestCollectorStopIsIdempotent(t *testing.T) {
	c := NewCollector(1, nil)
	c.Stop()
	c.Stop()
	c.Stop()
}

func TestCollectorStopOnNilIsSafe(t *testing.T) {
	var c *Collector
	c.Stop()
}
