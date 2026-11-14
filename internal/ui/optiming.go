package ui

import "time"

// processStart is when this agent-deck process started, the start of the
// tui.first_frame span (internal/optiming). Zero until SetProcessStart.
var processStart time.Time

// SetProcessStart records the process start for tui.first_frame. Call once,
// before the Bubble Tea program runs.
func SetProcessStart(t time.Time) { processStart = t }
